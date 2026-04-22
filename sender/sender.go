package main

import (
	"encoding/binary"
	"flag"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/ipv4"
	"traptunnel/internal/config"
	"traptunnel/internal/frame"
	"traptunnel/internal/logging"
)

type Config = config.SenderLegacyConfig

var (
	cfg         Config
	cfgLock     sync.RWMutex
	seqChan     chan PacketData
	counter     uint32
	currentConn net.Conn
	connLock    sync.Mutex
)

type PacketData struct {
	Seq    uint32
	Packet []byte
}

func loadConfig(path string, firstRun bool) {
	var previous *Config
	if !firstRun {
		cfgLock.RLock()
		snapshot := cfg
		cfgLock.RUnlock()
		previous = &snapshot
	}

	newCfg, err := config.LoadSenderLegacy(path, previous, firstRun)
	if err != nil {
		slog.Error("无法读取配置文件", "error", err)
		return
	}

	if firstRun || newCfg.MaxLogSize != cfg.MaxLogSize || newCfg.MaxLogBackups != cfg.MaxLogBackups || newCfg.LogLevel != cfg.LogLevel {
		logging.Setup(logging.Options{
			Component:  "sender",
			MaxSize:    newCfg.MaxLogSize,
			MaxBackups: newCfg.MaxLogBackups,
			Level:      newCfg.LogLevel,
		})
	}

	cfgLock.Lock()
	cfg = newCfg
	cfgLock.Unlock()

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
			// 获取当前 NodeID (允许热更新)
			cfgLock.RLock()
			nodeID := cfg.NodeID
			cfgLock.RUnlock()

			// 设置写超时，防止网络断开时阻塞过久
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			wireFrame := frame.Frame{
				NodeID:   nodeID,
				Sequence: data.Seq,
				Payload:  data.Packet,
			}
			if err := wireFrame.WriteTo(conn); err != nil {
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
			slog.Debug("Trap 已发送到 Receiver", "component", "sender", "event", "TrapSent", "node_id", nodeID, "seq", data.Seq, "src_ip", trapSrcIP, "dst_ip", trapDstIP, "src_port", trapSrcPort, "dst_port", trapDstPort, "size", wireFrame.TotalLength())
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
