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
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/ipv4"
	"gopkg.in/natefinch/lumberjack.v2"
)

type Config struct {
	NodeID            uint16
	Servers           []string // 支持多服务器: "ip:port"
	ListenPort        int
	ReconnectInterval int
	MaxBufferSize     int
	// Logging config
	// LogFile 已移除，使用默认路径
	MaxLogSize    int // MB
	MaxLogBackups int
}

var (
	cfg         Config
	cfgLock     sync.RWMutex
	seqChan     chan PacketData
	counter     uint32
	currentConn net.Conn
	connLock    sync.Mutex
	lastModTime time.Time
	logOutput   io.Writer
)

type PacketData struct {
	Seq    uint32
	Packet []byte
}

func getDefaultLogPath() string {
	if runtime.GOOS == "windows" {
		return "sender.log"
	}
	// Linux / Unix
	return "/var/log/traptunnel/sender.log"
}

func setupLogger(newCfg Config) {
	logPath := getDefaultLogPath()
	
	// 确保存储日志的目录存在 (仅针对 Linux 路径 /var/log/traptunnel)
	if runtime.GOOS != "windows" {
		dir := filepath.Dir(logPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			// 如果无法创建目录 (例如无权限)，回退到 stdout 并打印错误
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

	// Write to both stdout (for systemd) and file (for persistence/rotation)
	logOutput = io.MultiWriter(os.Stdout, l)
	log.SetOutput(logOutput)
}

// 简易 INI 解析器
func loadConfig(path string, firstRun bool) {
	file, err := os.Open(path)
	if err != nil {
		log.Printf("[!] 无法读取配置文件: %v", err)
		return
	}
	defer file.Close()

	// 临时配置对象，避免读取一半被使用
	var newCfg Config
	// 设置默认值或继承旧值
	if !firstRun {
		cfgLock.RLock()
		newCfg = cfg
		cfgLock.RUnlock()
		// 清空 Servers 以便重新加载
		newCfg.Servers = []string{}
	} else {
		// 默认值
		newCfg.ReconnectInterval = 5
		newCfg.MaxBufferSize = 2000
		newCfg.MaxLogSize = 10
		newCfg.MaxLogBackups = 100
	}

	// 临时变量兼容旧配置
	var bServerIP, bServerPort string

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
				fmt.Sscanf(val, "%d", &newCfg.NodeID)
			case "servers":
				servers := strings.Split(val, ",")
				for _, s := range servers {
					s = strings.TrimSpace(s)
					if s != "" {
						newCfg.Servers = append(newCfg.Servers, s)
					}
				}
			case "b_server_ip":
				bServerIP = val
			case "b_server_port":
				bServerPort = val
			case "listen_port":
				fmt.Sscanf(val, "%d", &newCfg.ListenPort)
			case "reconnect_interval":
				fmt.Sscanf(val, "%d", &newCfg.ReconnectInterval)
			case "max_buffer_size":
				if firstRun {
					fmt.Sscanf(val, "%d", &newCfg.MaxBufferSize)
				}
			case "max_log_size":
				fmt.Sscanf(val, "%d", &newCfg.MaxLogSize)
			case "max_log_backups":
				fmt.Sscanf(val, "%d", &newCfg.MaxLogBackups)
			}
		}
	}

	// 兼容旧配置格式
	if len(newCfg.Servers) == 0 && bServerIP != "" && bServerPort != "" {
		newCfg.Servers = append(newCfg.Servers, net.JoinHostPort(bServerIP, bServerPort))
	}

	// 检查是否需要更新 Logger
	// 仅在首次运行或日志配置变更时更新
	if firstRun || newCfg.MaxLogSize != cfg.MaxLogSize || newCfg.MaxLogBackups != cfg.MaxLogBackups {
		setupLogger(newCfg)
	}

	// 更新全局配置
	cfgLock.Lock()
	cfg = newCfg
	// lastModTime 已经不再使用了，因为我们改用信号触发
	cfgLock.Unlock()

	// 如果不是首次运行，且发生了配置变更，强制断开连接以触发重连
	if !firstRun {
		log.Println("[*] 配置已热重载")
		connLock.Lock()
		if currentConn != nil {
			currentConn.Close()
			currentConn = nil
		}
		connLock.Unlock()
	} else {
		seqChan = make(chan PacketData, cfg.MaxBufferSize)
	}
}

func signalWatcher(path string) {
	sigChan := make(chan os.Signal, 1)
	// 监听 SIGHUP 信号 (Linux下重载配置的标准信号)
	signal.Notify(sigChan, syscall.SIGHUP)

	for range sigChan {
		log.Println("[*] 收到 SIGHUP 信号，正在重载配置...")
		loadConfig(path, false)
	}
}

func main() {
	configPath := flag.String("c", "sender.conf", "Path to config file")
	flag.Parse()

	loadConfig(*configPath, true)
	cfgLock.RLock()
	log.Printf("[*] OriginTrap Sender 启动 | NodeID: %d | Targets: %v", cfg.NodeID, cfg.Servers)
	cfgLock.RUnlock()

	go signalWatcher(*configPath)
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
			// 获取当前监听端口
			cfgLock.RLock()
			listenPort := cfg.ListenPort
			cfgLock.RUnlock()

			if dstPort == uint16(listenPort) {
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
	for {
		// 获取服务器列表
		cfgLock.RLock()
		servers := cfg.Servers
		reconnectInterval := cfg.ReconnectInterval
		cfgLock.RUnlock()

		if len(servers) == 0 {
			log.Println("[!] 未配置目标服务器，等待配置更新...")
			time.Sleep(time.Duration(reconnectInterval) * time.Second)
			continue
		}

		// 尝试连接列表中的服务器
		var conn net.Conn
		var err error
		
		for _, addr := range servers {
			conn, err = net.DialTimeout("tcp", addr, 5*time.Second)
			if err == nil {
				log.Printf("[✓] 连接成功: %s", addr)
				break
			}
			log.Printf("[!] 连接失败 %s: %v", addr, err)
		}

		if conn == nil {
			time.Sleep(time.Duration(reconnectInterval) * time.Second)
			continue
		}

		// 保存连接引用以便热更新时关闭
		connLock.Lock()
		currentConn = conn
		connLock.Unlock()

		// 数据发送循环
		for data := range seqChan {
			totalLen := uint32(6 + len(data.Packet))
			head := make([]byte, 10)
			
			// 获取当前 NodeID (允许热更新)
			cfgLock.RLock()
			nodeID := cfg.NodeID
			cfgLock.RUnlock()

			binary.BigEndian.PutUint32(head[0:4], totalLen)
			binary.BigEndian.PutUint16(head[4:6], nodeID)
			binary.BigEndian.PutUint32(head[6:10], data.Seq)
			
			// 设置写超时，防止网络断开时阻塞过久
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := conn.Write(append(head, data.Packet...)); err != nil {
				log.Printf("[!] 发送错误: %v", err)
				break
			}
		}

		connLock.Lock()
		if currentConn == conn {
			currentConn = nil
		}
		connLock.Unlock()
		conn.Close()
		log.Println("[-] 连接断开，准备重连...")
	}
}
