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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/ini.v1"
	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
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

// 使用 ini.v1 库解析
func loadConfig(path string, firstRun bool) {
	cfgFile, err := ini.Load(path)
	if err != nil {
		slog.Error("配置文件读取失败", "error", err)
		return
	}

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

	// Server Section
	sectionServer := cfgFile.Section("server")
	newCfg.ListenPort = sectionServer.Key("listen_port").MustString(newCfg.ListenPort)
	newCfg.InjectIP = sectionServer.Key("inject_ip").MustString(newCfg.InjectIP)

	// Logging Section
	sectionLogging := cfgFile.Section("logging")
	newCfg.MaxLogSize = sectionLogging.Key("max_log_size").MustInt(newCfg.MaxLogSize)
	newCfg.MaxLogBackups = sectionLogging.Key("max_log_backups").MustInt(newCfg.MaxLogBackups)
	newCfg.LogLevel = sectionLogging.Key("log_level").MustString(newCfg.LogLevel)

	// Nodes Section
	sectionNodes := cfgFile.Section("nodes")
	for _, key := range sectionNodes.Keys() {
		if id, err := strconv.ParseUint(key.Name(), 10, 16); err == nil {
			newCfg.NodeMapping[uint16(id)] = key.String()
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

func patchAndInject(raw []byte, rawConn *ipv4.RawConn) {
	// 使用 gopacket 解析数据包
	packet := gopacket.NewPacket(raw, layers.LayerTypeIPv4, gopacket.Default)
	
	// 获取 IP 层
	ipLayer := packet.Layer(layers.LayerTypeIPv4)
	if ipLayer == nil {
		return
	}
	ip, _ := ipLayer.(*layers.IPv4)

	// 获取 UDP 层
	udpLayer := packet.Layer(layers.LayerTypeUDP)
	if udpLayer == nil {
		return
	}
	udp, _ := udpLayer.(*layers.UDP)

	// 修改目标 IP
	newDstIP := net.ParseIP(getInjectIP())
	ip.DstIP = newDstIP

	// 重新计算 UDP 校验和
	// gopacket 会自动处理伪首部
	udp.SetNetworkLayerForChecksum(ip)

	// 序列化修改后的 UDP 数据包
	buffer := gopacket.NewSerializeBuffer()
	options := gopacket.SerializeOptions{
		ComputeChecksums: true,
		FixLengths:       true,
	}
	
	if err := gopacket.SerializeLayers(buffer, options, udp, gopacket.Payload(udp.Payload)); err != nil {
		slog.Error("序列化失败", "error", err)
		return
	}

	// 使用 ipv4.RawConn 发送
	// 注意: RawConn 会自动处理 IP 头 (包括 Checksum), 我们只需要传入 Header 和 Payload (UDP + Data)
	// 但 gopacket 的 ipv4 layer 已经修改了 DstIP，我们需要确保传给 RawConn 的 header 也是新的
	
	// 由于 gopacket 和 x/net/ipv4 的结构体不兼容，我们需要手动转换或者直接用 RawConn 发送 gopacket 序列化的 UDP 部分
	// 但 RawConn 需要 ipv4.Header 结构体。
	// 这里最简单的方式是: 使用 x/net/ipv4 解析 Header (已有的逻辑)，但使用 gopacket 计算 UDP Checksum
	
	// 为了利用 gopacket 的校验和计算能力，我们已经有了 udp 层的正确 Checksum (在 SerializeLayers 后)
	// 现在我们可以把新的 UDP 包取出来
	newUDPBytes := buffer.Bytes()
	
	// 构造 ipv4.Header
	header, err := ipv4.ParseHeader(raw)
	if err != nil {
		return
	}
	header.Dst = newDstIP
	header.Checksum = 0 // RawConn 计算

	if err := rawConn.WriteTo(header, newUDPBytes, nil); err != nil {
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
			
			// 安全检查: 限制最大包大小 (例如 10MB)，防止 DoS 攻击
			if totalLen > 10*1024*1024 {
				slog.Error("数据包过大，断开连接", "component", "receiver", "event", "SecurityAlert", "size", totalLen, "conn_id", connID)
				return
			}

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
