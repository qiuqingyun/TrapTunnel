package main

import (
	"bufio"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/net/ipv4"
)

type Config struct {
	NodeID            uint16
	BServerIP         string
	BServerPort       string
	ListenPort        int
	ReconnectInterval int
	MaxBufferSize     int
}

var (
	cfg     Config
	seqChan chan PacketData
	counter uint32
)

type PacketData struct {
	Seq    uint32
	Packet []byte
}

// 简易 INI 解析器，避免引入第三方包
func loadConfig(path string) {
	file, err := os.Open(path)
	if err != nil {
		log.Fatalf("[!] 无法读取配置文件: %v", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "[") || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			switch key {
			case "node_id":
				fmt.Sscanf(val, "%d", &cfg.NodeID)
			case "b_server_ip":
				cfg.BServerIP = val
			case "b_server_port":
				cfg.BServerPort = val
			case "listen_port":
				fmt.Sscanf(val, "%d", &cfg.ListenPort)
			case "reconnect_interval":
				fmt.Sscanf(val, "%d", &cfg.ReconnectInterval)
			case "max_buffer_size":
				fmt.Sscanf(val, "%d", &cfg.MaxBufferSize)
			}
		}
	}
	seqChan = make(chan PacketData, cfg.MaxBufferSize)
}

func main() {
	configPath := flag.String("c", "sender.conf", "Path to config file")
	flag.Parse()

	loadConfig(*configPath)
	log.Printf("[*] OriginTrap Sender 启动 | NodeID: %d | Target: %s:%s", cfg.NodeID, cfg.BServerIP, cfg.BServerPort)

	go packetProducer()
	tunnelConsumer()
}

func packetProducer() {
	c, err := net.ListenPacket("ip4:udp", "0.0.0.0")
	if err != nil {
		log.Fatalf("[!] Socket 错误 (需root): %v", err)
	}
	r, err := ipv4.NewRawConn(c)
	if err != nil {
		log.Fatal(err)
	}

	for {
		buf := make([]byte, 2048)
		h, payload, _, err := r.ReadFrom(buf)
		if err != nil {
			continue
		}

		if len(payload) >= 4 {
			dstPort := binary.BigEndian.Uint16(payload[2:4])
			if dstPort == uint16(cfg.ListenPort) {
				fullPacket, _ := h.Marshal()
				fullPacket = append(fullPacket, payload...)
				counter++
				select {
				case seqChan <- PacketData{Seq: counter, Packet: fullPacket}:
				default:
				}
			}
		}
	}
}

func tunnelConsumer() {
	addr := net.JoinHostPort(cfg.BServerIP, cfg.BServerPort)
	for {
		conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
		if err != nil {
			time.Sleep(time.Duration(cfg.ReconnectInterval) * time.Second)
			continue
		}
		log.Printf("[✓] 隧道已建立")
		for data := range seqChan {
			totalLen := uint32(6 + len(data.Packet))
			head := make([]byte, 10)
			binary.BigEndian.PutUint32(head[0:4], totalLen)
			binary.BigEndian.PutUint16(head[4:6], cfg.NodeID)
			binary.BigEndian.PutUint32(head[6:10], data.Seq)
			if _, err := conn.Write(append(head, data.Packet...)); err != nil {
				break
			}
		}
		conn.Close()
	}
}