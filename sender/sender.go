package main

import (
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
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/ini.v1"
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
	LogLevel      string
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
	
	// 确保存储日志的目录存在 (仅针对 Linux /var/log/traptunnel)
	if runtime.GOOS != "windows" {
		dir := filepath.Dir(logPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			// 如果无法创建目录 (例如无权限)，回退到 stdout 并打印错误
			// 使用 fmt 而不是 log，因为 log 尚未配置
			fmt.Printf("[!] 无法创建日志目录 %s: %v. 将仅输出到控制台。\n", dir, err)
			// 配置一个仅输出到 stdout 的 logger
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

	// Write to both stdout (for systemd) and file (for persistence/rotation)
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
				// 自定义时间格式
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

// 使用 ini.v1 库解析
func loadConfig(path string, firstRun bool) {
	cfgFile, err := ini.Load(path)
	if err != nil {
		slog.Error("无法读取配置文件", "error", err)
		return
	}

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
		newCfg.LogLevel = "INFO"
	}

	// Common Section
	sectionCommon := cfgFile.Section("common")
	if k, err := sectionCommon.GetKey("node_id"); err == nil {
		if v, err := k.Int(); err == nil {
			newCfg.NodeID = uint16(v)
		}
	}

	if k, err := sectionCommon.GetKey("servers"); err == nil {
		servers := strings.Split(k.String(), ",")
		for _, s := range servers {
			s = strings.TrimSpace(s)
			if s != "" {
				newCfg.Servers = append(newCfg.Servers, s)
			}
		}
	}

	// 临时变量兼容旧配置
	bServerIP := sectionCommon.Key("b_server_ip").String()
	bServerPort := sectionCommon.Key("b_server_port").String()

	// Advanced Section
	sectionAdvanced := cfgFile.Section("advanced")
	newCfg.ListenPort = sectionAdvanced.Key("listen_port").MustInt(newCfg.ListenPort)
	newCfg.ReconnectInterval = sectionAdvanced.Key("reconnect_interval").MustInt(newCfg.ReconnectInterval)
	if firstRun {
		newCfg.MaxBufferSize = sectionAdvanced.Key("max_buffer_size").MustInt(newCfg.MaxBufferSize)
	}

	// Logging Section
	sectionLogging := cfgFile.Section("logging")
	newCfg.MaxLogSize = sectionLogging.Key("max_log_size").MustInt(newCfg.MaxLogSize)
	newCfg.MaxLogBackups = sectionLogging.Key("max_log_backups").MustInt(newCfg.MaxLogBackups)
	newCfg.LogLevel = sectionLogging.Key("log_level").MustString(newCfg.LogLevel)

	// 兼容旧配置格式
	if len(newCfg.Servers) == 0 && bServerIP != "" && bServerPort != "" {
		newCfg.Servers = append(newCfg.Servers, net.JoinHostPort(bServerIP, bServerPort))
	}

	// 检查是否需要更新 Logger
	// 仅在首次运行或日志配置变更时更新
	if firstRun || newCfg.MaxLogSize != cfg.MaxLogSize || newCfg.MaxLogBackups != cfg.MaxLogBackups || newCfg.LogLevel != cfg.LogLevel {
		setupLogger(newCfg)
	}

	// 更新全局配置
	cfgLock.Lock()
	cfg = newCfg
	// lastModTime 已经不再使用了，因为我们改用信号触发
	cfgLock.Unlock()

	// 如果不是首次运行，且发生了配置变更，强制断开连接以触发重连
	if !firstRun {
		slog.Info("配置已热重载", "component", "sender", "event", "ConfigReload")
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
		slog.Info("收到 SIGHUP 信号，正在重载配置...", "component", "sender", "event", "ConfigReload")
		loadConfig(path, false)
	}
}

func main() {
	configPath := flag.String("c", "sender.conf", "Path to config file")
	flag.Parse()

	loadConfig(*configPath, true)
	cfgLock.RLock()
	slog.Info("OriginTrap Sender 启动", 
		"component", "sender", 
		"event", "Startup", 
		"node_id", cfg.NodeID, 
		"targets", cfg.Servers,
		"log_level", cfg.LogLevel,
	)
	cfgLock.RUnlock()

	go signalWatcher(*configPath)
	go packetProducer()
	tunnelConsumer()
}

func packetProducer() {
	c, err := net.ListenPacket("ip4:udp", "0.0.0.0")
	if err != nil {
		slog.Error("Socket 错误 (需root)", "component", "sender", "event", "StartupFailed", "error", err)
		os.Exit(1)
	}
	r, err := ipv4.NewRawConn(c)
	if err != nil {
		slog.Error("RawConn error", "error", err)
		os.Exit(1)
	}

	for {
		buf := make([]byte, 2048)
		h, payload, _, err := r.ReadFrom(buf)
		if err != nil {
			slog.Error("读取 Raw Socket 失败", "component", "sender", "event", "ReadError", "error", err)
			time.Sleep(100 * time.Millisecond) // 防止错误导致 CPU 飙升
			continue
		}

		if len(payload) >= 4 {
			srcPort := binary.BigEndian.Uint16(payload[0:2])
			dstPort := binary.BigEndian.Uint16(payload[2:4])
			// 获取当前监听端口
			cfgLock.RLock()
			listenPort := cfg.ListenPort
			cfgLock.RUnlock()

			if dstPort == uint16(listenPort) {
				// 记录获取到的 Trap 数据 (DEBUG 级别)
				slog.Debug("捕获到 Trap 包", 
					"component", "sender", 
					"event", "PacketCaptured", 
					"src_ip", h.Src.String(), 
					"dst_ip", h.Dst.String(),
					"src_port", srcPort,
					"dst_port", dstPort,
					"length", len(payload),
					"seq", counter+1,
				)

				fullPacket, _ := h.Marshal()
				fullPacket = append(fullPacket, payload...)
				counter++
				select {
				case seqChan <- PacketData{Seq: counter, Packet: fullPacket}:
				default:
					slog.Warn("缓冲区已满，丢弃数据包", "component", "sender", "event", "BufferOverflow", "seq", counter)
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
			slog.Warn("未配置目标服务器，等待配置更新...", "component", "sender", "event", "NoTarget")
			time.Sleep(time.Duration(reconnectInterval) * time.Second)
			continue
		}

		// 尝试连接列表中的服务器
		var conn net.Conn
		var err error
		
		for _, addr := range servers {
			slog.Info("尝试连接服务器", "component", "sender", "event", "ConnAttempt", "target", addr)
			conn, err = net.DialTimeout("tcp", addr, 5*time.Second)
			if err == nil {
				slog.Info("连接成功", "component", "sender", "event", "ConnEstablished", "target", addr)
				break
			}
			slog.Error("连接失败", "component", "sender", "event", "ConnFailed", "target", addr, "error", err)
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
				slog.Error("发送错误", "component", "sender", "event", "SendError", "error", err, "seq", data.Seq)
				break
			}
			var trapSrcIP, trapDstIP string
			var trapSrcPort, trapDstPort uint16
			if ipHdr, err := ipv4.ParseHeader(data.Packet); err == nil {
				if ipHdr.Protocol == 17 && ipHdr.Len > 0 && len(data.Packet) >= ipHdr.Len+4 {
					trapSrcIP = ipHdr.Src.String()
					trapDstIP = ipHdr.Dst.String()
					trapSrcPort = binary.BigEndian.Uint16(data.Packet[ipHdr.Len : ipHdr.Len+2])
					trapDstPort = binary.BigEndian.Uint16(data.Packet[ipHdr.Len+2 : ipHdr.Len+4])
				}
			}
			slog.Debug("Trap 已发送到 Receiver", "component", "sender", "event", "TrapSent", "node_id", nodeID, "seq", data.Seq, "src_ip", trapSrcIP, "dst_ip", trapDstIP, "src_port", trapSrcPort, "dst_port", trapDstPort, "size", totalLen)
		}

		connLock.Lock()
		if currentConn == conn {
			currentConn = nil
		}
		connLock.Unlock()
		conn.Close()
		slog.Warn("连接断开，准备重连...", "component", "sender", "event", "ConnLost")
	}
}
