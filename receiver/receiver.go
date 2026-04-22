package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"golang.org/x/net/ipv4"
	"traptunnel/internal/config"
	"traptunnel/internal/frame"
	"traptunnel/internal/logging"
)

type Config = config.ReceiverLegacyConfig

var (
	cfg         Config
	cfgLock     sync.RWMutex
	nodeTracker = make(map[uint16]uint32)
	trackerLock sync.Mutex
)

func loadConfig(path string, firstRun bool) {
	var previous *Config
	if !firstRun {
		cfgLock.RLock()
		snapshot := cfg
		cfgLock.RUnlock()
		previous = &snapshot
	}

	newCfg, err := config.LoadReceiverLegacy(path, previous)
	if err != nil {
		slog.Error("配置文件读取失败", "error", err)
		return
	}

	if firstRun || newCfg.MaxLogSize != cfg.MaxLogSize || newCfg.MaxLogBackups != cfg.MaxLogBackups || newCfg.LogLevel != cfg.LogLevel {
		logging.Setup(logging.Options{
			Component:  "receiver",
			MaxSize:    newCfg.MaxLogSize,
			MaxBackups: newCfg.MaxLogBackups,
			Level:      newCfg.LogLevel,
		})
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
		slog.Debug("数据包注入成功", "component", "receiver", "event", "PacketInjected", "dst_ip", newDstIP.String(), "len", len(raw))
	}
}

func handleNode(conn net.Conn, rawConn *ipv4.RawConn) {
	defer conn.Close()
	tmp := make([]byte, 8192)
	decoder := frame.NewDecoder()
	var nodeID uint16
	firstPacket := true

	connID := conn.RemoteAddr().String()
	slog.Info("客户端已连接", "component", "receiver", "event", "ClientConnected", "client_ip", connID)

	for {
		n, err := conn.Read(tmp)
		if err != nil {
			break
		}
		frames, err := decoder.Push(tmp[:n])
		if err != nil {
			slog.Error("隧道帧解析失败", "component", "receiver", "event", "DecodeError", "error", err, "conn_id", connID)
			return
		}

		for _, incoming := range frames {
			nodeID = incoming.NodeID
			seq := incoming.Sequence
			packet := incoming.Payload

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

			var trapSrcIP, trapDstIP string
			var trapSrcPort, trapDstPort uint16
			if ipHdr, err := ipv4.ParseHeader(packet); err == nil {
				if ipHdr.Protocol == 17 && ipHdr.Len > 0 && len(packet) >= ipHdr.Len+4 {
					trapSrcIP = ipHdr.Src.String()
					trapDstIP = ipHdr.Dst.String()
					trapSrcPort = framePort(packet[ipHdr.Len : ipHdr.Len+2])
					trapDstPort = framePort(packet[ipHdr.Len+2 : ipHdr.Len+4])
				}
			}
			slog.Debug("收到 Trap (来自 Sender)", "component", "receiver", "event", "TrapReceived", "sender", connID, "node_id", nodeID, "node_name", getNodeName(nodeID), "seq", seq, "src_ip", trapSrcIP, "dst_ip", trapDstIP, "src_port", trapSrcPort, "dst_port", trapDstPort, "size", len(packet)+frame.MetaSize)
			patchAndInject(packet, rawConn)
		}
	}
	slog.Info("节点断开连接", "component", "receiver", "event", "ClientClosed", "node_id", nodeID, "node_name", getNodeName(nodeID), "conn_id", connID)
}

func framePort(portBytes []byte) uint16 {
	return uint16(portBytes[0])<<8 | uint16(portBytes[1])
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
	packetConn, err := net.ListenPacket("ip4:udp", "0.0.0.0")
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
