package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"golang.org/x/net/ipv4"

	"traptunnel/internal/config"
	"traptunnel/internal/frame"
)

const groupBufferSize = 1024

// Run starts the node runtime for the supported profiles.
func Run(ctx context.Context, cfg config.NodeConfig) error {
	switch cfg.Profile {
	case config.ProfileEdge:
		return runEdge(ctx, cfg)
	case config.ProfileRelay:
		return runRelay(ctx, cfg)
	case config.ProfileSink:
		return runSink(ctx, cfg)
	default:
		return fmt.Errorf("profile %q is not supported in current node runtime", cfg.Profile)
	}
}

func runEdge(ctx context.Context, cfg config.NodeConfig) error {
	if !cfg.Capture.Enabled {
		return errors.New("edge profile requires capture.enabled=true")
	}
	if !cfg.Egress.Enabled {
		return errors.New("edge profile requires egress.enabled=true")
	}
	if len(cfg.Capture.ListenPorts) == 0 {
		return errors.New("edge profile requires at least one capture.listen_ports entry")
	}
	if len(cfg.Egress.Groups) == 0 {
		return errors.New("edge profile requires at least one egress group")
	}

	frames := make(chan frame.Frame, 1024)
	errCh := make(chan error, 2)

	go func() {
		errCh <- captureLoop(ctx, cfg, frames)
	}()
	go func() {
		errCh <- fanoutLoop(ctx, cfg, frames)
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

func runRelay(ctx context.Context, cfg config.NodeConfig) error {
	if !cfg.Capture.Enabled {
		return errors.New("relay profile requires capture.enabled=true")
	}
	if !cfg.Ingress.Enabled {
		return errors.New("relay profile requires ingress.enabled=true")
	}
	if !cfg.Egress.Enabled {
		return errors.New("relay profile requires egress.enabled=true")
	}
	if len(cfg.Capture.ListenPorts) == 0 {
		return errors.New("relay profile requires at least one capture.listen_ports entry")
	}
	if cfg.Ingress.Listen == "" {
		return errors.New("relay profile requires ingress.listen")
	}
	if len(cfg.Egress.Groups) == 0 {
		return errors.New("relay profile requires at least one egress group")
	}

	frames := make(chan frame.Frame, 1024)
	errCh := make(chan error, 3)

	go func() {
		errCh <- captureLoop(ctx, cfg, frames)
	}()
	go func() {
		errCh <- ingressLoop(ctx, cfg, func(incoming frame.Frame, connID string) error {
			select {
			case frames <- incoming:
				slog.Debug("Relay 转发收到的帧", "component", "node", "profile", cfg.Profile, "event", "RelayEnqueue", "conn_id", connID, "node_id", incoming.NodeID, "seq", incoming.Sequence)
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	go func() {
		errCh <- fanoutLoop(ctx, cfg, frames)
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

func captureLoop(ctx context.Context, cfg config.NodeConfig, out chan<- frame.Frame) error {
	packetConn, err := net.ListenPacket("ip4:udp", "0.0.0.0")
	if err != nil {
		return fmt.Errorf("capture raw socket failed: %w", err)
	}
	defer packetConn.Close()

	rawConn, err := ipv4.NewRawConn(packetConn)
	if err != nil {
		return fmt.Errorf("capture raw conn failed: %w", err)
	}

	ports := make(map[uint16]struct{}, len(cfg.Capture.ListenPorts))
	for _, port := range cfg.Capture.ListenPorts {
		ports[uint16(port)] = struct{}{}
	}

	var seq uint32
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		buf := make([]byte, 2048)
		header, payload, _, err := rawConn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.Error("读取 Raw Socket 失败", "component", "node", "profile", cfg.Profile, "event", "ReadError", "error", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if len(payload) < 4 {
			continue
		}

		dstPort := udpPort(payload[2:4])
		if _, ok := ports[dstPort]; !ok {
			continue
		}

		fullPacket, err := header.Marshal()
		if err != nil {
			slog.Error("IP 头序列化失败", "component", "node", "profile", cfg.Profile, "event", "MarshalError", "error", err)
			continue
		}
		fullPacket = append(fullPacket, payload...)
		sequence := atomic.AddUint32(&seq, 1)

		outgoing := frame.Frame{
			NodeID:   cfg.ID,
			Sequence: sequence,
			Payload:  fullPacket,
		}

		select {
		case out <- outgoing:
			slog.Debug("捕获到 Trap 包", "component", "node", "profile", cfg.Profile, "event", "PacketCaptured", "seq", sequence, "src_ip", header.Src.String(), "dst_ip", header.Dst.String(), "dst_port", dstPort)
		case <-ctx.Done():
			return nil
		}
	}
}

func fanoutLoop(ctx context.Context, cfg config.NodeConfig, in <-chan frame.Frame) error {
	if len(cfg.Egress.Groups) == 0 {
		return errors.New("fanout requires at least one egress group")
	}

	groupInputs := make([]chan frame.Frame, len(cfg.Egress.Groups))
	errCh := make(chan error, len(cfg.Egress.Groups))
	var wg sync.WaitGroup

	for idx, group := range cfg.Egress.Groups {
		groupInput := make(chan frame.Frame, groupBufferSize)
		groupInputs[idx] = groupInput
		wg.Add(1)
		go func(groupIndex int, members []string, input <-chan frame.Frame) {
			defer wg.Done()
			errCh <- egressGroupLoop(ctx, cfg, groupIndex, members, input)
		}(idx, append([]string(nil), group.Members...), groupInput)
	}

	go func() {
		<-ctx.Done()
		for _, groupInput := range groupInputs {
			close(groupInput)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil
		case err := <-errCh:
			if err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
		case incoming, ok := <-in:
			if !ok {
				wg.Wait()
				return nil
			}
			for groupIndex, groupInput := range groupInputs {
				outgoing := incoming.Clone()
				select {
				case groupInput <- outgoing:
				case <-ctx.Done():
					wg.Wait()
					return nil
				}
				slog.Debug("帧加入 fanout group", "component", "node", "profile", cfg.Profile, "event", "FanoutDispatch", "group", groupIndex, "node_id", incoming.NodeID, "seq", incoming.Sequence)
			}
		}
	}
}

func egressGroupLoop(ctx context.Context, cfg config.NodeConfig, groupIndex int, members []string, in <-chan frame.Frame) error {
	reconnectInterval := time.Duration(cfg.Egress.ReconnectInterval) * time.Second
	if reconnectInterval <= 0 {
		reconnectInterval = 5 * time.Second
	}

	var pending *frame.Frame
	for {
		if pending == nil {
			select {
			case <-ctx.Done():
				return nil
			case next, ok := <-in:
				if !ok {
					return nil
				}
				pending = &next
			}
		}

		conn, target := dialTargets(ctx, members)
		if conn == nil {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(reconnectInterval):
				continue
			}
		}

		slog.Info("egress group 已连接", "component", "node", "profile", cfg.Profile, "event", "ConnEstablished", "group", groupIndex, "target", target)

		for pending != nil {
			if err := writeFrame(conn, *pending); err != nil {
				slog.Error("egress group 发送错误", "component", "node", "profile", cfg.Profile, "event", "SendError", "group", groupIndex, "target", target, "error", err, "seq", pending.Sequence)
				_ = conn.Close()
				time.Sleep(reconnectInterval)
				goto reconnect
			}
			slog.Debug("Trap 已发送", "component", "node", "profile", cfg.Profile, "event", "TrapSent", "group", groupIndex, "target", target, "seq", pending.Sequence, "size", pending.TotalLength())
			pending = nil
		}

		for {
			select {
			case <-ctx.Done():
				_ = conn.Close()
				return nil
			case next, ok := <-in:
				if !ok {
					_ = conn.Close()
					return nil
				}
				if err := writeFrame(conn, next); err != nil {
					slog.Error("egress group 发送错误", "component", "node", "profile", cfg.Profile, "event", "SendError", "group", groupIndex, "target", target, "error", err, "seq", next.Sequence)
					pending = &next
					_ = conn.Close()
					time.Sleep(reconnectInterval)
					goto reconnect
				}
				slog.Debug("Trap 已发送", "component", "node", "profile", cfg.Profile, "event", "TrapSent", "group", groupIndex, "target", target, "seq", next.Sequence, "size", next.TotalLength())
			}
		}

	reconnect:
	}
}

func writeFrame(conn net.Conn, outgoing frame.Frame) error {
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return outgoing.WriteTo(conn)
}

func dialTargets(ctx context.Context, targets []string) (net.Conn, string) {
	for _, target := range targets {
		select {
		case <-ctx.Done():
			return nil, ""
		default:
		}

		slog.Info("尝试连接服务器", "component", "node", "event", "ConnAttempt", "target", target)
		conn, err := net.DialTimeout("tcp", target, 5*time.Second)
		if err == nil {
			return conn, target
		}
		slog.Error("连接失败", "component", "node", "event", "ConnFailed", "target", target, "error", err)
	}
	return nil, ""
}

func runSink(ctx context.Context, cfg config.NodeConfig) error {
	if !cfg.Ingress.Enabled {
		return errors.New("sink profile requires ingress.enabled=true")
	}
	if !cfg.Inject.Enabled {
		return errors.New("sink profile requires inject.enabled=true")
	}
	if cfg.Ingress.Listen == "" {
		return errors.New("sink profile requires ingress.listen")
	}

	packetConn, err := net.ListenPacket("ip4:udp", "0.0.0.0")
	if err != nil {
		return fmt.Errorf("inject raw socket failed: %w", err)
	}
	defer packetConn.Close()

	rawConn, err := ipv4.NewRawConn(packetConn)
	if err != nil {
		return fmt.Errorf("inject raw conn failed: %w", err)
	}

	slog.Info("Node 启动", "component", "node", "profile", cfg.Profile, "event", "Startup", "listen", cfg.Ingress.Listen, "inject_ip", cfg.Inject.IP)

	return ingressLoop(ctx, cfg, func(incoming frame.Frame, connID string) error {
		slog.Debug("收到 Trap", "component", "node", "profile", cfg.Profile, "event", "TrapReceived", "conn_id", connID, "node_id", incoming.NodeID, "seq", incoming.Sequence, "size", incoming.TotalLength())
		if err := patchAndInject(incoming.Payload, cfg, rawConn); err != nil {
			slog.Error("注入失败", "component", "node", "profile", cfg.Profile, "event", "InjectFailed", "conn_id", connID, "node_id", incoming.NodeID, "seq", incoming.Sequence, "error", err)
			return nil
		}
		return nil
	})
}

func ingressLoop(ctx context.Context, cfg config.NodeConfig, handler func(frame.Frame, string) error) error {
	listener, err := net.Listen("tcp", cfg.Ingress.Listen)
	if err != nil {
		return fmt.Errorf("ingress listen failed: %w", err)
	}
	defer listener.Close()

	if tcpListener, ok := listener.(*net.TCPListener); ok {
		go func() {
			<-ctx.Done()
			_ = tcpListener.Close()
		}()
	}

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			slog.Error("TCP 接收失败", "component", "node", "profile", cfg.Profile, "event", "AcceptError", "error", err)
			continue
		}
		go handleIngressConn(ctx, cfg, conn, handler)
	}
}

func handleIngressConn(ctx context.Context, cfg config.NodeConfig, conn net.Conn, handler func(frame.Frame, string) error) {
	defer conn.Close()

	tmp := make([]byte, 8192)
	decoder := frame.NewDecoder()
	connID := conn.RemoteAddr().String()

	slog.Info("客户端已连接", "component", "node", "profile", cfg.Profile, "event", "ClientConnected", "client_ip", connID)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, err := conn.Read(tmp)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				slog.Error("TCP 读取失败", "component", "node", "profile", cfg.Profile, "event", "ReadError", "error", err, "conn_id", connID)
			}
			break
		}

		frames, err := decoder.Push(tmp[:n])
		if err != nil {
			slog.Error("隧道帧解析失败", "component", "node", "profile", cfg.Profile, "event", "DecodeError", "error", err, "conn_id", connID)
			return
		}

		for _, incoming := range frames {
			if err := handler(incoming, connID); err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				slog.Error("处理隧道帧失败", "component", "node", "profile", cfg.Profile, "event", "HandleFrameError", "error", err, "conn_id", connID, "node_id", incoming.NodeID, "seq", incoming.Sequence)
				return
			}
		}
	}

	slog.Info("客户端断开连接", "component", "node", "profile", cfg.Profile, "event", "ClientClosed", "client_ip", connID)
}

func patchAndInject(raw []byte, cfg config.NodeConfig, rawConn *ipv4.RawConn) error {
	packet := gopacket.NewPacket(raw, layers.LayerTypeIPv4, gopacket.Default)

	ipLayer := packet.Layer(layers.LayerTypeIPv4)
	if ipLayer == nil {
		return errors.New("missing ipv4 layer")
	}
	ip, _ := ipLayer.(*layers.IPv4)

	udpLayer := packet.Layer(layers.LayerTypeUDP)
	if udpLayer == nil {
		return errors.New("missing udp layer")
	}
	udp, _ := udpLayer.(*layers.UDP)

	newDstIP := net.ParseIP(cfg.Inject.IP)
	if newDstIP == nil {
		return fmt.Errorf("invalid inject ip: %s", cfg.Inject.IP)
	}
	ip.DstIP = newDstIP
	udp.DstPort = layers.UDPPort(cfg.Inject.Port)
	udp.SetNetworkLayerForChecksum(ip)

	buffer := gopacket.NewSerializeBuffer()
	options := gopacket.SerializeOptions{
		ComputeChecksums: true,
		FixLengths:       true,
	}
	if err := gopacket.SerializeLayers(buffer, options, udp, gopacket.Payload(udp.Payload)); err != nil {
		return err
	}

	header, err := ipv4.ParseHeader(raw)
	if err != nil {
		return err
	}
	header.Dst = newDstIP
	header.Checksum = 0
	return rawConn.WriteTo(header, buffer.Bytes(), nil)
}

func udpPort(b []byte) uint16 {
	if len(b) < 2 {
		return 0
	}
	return uint16(b[0])<<8 | uint16(b[1])
}

// Main wraps signal-aware execution for cmd/node.
func Main(cfg config.NodeConfig) int {
	ctx, stop := signalContext()
	defer stop()

	if err := Run(ctx, cfg); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("Node 运行失败", "component", "node", "profile", cfg.Profile, "error", err)
		return 1
	}
	return 0
}

func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	// SIGINT and SIGTERM are enough for the current runtime.
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ch
		cancel()
	}()
	return ctx, cancel
}
