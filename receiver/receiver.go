package main

import (
	"bufio"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/net/ipv4"
	"gopkg.in/natefinch/lumberjack.v2"
)

type Config struct {
	ListenPort    string
	InjectIP      string
	NodeMapping   map[uint16]string
	// Logging
	MaxLogSize    int
	MaxLogBackups int
}

var (
	cfg         Config
	cfgLock     sync.RWMutex
	nodeTracker = make(map[uint16]uint32)
	trackerLock sync.Mutex
	logOutput   io.Writer
)

func getDefaultLogPath() string {
	if runtime.GOOS == "windows" {
		return "receiver.log"
	}
	// Linux / Unix
	return "/var/log/traptunnel/receiver.log"
}

func setupLogger(newCfg Config) {
	logPath := getDefaultLogPath()
	
	// 确保存储日志的目录存在 (仅针对 Linux)
	if runtime.GOOS != "windows" {
		dir := filepath.Dir(logPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.SetOutput(os.Stdout)
			log.Printf("[!] 无法创建日志目录 %s: %v. 将仅输出到控制台。", dir, err)
			return
		}
	}

	l := &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    newCfg.MaxLogSize, // megabytes
		MaxBackups: newCfg.MaxLogBackups,
		MaxAge:     28,   // days
		Compress:   true, // disabled by default
	}

	// MultiWriter: Stdout (for systemd) + File (for rotation)
	logOutput = io.MultiWriter(os.Stdout, l)
	log.SetOutput(logOutput)
}

// 简易 INI 解析
func loadConfig(path string, firstRun bool) {
	file, err := os.Open(path)
	if err != nil {
		log.Printf("[!] 配置文件读取失败: %v", err)
		return
	}
	defer file.Close()

	// 临时配置，默认值
	newCfg := Config{
		ListenPort:    "10000",
		InjectIP:      "127.0.0.1",
		NodeMapping:   make(map[uint16]string),
		MaxLogSize:    10,
		MaxLogBackups: 100,
	}
	
	// 如果不是首次运行，保留旧的 NodeMapping，稍后覆盖
	if !firstRun {
		cfgLock.RLock()
		// 复制旧的 mapping
		for k, v := range cfg.NodeMapping {
			newCfg.NodeMapping[k] = v
		}
		cfgLock.RUnlock()
	}

	scanner := bufio.NewScanner(file)
	currentSection := ""

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentSection = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			k, v := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
			
			switch currentSection {
			case "server":
				switch k {
				case "listen_port":
					newCfg.ListenPort = v
				case "inject_ip":
					newCfg.InjectIP = v
				}
			case "logging":
				switch k {
				case "max_log_size":
					if val, err := strconv.Atoi(v); err == nil {
						newCfg.MaxLogSize = val
					}
				case "max_log_backups":
					if val, err := strconv.Atoi(v); err == nil {
						newCfg.MaxLogBackups = val
					}
				}
			case "nodes":
				if id, err := strconv.ParseUint(k, 10, 16); err == nil {
					newCfg.NodeMapping[uint16(id)] = v
				}
			}
		}
	}

	// 检查日志配置是否变更
	if firstRun || newCfg.MaxLogSize != cfg.MaxLogSize || newCfg.MaxLogBackups != cfg.MaxLogBackups {
		setupLogger(newCfg)
	}

	cfgLock.Lock()
	cfg = newCfg
	cfgLock.Unlock()
	
	log.Println("[-] 配置已加载/更新")
}

func getNodeName(id uint16) string {
	cfgLock.RLock()
	defer cfgLock.RUnlock()
	if name, ok := cfg.NodeMapping[id]; ok {
		return fmt.Sprintf("%s(%d)", name, id)
	}
	return fmt.Sprintf("UNKNOWN(%d)", id)
}

func getInjectIP() string {
	cfgLock.RLock()
	defer cfgLock.RUnlock()
	return cfg.InjectIP
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
	if len(raw) < 20 {
		return
	}

	// 1. 解析 IP 头
	header, _ := ipv4.ParseHeader(raw)
	ihl := header.Len
	udpHeader := raw[ihl : ihl+8]
	pdu := raw[ihl+8:]

	// 2. 修改目的 IP
	header.Dst = net.ParseIP(getInjectIP())
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
	firstPacket := true

	for {
		n, err := conn.Read(tmp)
		if err != nil {
			break
		}
		buffer = append(buffer, tmp[:n]...)

		for len(buffer) >= 4 {
			totalLen := binary.BigEndian.Uint32(buffer[0:4])
			if len(buffer) < int(4+totalLen) {
				break
			}

			header := buffer[4:10]
			packet := buffer[10 : 4+totalLen]
			nodeID = binary.BigEndian.Uint16(header[0:2])
			seq := binary.BigEndian.Uint32(header[2:6])
			
			if firstPacket {
				log.Printf("[+] 节点连接: %s", getNodeName(nodeID))
				firstPacket = false
			}

			// 丢包监测
			trackerLock.Lock()
			if lastSeq, ok := nodeTracker[nodeID]; ok {
				if seq != (lastSeq + 1) {
					log.Printf("[丢包] 节点:%s | 预期:%d | 收到:%d", getNodeName(nodeID), lastSeq+1, seq)
				}
			}
			nodeTracker[nodeID] = seq
			trackerLock.Unlock()

			patchAndInject(packet, rawConn)
			buffer = buffer[4+totalLen:]
		}
	}
	log.Printf("[-] 节点断开: %s", getNodeName(nodeID))
}

func setupSignalHandler(configPath string) {
	c := make(chan os.Signal, 1)
	// 监听 SIGHUP 信号 (Linux下热重载标准信号)
	signal.Notify(c, syscall.SIGHUP)

	go func() {
		for range c {
			log.Println("Received SIGHUP, reloading config...")
			loadConfig(configPath, false)
		}
	}()
}

func main() {
	configPath := flag.String("c", "receiver.conf", "配置文件路径")
	flag.Parse()
	
	// 初始加载配置
	loadConfig(*configPath, true)
	
	// 设置信号监听
	setupSignalHandler(*configPath)

	// 初始化注入 Socket
	packetConn, err := net.ListenPacket("ip4:raw", "0.0.0.0")
	if err != nil {
		log.Fatalf("[!] 需 root 权限: %v", err)
	}
	rawConn, _ := ipv4.NewRawConn(packetConn)

	// 监听隧道
	// 注意: ListenPort 更改通常需要重启，此处只读初始值
	cfgLock.RLock()
	port := cfg.ListenPort
	cfgLock.RUnlock()
	
	l, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("[*] OriginTrap Receiver (Go) 启动 | 端口: %s", port)

	for {
		conn, err := l.Accept()
		if err != nil {
			continue
		}
		go handleNode(conn, rawConn)
	}
}
