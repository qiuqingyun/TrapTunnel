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
	"sync"
	"time"

	"golang.org/x/net/ipv4"
)

type Config struct {
	ListenPort string
	InjectIP   string
	LogFile    string
}

var (
	cfg         Config
	nodeTracker = make(map[uint16]uint32)
	trackerLock sync.Mutex
)

// 简易 INI 解析
func loadConfig(path string) {
	file, err := os.Open(path)
	if err != nil {
		log.Fatalf("[!] 配置文件读取失败: %v", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "[") || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			k, v := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
			switch k {
			case "listen_port": cfg.ListenPort = v
			case "inject_ip":   cfg.InjectIP = v
			case "log_file":    cfg.LogFile = v
			}
		}
	}
}

// RFC 791 Checksum 算法
func checksum(data []byte) uint16 {
	var sum uint32
	for i := 0; i < len(data)-1; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(data[i : i+2]))
	}
	if len(data)%2 != 0 {
		sum += uint32(data[len(data)-1]) << 8
	}
	for sum > 0xffff {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	return ^uint16(sum)
}

func patchAndInject(raw []byte, rawConn *ipv4.RawConn) {
	if len(raw) < 20 { return }
	
	// 1. 解析 IP 头
	header, _ := ipv4.ParseHeader(raw)
	ihl := header.Len
	udpHeader := raw[ihl : ihl+8]
	pdu := raw[ihl+8:]

	// 2. 修改目的 IP
	header.Dst = net.ParseIP(cfg.InjectIP)
	header.Checksum = 0 // ipv4.RawConn 会自动计算 IP 校验和

	// 3. 重算 UDP 校验和 (需伪首部)
	// 构造伪首部: Src(4) + Dst(4) + Zero(1) + Proto(1) + Len(2)
	udpLen := binary.BigEndian.Uint16(udpHeader[4:6])
	pseudo := make([]byte, 12)
	copy(pseudo[0:4], header.Src.To4())
	copy(pseudo[4:8], header.Dst.To4())
	pseudo[9] = 17 // UDP
	binary.BigEndian.PutUint16(pseudo[10:12], udpLen)

	// 清空原 UDP Checksum 位
	udpHeader[6], udpHeader[7] = 0, 0
	checkData := append(pseudo, append(udpHeader, pdu...)...)
	newUDPCheck := checksum(checkData)
	binary.BigEndian.PutUint16(udpHeader[6:8], newUDPCheck)

	// 4. 写入 Raw Socket
	if err := rawConn.WriteTo(header, append(udpHeader, pdu...), nil); err != nil {
		log.Printf("[错误] 注入失败: %v", err)
	}
}

func handleNode(conn net.Conn, rawConn *ipv4.RawConn) {
	defer conn.Close()
	buffer := make([]byte, 0)
	tmp := make([]byte, 8192)
	var nodeID uint16

	for {
		n, err := conn.Read(tmp)
		if err != nil { break }
		buffer = append(buffer, tmp[:n]...)

		for len(buffer) >= 4 {
			totalLen := binary.BigEndian.Uint32(buffer[0:4])
			if len(buffer) < int(4+totalLen) { break }

			header := buffer[4:10]
			packet := buffer[10 : 4+totalLen]
			nodeID = binary.BigEndian.Uint16(header[0:2])
			seq := binary.BigEndian.Uint32(header[2:6])

			// 丢包监测
			trackerLock.Lock()
			if lastSeq, ok := nodeTracker[nodeID]; ok {
				if seq != (lastSeq+1) {
					log.Printf("[丢包] 节点:%d | 预期:%d | 收到:%d", nodeID, lastSeq+1, seq)
				}
			}
			nodeTracker[nodeID] = seq
			trackerLock.Unlock()

			patchAndInject(packet, rawConn)
			buffer = buffer[4+totalLen:]
		}
	}
	log.Printf("[-] 节点 %d 断开连接", nodeID)
}

func main() {
	configPath := flag.String("c", "receiver.conf", "配置文件路径")
	flag.Parse()
	loadConfig(*configPath)

	// 初始化注入 Socket
	packetConn, err := net.ListenPacket("ip4:raw", "0.0.0.0")
	if err != nil { log.Fatalf("[!] 需 root 权限: %v", err) }
	rawConn, _ := ipv4.NewRawConn(packetConn)

	// 监听隧道
	l, err := net.Listen("tcp", ":"+cfg.ListenPort)
	if err != nil { log.Fatal(err) }
	log.Printf("[*] OriginTrap Receiver (Go) 启动 | 端口: %s", cfg.ListenPort)

	for {
		conn, err := l.Accept()
		if err != nil { continue }
		go handleNode(conn, rawConn)
	}
}