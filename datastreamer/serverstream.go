package datastreamer

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

const (
	// Stream type
	StSequencer = 1 // Sequencer

	// Commands
	CmdStart  = 1
	CmdStop   = 2
	CmdHeader = 3

	// Client status
	csStarted = 1
	csStopped = 2

	// Transaction status
	txNone       = 0
	txStarted    = 1
	txCommitting = 2
)

type txStream struct {
	status       uint8
	txAfterEntry uint64
}

type clientStream struct {
	conn   net.Conn
	status uint8
}

type ServerStream struct {
	port     uint16 // server stream port
	fileName string // stream file name

	streamType uint64
	ln         net.Listener
	clients    map[string]clientStream

	lastEntry uint64
	tx        txStream
	fs        FileStream
}

type ResultEntry struct {
	isEntry  uint8 // 0xff: Result
	length   uint32
	errorNum uint32 // 0:No error
	errorStr []byte
}

func New(port uint16, fileName string) (ServerStream, error) {
	// Create the server data stream
	s := ServerStream{
		port:     port,
		fileName: fileName,

		streamType: StSequencer,
		ln:         nil,
		clients:    make(map[string]clientStream),
		lastEntry:  0,

		tx: txStream{
			status:       txNone,
			txAfterEntry: 0,
		},
	}

	// Open (or create) the data stream file
	var err error
	s.fs, err = PrepareStreamFile(s.fileName, s.streamType)
	if err != nil {
		return s, err
	}

	return s, nil
}

func (s *ServerStream) Start() error {
	// Start the server data stream
	var err error
	s.ln, err = net.Listen("tcp", ":"+strconv.Itoa(int(s.port)))
	if err != nil {
		fmt.Println("Error creating datastream server:", s.port, err)
		return err
	}

	// Wait for clients connections
	fmt.Println("Listening on port:", s.port)
	go s.waitConnections()

	return nil
}

func (s *ServerStream) waitConnections() {
	defer s.ln.Close()

	for {
		conn, err := s.ln.Accept()
		if err != nil {
			fmt.Println("Error accepting new connection:", err)
			time.Sleep(2 * time.Second)
			continue
		}

		// Goroutine to manage client (command requests and entries stream)
		go s.handleConnection(conn)
	}
}

func (s *ServerStream) handleConnection(conn net.Conn) {
	defer conn.Close()

	clientId := conn.RemoteAddr().String()
	fmt.Println("New connection:", conn.RemoteAddr())

	client := clientStream{
		conn:   conn,
		status: csStopped,
	}

	s.clients[clientId] = client

	reader := bufio.NewReader(conn)
	for {
		// Read command and stream type
		command, err := readFullUint64(reader)
		if err != nil {
			return //TODO
		}
		st, err := readFullUint64(reader)
		if err != nil {
			return //TODO
		}
		if st != s.streamType {
			fmt.Println("Mismatch stream type, killed:", clientId)
			return //TODO
		}

		// Manage the requested command
		fmt.Printf("Command %d received from %s\n", command, clientId)
		err = s.processCommand(command, clientId)
		if err != nil {
			// Kill client connection
			return
		}
	}
}

func readFullUint64(reader *bufio.Reader) (uint64, error) {
	// Read 8 bytes (uint64 value)
	buffer := make([]byte, 8)
	n, err := io.ReadFull(reader, buffer)
	if err != nil {
		if err == io.EOF {
			fmt.Println("Client close connection")
		} else {
			fmt.Println("Error reading from client:", err)
		}
		return 0, err
	}

	// Convert bytes to uint64
	var value uint64
	err = binary.Read(bytes.NewReader(buffer[:n]), binary.BigEndian, &value)
	if err != nil {
		fmt.Println("Error converting bytes to uint64")
		return 0, err
	}

	return value, nil
}

func (s *ServerStream) processCommand(command uint64, clientId string) error {
	client := s.clients[clientId]

	var err error = nil
	var errNum uint32 = 0

	// Manage each different kind of command request from a client
	switch command {
	case CmdStart:
		if client.status != csStopped {
			fmt.Println("Stream to client already started!")
			err = errors.New("client already started")
		} else {
			client.status = csStarted
			// TODO
		}

	case CmdStop:
		if client.status != csStarted {
			fmt.Println("Stream to client already stopped!")
			err = errors.New("client already stopped")
		} else {
			client.status = csStopped
			// TODO
		}

	case CmdHeader:
		if client.status != csStopped {
			fmt.Println("Header command not allowed, stream started!")
			err = errors.New("header command not allowed")
		}

	default:
		fmt.Println("Invalid command!")
		err = errors.New("invalid command")
	}

	var errStr string
	if err != nil {
		errStr = err.Error()
	} else {
		errStr = "OK"
	}
	err = s.sendResultEntry(errNum, errStr, clientId)
	return err
}

// Send the response to a command that is a result entry
func (s *ServerStream) sendResultEntry(errorNum uint32, errorStr string, clientId string) error {
	// Prepare the result entry
	byteSlice := []byte(errorStr)

	entry := ResultEntry{
		isEntry:  0xff,
		length:   uint32(len(byteSlice) + 1 + 4 + 4),
		errorNum: errorNum,
		errorStr: byteSlice,
	}

	// Convert struct to binary bytes
	binaryEntry := encodeResultEntryToBinary(entry)
	fmt.Println("result entry:", binaryEntry)

	// Send the result entry to the client
	conn := s.clients[clientId].conn
	writer := bufio.NewWriter(conn)
	_, err := writer.Write(binaryEntry)
	if err != nil {
		fmt.Println("Error sending result entry")
	}
	writer.Flush()

	return nil
}

// Encode/convert from an entry type to binary bytes slice
func encodeResultEntryToBinary(e ResultEntry) []byte {
	be := make([]byte, 1)
	be[0] = e.isEntry
	be = binary.BigEndian.AppendUint32(be, e.length)
	be = binary.BigEndian.AppendUint32(be, e.errorNum)
	be = append(be, e.errorStr...)
	return be
}

// Decode/convert from binary bytes slice to an entry type
func DecodeBinaryToResultEntry(b []byte) (ResultEntry, error) {
	e := ResultEntry{}

	if len(b) < 10 {
		fmt.Println("Invalid binary result entry")
		return e, errors.New("invalid binary result entry")
	}

	e.isEntry = b[0]
	e.length = binary.BigEndian.Uint32(b[1:5])
	e.errorNum = binary.BigEndian.Uint32(b[5:9])
	e.errorStr = b[9:]

	if uint32(len(e.errorStr)) != e.length-1-4-4 {
		fmt.Println("Error decoding binary result entry")
		return e, errors.New("error decoding binary result entry")
	}

	return e, nil
}

func PrintResultEntry(e ResultEntry) {
	fmt.Printf("isEntry: [%d]\n", e.isEntry)
	fmt.Printf("length: [%d]\n", e.length)
	fmt.Printf("errorNum: [%d]\n", e.errorNum)
	fmt.Printf("errorStr: [%s]\n", e.errorStr)
}

// Internal interface:
func (s *ServerStream) StartStreamTx() error {
	s.tx.status = txStarted
	s.tx.txAfterEntry = s.lastEntry
	return nil
}

func (s *ServerStream) AddStreamEntry(etype uint32, data []uint8) (uint64, error) {
	s.lastEntry++
	return s.lastEntry, nil
}

func (s *ServerStream) CommitStreamTx() error {
	s.tx.status = txCommitting
	// TODO: work
	s.tx.status = txNone
	return nil
}