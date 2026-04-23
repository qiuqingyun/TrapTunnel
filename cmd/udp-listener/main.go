package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net"
	"time"
)

func main() {
	listenAddr := flag.String("listen", "127.0.0.1:162", "UDP listen address")
	showHex := flag.Bool("hex", true, "print payload in hex")
	flag.Parse()

	conn, err := net.ListenPacket("udp4", *listenAddr)
	if err != nil {
		log.Fatalf("listen udp: %v", err)
	}
	defer conn.Close()

	log.Printf("udp-listener started on %s", *listenAddr)

	buf := make([]byte, 65535)
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			log.Fatalf("read udp: %v", err)
		}

		payload := make([]byte, n)
		copy(payload, buf[:n])

		fmt.Printf("[%s] from=%s len=%d\n", time.Now().Format(time.RFC3339), addr.String(), len(payload))
		if *showHex {
			fmt.Printf("hex=%s\n", hex.EncodeToString(payload))
		}
		fmt.Printf("text=%q\n\n", string(payload))
	}
}
