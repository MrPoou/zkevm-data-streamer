package main

import (
	"encoding/binary"
	"errors"
	"math/rand"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/0xPolygonHermez/zkevm-data-streamer/datastreamer"
	"github.com/0xPolygonHermez/zkevm-data-streamer/log"
	"github.com/urfave/cli/v2"
)

const (
	EtL2BlockStart datastreamer.EntryType = 1 // EtL2BlockStart entry type
	EtL2Tx         datastreamer.EntryType = 2 // EtL2Tx entry type
	EtL2BlockEnd   datastreamer.EntryType = 3 // EtL2BlockEnd entry type

	StSequencer = 1 // StSequencer sequencer stream type
)

var (
	initSanityEntry    bool   = false
	initSanityBlock    bool   = false
	initSanityBookmark bool   = false
	sanityEntry        uint64 = 0
	sanityBlock        uint64 = 0
	sanityBookmark     uint64 = 0
)

// main runs a datastream server or client
func main() {
	// Set log level
	log.Init(log.Config{
		Environment: "development",
		Level:       "debug",
		Outputs:     []string{"stdout"},
	})

	app := cli.NewApp()
	app.Usage = "Run a datastream server/client/relay demo cli app"

	app.Commands = []*cli.Command{
		{
			Name:    "server",
			Aliases: []string{},
			Usage:   "Run datastream server",
			Flags: []cli.Flag{
				&cli.Uint64Flag{
					Name:        "port",
					Usage:       "exposed port for clients to connect",
					Value:       6900, // nolint:gomnd
					DefaultText: "6900",
				},
				&cli.StringFlag{
					Name:        "file",
					Usage:       "datastream data file name (*.bin)",
					Value:       "datastream.bin",
					DefaultText: "datastream.bin",
				},
				&cli.StringFlag{
					Name:        "log",
					Usage:       "log level (debug|info|warn|error)",
					Value:       "info",
					DefaultText: "info",
				},
				&cli.Uint64Flag{
					Name:        "sleep",
					Usage:       "initial sleep and sleep between atomic operations in ms",
					Value:       0, // nolint:gomnd
					DefaultText: "0",
				},
				&cli.Uint64Flag{
					Name:        "opers",
					Usage:       "number of atomic operations (server will terminate after them)",
					Value:       1000000, // nolint:gomnd
					DefaultText: "1000000",
				},
			},
			Action: runServer,
		},
		{
			Name:    "client",
			Aliases: []string{},
			Usage:   "Run datastream client",
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:        "server",
					Usage:       "datastream server address to connect (IP:port)",
					Value:       "127.0.0.1:6900",
					DefaultText: "127.0.0.1:6900",
				},
				&cli.StringFlag{
					Name:        "from",
					Usage:       "entry number to start the sync/streaming from (latest|0..N)",
					Value:       "latest",
					DefaultText: "latest",
				},
				&cli.StringFlag{
					Name:  "frombookmark",
					Usage: "bookmark to start the sync/streaming from (0..N) (has preference over --from parameter)",
					Value: "none",
				},
				&cli.BoolFlag{
					Name:  "header",
					Usage: "query file header information",
					Value: false,
				},
				&cli.StringFlag{
					Name:  "entry",
					Usage: "entry number to query data (0..N)",
					Value: "none",
				},
				&cli.StringFlag{
					Name:  "bookmark",
					Usage: "entry bookmark to query entry data pointed by it (0..N)",
					Value: "none",
				},
				&cli.BoolFlag{
					Name:  "sanitycheck",
					Usage: "when receiving streaming check entry, bookmark, and block sequence consistency",
					Value: false,
				},
				&cli.StringFlag{
					Name:        "log",
					Usage:       "log level (debug|info|warn|error)",
					Value:       "info",
					DefaultText: "info",
				},
			},
			Action: runClient,
		},
		{
			Name:    "relay",
			Aliases: []string{},
			Usage:   "Run datastream relay",
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:        "server",
					Usage:       "datastream server address to connect (IP:port)",
					Value:       "127.0.0.1:6900",
					DefaultText: "127.0.0.1:6900",
				},
				&cli.Uint64Flag{
					Name:        "port",
					Usage:       "exposed port for clients to connect",
					Value:       7900, // nolint:gomnd
					DefaultText: "7900",
				},
				&cli.StringFlag{
					Name:        "file",
					Usage:       "relay data file name (*.bin)",
					Value:       "datarelay.bin",
					DefaultText: "datarelay.bin",
				},
				&cli.StringFlag{
					Name:        "log",
					Usage:       "log level (debug|info|warn|error)",
					Value:       "info",
					DefaultText: "info",
				},
			},
			Action: runRelay,
		},
	}

	err := app.Run(os.Args)
	if err != nil {
		log.Fatal(err)
		os.Exit(1)
	}
}

// runServer runs a local datastream server and tests its features
func runServer(ctx *cli.Context) error {
	// Set log level
	logLevel := ctx.String("log")
	log.Init(log.Config{
		Environment: "development",
		Level:       logLevel,
		Outputs:     []string{"stdout"},
	})

	log.Info(">> App begin")

	// Parameters
	file := ctx.String("file")
	port := ctx.Uint64("port")
	sleep := ctx.Uint64("sleep")
	numOpersLoop := ctx.Uint64("opers")
	if file == "" || port <= 0 {
		return errors.New("bad/missing parameters")
	}

	// Create stream server
	s, err := datastreamer.NewServer(uint16(port), StSequencer, file, nil)
	if err != nil {
		return err
	}

	// Start stream server
	err = s.Start()
	if err != nil {
		log.Error(">> App error! Start")
		return err
	}

	// Pause for testing purpose
	time.Sleep(time.Duration(sleep) * time.Millisecond)

	end := make(chan uint8)

	go func(chan uint8) {
		var testRollback bool = false
		var latestRollback uint64 = 0

		rand.Seed(time.Now().UnixNano())

		init := s.GetHeader().TotalEntries / 4 // nolint:gomnd

		// Atomic Operations loop
		for n := uint64(0); n < numOpersLoop; n++ {
			// Start atomic operation
			err = s.StartAtomicOp()
			if err != nil {
				log.Error(">> App error! StartAtomicOp")
				return
			}

			// Add stream entries (sample):
			// 1.Bookmark
			_, err := s.AddStreamBookmark(fakeBookmark(init + n))
			if err != nil {
				log.Errorf(">> App error! AddStreamBookmark: %v", err)
			}
			// 2.Block Start
			entryBlockStart, err := s.AddStreamEntry(EtL2BlockStart, fakeDataBlockStart(init+n))
			if err != nil {
				log.Errorf(">> App error! AddStreamEntry type %v: %v", EtL2BlockStart, err)
				return
			}
			// 3.Tx
			numTx := 1 //rand.Intn(20) + 1
			for i := 1; i <= numTx; i++ {
				_, err = s.AddStreamEntry(EtL2Tx, fakeDataTx())
				if err != nil {
					log.Errorf(">> App error! AddStreamEntry type %v: %v", EtL2Tx, err)
					return
				}
			}
			// 4.Block End
			_, err = s.AddStreamEntry(EtL2BlockEnd, fakeDataBlockEnd(init+n))
			if err != nil {
				log.Errorf(">> App error! AddStreamEntry type %v: %v", EtL2BlockEnd, err)
				return
			}

			if !testRollback || entryBlockStart%10 != 0 || latestRollback == entryBlockStart {
				// Commit atomic operation
				err = s.CommitAtomicOp()
				if err != nil {
					log.Error(">> App error! CommitAtomicOp")
					return
				}
			} else {
				// Rollback atomic operation
				err = s.RollbackAtomicOp()
				if err != nil {
					log.Error(">> App error! RollbackAtomicOp")
				}
				latestRollback = entryBlockStart
			}

			// Pause for testing purpose
			time.Sleep(time.Duration(sleep) * time.Millisecond)
		}
		end <- 0
	}(end)

	// Wait for loop to end
	<-end

	log.Info(">> App end")

	return nil
}

func fakeBookmark(blockNum uint64) []byte {
	bookmark := []byte{0} // nolint:gomnd
	bookmark = binary.LittleEndian.AppendUint64(bookmark, blockNum)
	return bookmark
}

func fakeDataBlockStart(blockNum uint64) []byte {
	dataBlockStart := make([]byte, 0)
	dataBlockStart = binary.LittleEndian.AppendUint64(dataBlockStart, 101) // nolint:gomnd
	dataBlockStart = binary.LittleEndian.AppendUint64(dataBlockStart, blockNum)
	dataBlockStart = binary.LittleEndian.AppendUint64(dataBlockStart, uint64(time.Now().Unix()))
	dataBlockStart = append(dataBlockStart, []byte{10, 11, 12, 13, 14, 15, 16, 17, 10, 11, 12, 13, 14, 15, 16, 17, 10, 11, 12, 13, 14, 15, 16, 17, 10, 11, 12, 13, 14, 15, 16, 17}...)
	dataBlockStart = append(dataBlockStart, []byte{20, 21, 22, 23, 24, 20, 21, 22, 23, 24, 20, 21, 22, 23, 24, 20, 21, 22, 23, 24}...)
	dataBlockStart = binary.LittleEndian.AppendUint16(dataBlockStart, 5) // nolint:gomnd
	return dataBlockStart
}

func fakeDataTx() []byte {
	dataTx := make([]byte, 0)                            // nolint:gomnd
	dataTx = append(dataTx, 128)                         // nolint:gomnd
	dataTx = append(dataTx, 1)                           // nolint:gomnd
	dataTx = binary.LittleEndian.AppendUint32(dataTx, 5) // nolint:gomnd
	dataTx = append(dataTx, []byte{1, 2, 3, 4, 5}...)    // nolint:gomnd
	return dataTx
}

func fakeDataBlockEnd(blockNum uint64) []byte {
	dataBlockEnd := make([]byte, 0)
	dataBlockEnd = binary.LittleEndian.AppendUint64(dataBlockEnd, blockNum)
	dataBlockEnd = append(dataBlockEnd, []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}...)
	dataBlockEnd = append(dataBlockEnd, []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}...)
	return dataBlockEnd
}

// runClient runs a local datastream client and tests its features
func runClient(ctx *cli.Context) error {
	// Set log level
	logLevel := ctx.String("log")
	log.Init(log.Config{
		Environment: "development",
		Level:       logLevel,
		Outputs:     []string{"stdout"},
	})

	// Parameters
	server := ctx.String("server")
	if server == "" {
		return errors.New("bad/missing parameters")
	}
	from := ctx.String("from")
	fromBookmark := ctx.String("frombookmark")
	queryHeader := ctx.Bool("header")
	queryEntry := ctx.String("entry")
	queryBookmark := ctx.String("bookmark")
	sanityCheck := ctx.Bool("sanitycheck")

	// Create client
	c, err := datastreamer.NewClient(server, StSequencer)
	if err != nil {
		return err
	}

	// Set process entry callback function
	if !sanityCheck {
		c.SetProcessEntryFunc(printEntryNum)
	} else {
		c.SetProcessEntryFunc(checkEntryBlockSanity)
	}

	// Start client (connect to the server)
	err = c.Start()
	if err != nil {
		return err
	}

	// Query file header information
	if queryHeader {
		err = c.ExecCommand(datastreamer.CmdHeader)
		if err != nil {
			log.Infof("Error: %v", err)
		} else {
			log.Infof("QUERY HEADER: TotalEntries[%d] TotalLength[%d]", c.Header.TotalEntries, c.Header.TotalLength)
		}
		return nil
	}

	// Query entry option
	if queryEntry != "none" {
		qEntry, err := strconv.Atoi(queryEntry)
		if err != nil {
			return err
		}
		c.FromEntry = uint64(qEntry)
		err = c.ExecCommand(datastreamer.CmdEntry)
		if err != nil {
			log.Infof("Error: %v", err)
		} else {
			log.Infof("QUERY ENTRY %d: Entry[%d] Length[%d] Type[%d] Data[%v]", qEntry, c.Entry.Number, c.Entry.Length, c.Entry.Type, c.Entry.Data)
		}
		return nil
	}

	// Query bookmark option
	if queryBookmark != "none" {
		qBookmark, err := strconv.Atoi(queryBookmark)
		if err != nil {
			return err
		}
		qBook := []byte{0} // nolint:gomnd
		qBook = binary.LittleEndian.AppendUint64(qBook, uint64(qBookmark))
		c.FromBookmark = qBook
		err = c.ExecCommand(datastreamer.CmdBookmark)
		if err != nil {
			log.Infof("Error: %v", err)
		} else {
			log.Infof("QUERY BOOKMARK %v: Entry[%d] Length[%d] Type[%d] Data[%v]", qBook, c.Entry.Number, c.Entry.Length, c.Entry.Type, c.Entry.Data)
		}
		return nil
	}

	// Command header: Get status
	err = c.ExecCommand(datastreamer.CmdHeader)
	if err != nil {
		return err
	}

	if fromBookmark != "none" {
		// Command StartBookmark: Sync and start streaming receive from bookmark
		fromBookNum, err := strconv.Atoi(fromBookmark)
		if err != nil {
			return err
		}
		bookmark := []byte{0} // nolint:gomnd
		bookmark = binary.LittleEndian.AppendUint64(bookmark, uint64(fromBookNum))
		c.FromBookmark = bookmark
		err = c.ExecCommand(datastreamer.CmdStartBookmark)
		if err != nil {
			return err
		}
	} else {
		// Command start: Sync and start streaming receive from entry number
		if from == "latest" { // nolint:gomnd
			c.FromEntry = c.Header.TotalEntries
		} else {
			fromNum, err := strconv.Atoi(from)
			if err != nil {
				return err
			}
			c.FromEntry = uint64(fromNum)
		}
		err = c.ExecCommand(datastreamer.CmdStart)
		if err != nil {
			return err
		}
	}

	// After the initial sync, run until Ctl+C
	interruptSignal := make(chan os.Signal, 1)
	signal.Notify(interruptSignal, os.Interrupt, syscall.SIGTERM)
	<-interruptSignal

	// Command stop: Stop streaming
	err = c.ExecCommand(datastreamer.CmdStop)
	if err != nil {
		return err
	}

	log.Info("Client stopped")
	return nil
}

// printEntryNum prints basic data of the entry
func printEntryNum(e *datastreamer.FileEntry, c *datastreamer.StreamClient, s *datastreamer.StreamServer) error {
	log.Infof("PROCESS entry(%s): %d | %d | %d | %d", c.Id, e.Number, e.Length, e.Type, len(e.Data))
	return nil
}

// checkEntryBlockSanity checks entry, bookmark, and block sequence consistency
func checkEntryBlockSanity(e *datastreamer.FileEntry, c *datastreamer.StreamClient, s *datastreamer.StreamServer) error {
	// Sanity check initialization
	if !initSanityEntry {
		initSanityEntry = true
		if c.FromEntry > 0 {
			sanityEntry = c.FromEntry
		} else {
			sanityEntry = 0
		}
	}

	// Log work in progress
	if e.Number%100000 == 0 {
		log.Infof("Checking entry #%d...", e.Number)
	}

	// Sanity check for entry sequence
	if sanityEntry > 0 {
		if e.Number != sanityEntry {
			if e.Number < sanityEntry {
				log.Warnf("(X) SANITY CHECK failed: REPEATED entries? Received[%d] | Entry expected[%d]", e.Number, sanityEntry)
			} else {
				log.Warnf("(X) SANITY CHECK failed: GAP entries? Received[%d] | Entry expected[%d]", e.Number, sanityEntry)
			}
			return errors.New("sanity check failed for entry sequence")
		}
	} else {
		if e.Number != 0 {
			log.Warnf("(X) SANITY CHECK failed: Entry received[%d] | Entry expected[0]", e.Number)
			return errors.New("sanity check failed for entry sequence")
		}
	}
	sanityEntry++

	// Sanity check for block sequence
	if e.Type == EtL2BlockStart {
		blockNum := binary.LittleEndian.Uint64(e.Data[8:16])
		if sanityBlock > 0 {
			if blockNum != sanityBlock {
				if blockNum < sanityBlock {
					log.Infof("(X) SANITY CHECK failed (%d): REPEATED blocks? Received[%d] | Block expected[%d]", e.Number, blockNum, sanityBlock)
				} else {
					log.Infof("(X) SANITY CHECK failed (%d): GAP blocks? Received[%d] | Block expected[%d]", e.Number, blockNum, sanityBlock)
				}
				sanityBlock = blockNum
			}
		} else {
			if blockNum != 0 {
				if initSanityBlock {
					log.Infof("(X) SANITY CHECK failed (%d): Block received[%d] | Block expected[0]", e.Number, blockNum)
					sanityBlock = 0
				} else {
					log.Infof("SANITY CHECK note (%d): First Block received[%d]", e.Number, blockNum)
					sanityBlock = blockNum
				}
				initSanityBlock = true
			}
		}
		sanityBlock++
	}

	// Sanity check for bookmarks
	if e.Type == datastreamer.EtBookmark {
		bookmarkNum := binary.LittleEndian.Uint64(e.Data[1:9])
		if sanityBookmark > 0 {
			if bookmarkNum != sanityBookmark {
				if bookmarkNum < sanityBookmark {
					log.Infof("(X) SANITY CHECK failed (%d): REPEATED bookmarks? Received[%d] | Bookmark expected[%d]", e.Number, bookmarkNum, sanityBookmark)
				} else {
					log.Infof("(X) SANITY CHECK failed (%d): GAP bookmarks? Received[%d] | Bookmark expected[%d]", e.Number, bookmarkNum, sanityBookmark)
				}
				sanityBookmark = bookmarkNum
			}
		} else {
			if bookmarkNum != 0 {
				if initSanityBookmark {
					log.Infof("(X) SANITY CHECK failed (%d): Bookmark received[%d] | Bookmark expected[0]", e.Number, bookmarkNum)
					sanityBookmark = 0
				} else {
					log.Infof("SANITY CHECK note (%d): First Bookmark received[%d]", e.Number, bookmarkNum)
					sanityBookmark = bookmarkNum
				}
				initSanityBookmark = true
			}
		}
		sanityBookmark++
	}

	// Sanity check end condition
	if e.Number+1 >= c.Header.TotalEntries {
		log.Infof("SANITY CHECK finished! From entry [%d] to entry [%d]", c.FromEntry, c.Header.TotalEntries-1)
		return errors.New("sanity check finished")
	}

	return nil
}

// runRelay runs a local datastream relay
func runRelay(ctx *cli.Context) error {
	// Set log level
	logLevel := ctx.String("log")
	log.Init(log.Config{
		Environment: "development",
		Level:       logLevel,
		Outputs:     []string{"stdout"},
	})

	log.Info(">> App begin")

	// Parameters
	server := ctx.String("server")
	port := ctx.Uint64("port")
	file := ctx.String("file")
	if server == "" || file == "" || port <= 0 {
		return errors.New("bad/missing parameters")
	}

	// Create relay server
	r, err := datastreamer.NewRelay(server, uint16(port), StSequencer, file, nil)
	if err != nil {
		return err
	}

	// Start relay server
	err = r.Start()
	if err != nil {
		log.Error(">> App error! Start")
		return err
	}

	// Run until Ctl+C
	interruptSignal := make(chan os.Signal, 1)
	signal.Notify(interruptSignal, os.Interrupt, syscall.SIGTERM)
	<-interruptSignal

	log.Info(">> App end")
	return nil
}
