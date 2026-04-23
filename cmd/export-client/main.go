package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"

	"traptunnel/internal/frame"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:12000", "export server address")
	count := flag.Int("count", 1, "number of frames to read before exit; 0 means forever")
	flag.Parse()

	conn, err := net.Dial("tcp", *addr)
	if err != nil {
		log.Fatalf("connect export server %s: %v", *addr, err)
	}
	defer conn.Close()

	decoder := frame.NewDecoder()
	buf := make([]byte, 8192)
	received := 0

	for {
		n, err := conn.Read(buf)
		if err != nil {
			if err == io.EOF {
				return
			}
			log.Fatalf("read export stream: %v", err)
		}

		frames, err := decoder.Push(buf[:n])
		if err != nil {
			log.Fatalf("decode export stream: %v", err)
		}

		for _, incoming := range frames {
			received++
			fmt.Fprintf(os.Stdout, "frame=%d node_id=%d seq=%d size=%d\n", received, incoming.NodeID, incoming.Sequence, incoming.TotalLength())
			if *count > 0 && received >= *count {
				return
			}
		}
	}
}
