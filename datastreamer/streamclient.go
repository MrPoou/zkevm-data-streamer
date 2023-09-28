package datastreamer

import (
	"encoding/binary"
	"errors"
	"io"
	"net"

	"github.com/0xPolygonHermez/zkevm-data-streamer/log"
	"go.uber.org/zap/zapcore"
)

const (
	resultsBuffer = 32  // Buffers for the results channel
	headersBuffer = 32  // Buffers for the headers channel
	entriesBuffer = 128 // Buffers for the entries channel
)

type StreamClient struct {
	server     string // Server address to connect IP:port
	streamType StreamType
	conn       net.Conn
	id         string // Client id

	FromEntry uint64      // Set starting entry data (for Start command)
	Header    HeaderEntry // Header info received (from Header command)

	results chan ResultEntry // Channel to read command results
	headers chan HeaderEntry // Channel to read header entries from the command Header
	entries chan FileEntry   // Channel to read data entries from the streaming

	entriesDef map[EntryType]EntityDefinition
}

func NewClient(server string, streamType StreamType) (StreamClient, error) {
	// Create the client data stream
	c := StreamClient{
		server:     server,
		streamType: streamType,
		id:         "",
		FromEntry:  0,

		results: make(chan ResultEntry, resultsBuffer),
		headers: make(chan HeaderEntry, headersBuffer),
		entries: make(chan FileEntry, entriesBuffer),
	}
	return c, nil
}

func (c *StreamClient) Start() error {
	// Connect to server
	var err error
	c.conn, err = net.Dial("tcp", c.server)
	if err != nil {
		log.Errorf("Error connecting to server %s: %v", c.server, err)
		return err
	}

	c.id = c.conn.LocalAddr().String()
	log.Infof("%s Connected to server: %s", c.id, c.server)

	// Goroutine to read from the server all entry types
	go c.readEntries()

	// Goroutine to consume streaming entries
	go c.getStreaming()

	return nil
}

func (c *StreamClient) SetEntriesDef(entriesDef map[EntryType]EntityDefinition) {
	c.entriesDef = entriesDef
}

func (c *StreamClient) ExecCommand(cmd Command) error {
	log.Infof("%s Executing command %d[%s]...", c.id, cmd, StrCommand[cmd])

	// Check valid command
	if cmd < CmdStart || cmd > CmdHeader {
		log.Errorf("%s Invalid command %d", c.id, cmd)
		return errors.New("invalid command")
	}

	// Send command
	err := writeFullUint64(uint64(cmd), c.conn)
	if err != nil {
		log.Errorf("%s %v", c.id, err)
		return err
	}
	// Send stream type
	err = writeFullUint64(uint64(c.streamType), c.conn)
	if err != nil {
		log.Errorf("%s %v", c.id, err)
		return err
	}

	// Send the Start command parameter
	if cmd == CmdStart {
		log.Infof("%s ...from entry %d", c.id, c.FromEntry)
		// Send starting/from entry num	ber
		err = writeFullUint64(c.FromEntry, c.conn)
		if err != nil {
			log.Errorf("%s %v", c.id, err)
			return err
		}
	}

	// Get command result
	r := c.getResult(cmd)
	if r.errorNum != uint32(CmdErrOK) {
		return errors.New("result command error")
	}

	// Get header entry
	if cmd == CmdHeader {
		h := c.getHeader()
		c.Header = h
	}

	return nil
}

func writeFullUint64(value uint64, conn net.Conn) error {
	buffer := make([]byte, 8)
	binary.BigEndian.PutUint64(buffer, uint64(value))

	var err error
	if conn != nil {
		_, err = conn.Write(buffer)
	} else {
		err = errors.New("error nil connection")
	}
	if err != nil {
		log.Errorf("%s Error sending to server: %v", conn.RemoteAddr().String(), err)
		return err
	}
	return nil
}

// Read bytes from server connection and returns a file/stream data entry type
func (c *StreamClient) readDataEntry() (FileEntry, error) {
	d := FileEntry{}

	// Read the rest of fixed size fields
	buffer := make([]byte, FixedSizeFileEntry-1)
	_, err := io.ReadFull(c.conn, buffer)
	if err != nil {
		if err == io.EOF {
			log.Warnf("%s Server close connection", c.id)
		} else {
			log.Errorf("%s Error reading from server: %v", c.id, err)
		}
		return d, err
	}
	packet := []byte{PtData}
	buffer = append(packet, buffer...)

	// Read variable field (data)
	length := binary.BigEndian.Uint32(buffer[1:5])
	if length < FixedSizeFileEntry {
		log.Errorf("%s Error reading data entry", c.id)
		return d, errors.New("error reading data entry")
	}

	bufferAux := make([]byte, length-FixedSizeFileEntry)
	_, err = io.ReadFull(c.conn, bufferAux)
	if err != nil {
		if err == io.EOF {
			log.Warnf("%s Server close connection", c.id)
		} else {
			log.Errorf("%s Error reading from server: %v", c.id, err)
		}
		return d, err
	}
	buffer = append(buffer, bufferAux...)

	// Decode binary data to data entry struct
	d, err = DecodeBinaryToFileEntry(buffer)
	if err != nil {
		return d, err
	}

	return d, nil
}

// Read bytes from server connection and returns a header entry type
func (c *StreamClient) readHeaderEntry() (HeaderEntry, error) {
	h := HeaderEntry{}

	// Read the rest of header bytes
	buffer := make([]byte, headerSize-1)
	n, err := io.ReadFull(c.conn, buffer)
	if err != nil {
		log.Errorf("Error reading the header: %v", err)
		return h, err
	}
	if n != headerSize-1 {
		log.Error("Error getting header info")
		return h, errors.New("error getting header info")
	}
	packet := []byte{PtHeader}
	buffer = append(packet, buffer...)

	// Decode bytes stream to header entry struct
	h, err = decodeBinaryToHeaderEntry(buffer)
	if err != nil {
		log.Error("Error decoding binary header")
		return h, err
	}

	return h, nil
}

// Read bytes from server connection and returns a result entry type
func (c *StreamClient) readResultEntry() (ResultEntry, error) {
	e := ResultEntry{}

	// Read the rest of fixed size fields
	buffer := make([]byte, FixedSizeResultEntry-1)
	_, err := io.ReadFull(c.conn, buffer)
	if err != nil {
		if err == io.EOF {
			log.Warnf("%s Server close connection", c.id)
		} else {
			log.Errorf("%s Error reading from server: %v", c.id, err)
		}
		return e, err
	}
	packet := []byte{PtResult}
	buffer = append(packet, buffer...)

	// Read variable field (errStr)
	length := binary.BigEndian.Uint32(buffer[1:5])
	if length < FixedSizeResultEntry {
		log.Errorf("%s Error reading result entry", c.id)
		return e, errors.New("error reading result entry")
	}

	bufferAux := make([]byte, length-FixedSizeResultEntry)
	_, err = io.ReadFull(c.conn, bufferAux)
	if err != nil {
		if err == io.EOF {
			log.Warnf("%s Server close connection", c.id)
		} else {
			log.Errorf("%s Error reading from server: %v", c.id, err)
		}
		return e, err
	}
	buffer = append(buffer, bufferAux...)

	// Decode binary entry result
	e, err = DecodeBinaryToResultEntry(buffer)
	if err != nil {
		return e, err
	}
	// PrintResultEntry(e)
	return e, nil
}

// Goroutine to read from the server all type of packets
func (c *StreamClient) readEntries() {
	defer c.conn.Close()

	for {
		// Read packet type
		packet := make([]byte, 1)
		_, err := io.ReadFull(c.conn, packet)
		if err != nil {
			if err == io.EOF {
				log.Warnf("%s Server close connection", c.id)
			} else {
				log.Errorf("%s Error reading from server: %v", c.id, err)
			}
			return
		}

		// Manage packet type
		switch packet[0] {
		case PtResult:
			// Read result entry data
			r, err := c.readResultEntry()
			if err != nil {
				return
			}
			// Send data to results channel
			c.results <- r

		case PtHeader:
			// Read header entry data
			h, err := c.readHeaderEntry()
			if err != nil {
				return
			}
			// Send data to headers channel
			c.headers <- h

		case PtData:
			// Read file/stream entry data
			e, err := c.readDataEntry()
			if err != nil {
				return
			}
			// Send data to stream entries channel
			c.entries <- e

		default:
			// Unknown type
			log.Warnf("%s Unknown packet type %d", c.id, packet[0])
			continue
		}
	}
}

// Consume a result entry
func (c *StreamClient) getResult(cmd Command) ResultEntry {
	// Get result entry
	r := <-c.results
	log.Infof("%s Result %d[%s] received for command %d[%s]", c.id, r.errorNum, r.errorStr, cmd, StrCommand[cmd])
	return r
}

// Consume a header entry
func (c *StreamClient) getHeader() HeaderEntry {
	h := <-c.headers
	log.Infof("%s Header received info: TotalEntries[%d], TotalLength[%d]", c.id, h.TotalEntries, h.TotalLength)
	return h
}

// Goroutine to consume streaming data entries
func (c *StreamClient) getStreaming() {
	for {
		e := <-c.entries

		// Process the data entry
		err := c.processEntry(e)
		if err != nil {
			log.Errorf("%s Error processing entry %d", c.id, e.entryNum)
		}
	}
}

// DO YOUR CUSTOM BUSINESS LOGIC
func (c *StreamClient) processEntry(e FileEntry) error {
	// Log data entry fields
	if log.GetLevel() == zapcore.DebugLevel {
		entity := c.entriesDef[e.entryType]
		if entity.Name != "" {
			log.Debugf("Data entry(%s): %d | %d | %d | %d | %s", c.id, e.entryNum, e.packetType, e.length, e.entryType, entity.toString(e.data))
		} else {
			log.Warnf("Data entry(%s): %d | %d | %d | %d | No definition for this entry type", c.id, e.entryNum, e.packetType, e.length, e.entryType)
		}
	} else {
		log.Infof("Data entry(%s): %d | %d | %d | %d | %d", c.id, e.entryNum, e.packetType, e.length, e.entryType, len(e.data))
	}

	return nil
}
