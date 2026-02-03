package main

import (
	"bufio"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

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
	LogLevel      string
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
			fmt.Printf("[!] 无法创建日志目录 %s: %v. 将仅输出到控制台。\n", dir, err)
			opts := &slog.HandlerOptions{Level: parseLogLevel(newCfg.LogLevel)}
			logger := slog.New(slog.NewTextHandler(os.Stdout, opts))
			slog.SetDefault(logger)
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
	
	// 配置 slog
	var level slog.Level
	switch strings.ToUpper(newCfg.LogLevel) {
	case "DEBUG":
		level = slog.LevelDebug
	case "WARN":
		level = slog.LevelWarn
	case "ERROR":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{Key: "time", Value: slog.StringValue(a.Value.Time().Format(time.RFC3339))}
			}
			return a
		},
	}
	logger := slog.New(slog.NewTextHandler(logOutput, opts))
	slog.SetDefault(logger)
}

func parseLogLevel(s string) slog.Level {
	switch strings.ToUpper(s) {
	case "DEBUG":
		return slog.LevelDebug
	case "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// 简易 INI 解析
func loadConfig(path string, firstRun bool) {
	file, err := os.Open(path)
	if err != nil {
		slog.Error("配置文件读取失败", "error", err)
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
		LogLevel:      "INFO",
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
	
	slog.Info("配置已加载/更新", "component", "receiver", "event", "ConfigReload")
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
		slog.Error("注入失败", "component", "receiver", "event", "InjectFailed", "error", err)
	} else {
		slog.Debug("数据包注入成功", "component", "receiver", "event", "PacketInjected", "len", len(raw))
	}
}

func handleNode(conn net.Conn, rawConn *ipv4.RawConn) {
	defer conn.Close()
	buffer := make([]byte, 0)
	tmp := make([]byte, 8192)
	var nodeID uint16
	firstPacket := true
	
	connID := conn.RemoteAddr().String()
	slog.Info("客户端已连接", "component", "receiver", "event", "ClientConnected", "client_ip", connID)

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
				slog.Info("节点身份识别成功", "component", "receiver", "event", "NodeIdentified", "node_id", nodeID, "node_name", getNodeName(nodeID), "conn_id", connID)
				firstPacket = false
			}

			// 丢包监测
			trackerLock.Lock()
			if lastSeq, ok := nodeTracker[nodeID]; ok {
				if seq != (lastSeq + 1) {
					slog.Warn("检测到丢包", "component", "receiver", "event", "PacketLoss", "node_id", nodeID, "expected", lastSeq+1, "received", seq)
				}
			}
			nodeTracker[nodeID] = seq
			trackerLock.Unlock()

			slog.Debug("收到数据包", "component", "receiver", "event", "MsgReceived", "node_id", nodeID, "seq", seq, "size", totalLen)
			patchAndInject(packet, rawConn)
			buffer = buffer[4+totalLen:]
		}
	}
	slog.Info("节点断开连接", "component", "receiver", "event", "ClientClosed", "node_id", nodeID, "node_name", getNodeName(nodeID), "conn_id", connID)
}

func setupSignalHandler(configPath string) {
	c := make(chan os.Signal, 1)
	// 监听 SIGHUP 信号 (Linux下热重载标准信号)
	signal.Notify(c, syscall.SIGHUP)

	go func() {
		for range c {
			slog.Info("收到 SIGHUP, 正在重载配置...", "component", "receiver", "event", "ConfigReload")
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
		slog.Error("Raw Socket 创建失败 (需 root 权限)", "error", err)
		os.Exit(1)
	}
	rawConn, _ := ipv4.NewRawConn(packetConn)

	// 监听隧道
	// 注意: ListenPort 更改通常需要重启，此处只读初始值
	cfgLock.RLock()
	port := cfg.ListenPort
	cfgLock.RUnlock()
	
	l, err := net.Listen("tcp", ":"+port)
	if err != nil {
		slog.Error("TCP 监听失败", "error", err)
		os.Exit(1)
	}
	slog.Info("OriginTrap Receiver 启动", "component", "receiver", "event", "Startup", "port", port, "log_level", cfg.LogLevel)

	for {
		conn, err := l.Accept()
		if err != nil {
			continue
		}
		go handleNode(conn, rawConn)
	}
}
