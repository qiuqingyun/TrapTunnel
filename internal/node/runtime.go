package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
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

	sourceFrames := make(chan frame.Frame, cfg.Tuning.PipelineBufferSize)
	egressFrames := make(chan frame.Frame, cfg.Tuning.PipelineBufferSize)
	errCh := make(chan error, 3)

	exportHub, err := optionalExportHub(ctx, cfg)
	if err != nil {
		return err
	}

	go func() {
		errCh <- captureLoop(ctx, cfg, sourceFrames)
	}()
	go func() {
		errCh <- dispatchLoop(ctx, cfg, sourceFrames, egressFrames, exportHub)
	}()
	go func() {
		errCh <- fanoutLoop(ctx, cfg, egressFrames)
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

	sourceFrames := make(chan frame.Frame, cfg.Tuning.PipelineBufferSize)
	egressFrames := make(chan frame.Frame, cfg.Tuning.PipelineBufferSize)
	errCh := make(chan error, 4)

	exportHub, err := optionalExportHub(ctx, cfg)
	if err != nil {
		return err
	}

	go func() {
		errCh <- captureLoop(ctx, cfg, sourceFrames)
	}()
	go func() {
		errCh <- ingressLoop(ctx, cfg, func(incoming frame.Frame, connID string) error {
			select {
			case sourceFrames <- incoming:
				slog.Debug("Relay 转发收到的帧", "component", "node", "profile", cfg.Profile, "event", "RelayEnqueue", "conn_id", connID, "node_id", incoming.NodeID, "seq", incoming.Sequence)
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	go func() {
		errCh <- dispatchLoop(ctx, cfg, sourceFrames, egressFrames, exportHub)
	}()
	go func() {
		errCh <- fanoutLoop(ctx, cfg, egressFrames)
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

	go func() {
		<-ctx.Done()
		_ = packetConn.Close()
	}()

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

		buf := make([]byte, cfg.Tuning.CaptureReadBufferSize)
		header, payload, _, err := rawConn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.Error("读取 Raw Socket 失败", "component", "node", "profile", cfg.Profile, "event", "ReadError", "error", err)
			time.Sleep(time.Duration(cfg.Tuning.CaptureReadBackoffMS) * time.Millisecond)
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

		if len(fullPacket)+frame.MetaSize > cfg.Tuning.MaxFrameTotalLength {
			slog.Error("捕获到的帧超过上限，已丢弃", "component", "node", "profile", cfg.Profile, "event", "FrameTooLarge", "size", len(fullPacket)+frame.MetaSize, "limit", cfg.Tuning.MaxFrameTotalLength)
			continue
		}

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
		groupInput := make(chan frame.Frame, cfg.Tuning.EgressGroupBufferSize)
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
	baseReconnectInterval := time.Duration(cfg.Egress.ReconnectInterval) * time.Second
	reconnectInterval := baseReconnectInterval

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

		conn, target := dialTargets(ctx, cfg, members)
		if conn == nil {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(nextReconnectDelay(cfg, reconnectInterval)):
				continue
			}
		}

		slog.Info("egress group 已连接", "component", "node", "profile", cfg.Profile, "event", "ConnEstablished", "group", groupIndex, "target", target)
		reconnectInterval = baseReconnectInterval

		for pending != nil {
			if err := writeFrame(conn, *pending, cfg); err != nil {
				slog.Error("egress group 发送错误", "component", "node", "profile", cfg.Profile, "event", "SendError", "group", groupIndex, "target", target, "error", err, "seq", pending.Sequence)
				_ = conn.Close()
				time.Sleep(nextReconnectDelay(cfg, reconnectInterval))
				reconnectInterval = nextBackoff(cfg, reconnectInterval, baseReconnectInterval)
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
				if err := writeFrame(conn, next, cfg); err != nil {
					slog.Error("egress group 发送错误", "component", "node", "profile", cfg.Profile, "event", "SendError", "group", groupIndex, "target", target, "error", err, "seq", next.Sequence)
					pending = &next
					_ = conn.Close()
					time.Sleep(nextReconnectDelay(cfg, reconnectInterval))
					reconnectInterval = nextBackoff(cfg, reconnectInterval, baseReconnectInterval)
					goto reconnect
				}
				slog.Debug("Trap 已发送", "component", "node", "profile", cfg.Profile, "event", "TrapSent", "group", groupIndex, "target", target, "seq", next.Sequence, "size", next.TotalLength())
			}
		}

	reconnect:
	}
}

func writeFrame(conn net.Conn, outgoing frame.Frame, cfg config.NodeConfig) error {
	_ = conn.SetWriteDeadline(time.Now().Add(time.Duration(cfg.Tuning.EgressWriteTimeoutMS) * time.Millisecond))
	return outgoing.WriteToWithLimit(conn, uint32(cfg.Tuning.MaxFrameTotalLength))
}

func dialTargets(ctx context.Context, cfg config.NodeConfig, targets []string) (net.Conn, string) {
	for _, target := range targets {
		select {
		case <-ctx.Done():
			return nil, ""
		default:
		}

		slog.Info("尝试连接服务器", "component", "node", "event", "ConnAttempt", "target", target)
		conn, err := net.DialTimeout("tcp", target, time.Duration(cfg.Tuning.EgressDialTimeoutMS)*time.Millisecond)
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

	exportHub, err := optionalExportHub(ctx, cfg)
	if err != nil {
		return err
	}

	slog.Info("Node 启动", "component", "node", "profile", cfg.Profile, "event", "Startup", "listen", cfg.Ingress.Listen, "inject_ip", cfg.Inject.IP)

	return ingressLoop(ctx, cfg, func(incoming frame.Frame, connID string) error {
		slog.Debug("收到 Trap", "component", "node", "profile", cfg.Profile, "event", "TrapReceived", "conn_id", connID, "node_id", incoming.NodeID, "seq", incoming.Sequence, "size", incoming.TotalLength())
		if exportHub != nil {
			exportHub.Publish(incoming)
		}
		rewrite, err := patchAndInject(incoming.Payload, cfg, rawConn)
		if err != nil {
			slog.Error("注入失败", "component", "node", "profile", cfg.Profile, "event", "InjectFailed", "conn_id", connID, "node_id", incoming.NodeID, "seq", incoming.Sequence, "error", err)
			return nil
		}
		if rewrite.SourceOverridden {
			slog.Debug("按 SNMPv1 agent-addr 修正源 IP", "component", "node", "profile", cfg.Profile, "event", "InjectSourceOverride", "conn_id", connID, "node_id", incoming.NodeID, "seq", incoming.Sequence, "original_src_ip", rewrite.OriginalSrcIP, "effective_src_ip", rewrite.EffectiveSrcIP)
		}
		return nil
	})
}

func optionalExportHub(ctx context.Context, cfg config.NodeConfig) (*exportHub, error) {
	if !cfg.Export.Enabled {
		return nil, nil
	}
	return startExportHub(ctx, cfg)
}

func dispatchLoop(ctx context.Context, cfg config.NodeConfig, source <-chan frame.Frame, egress chan<- frame.Frame, exportHub *exportHub) error {
	defer close(egress)

	for {
		select {
		case <-ctx.Done():
			return nil
		case incoming, ok := <-source:
			if !ok {
				return nil
			}

			if exportHub != nil {
				exportHub.Publish(incoming)
			}

			select {
			case egress <- incoming:
			case <-ctx.Done():
				return nil
			}
		}
	}
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

	go func() {
		<-ctx.Done()
		closeConnNow(conn)
	}()

	tmp := make([]byte, cfg.Tuning.IngressReadBufferSize)
	decoder := frame.NewDecoder()
	decoder.MaxTotalLength = uint32(cfg.Tuning.MaxFrameTotalLength)
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

type injectRewrite struct {
	OriginalSrcIP    string
	EffectiveSrcIP   string
	SourceOverridden bool
}

func patchAndInject(raw []byte, cfg config.NodeConfig, rawConn *ipv4.RawConn) (injectRewrite, error) {
	header, payload, rewrite, err := prepareInjectedPacket(raw, cfg)
	if err != nil {
		return injectRewrite{}, err
	}
	if err := rawConn.WriteTo(header, payload, nil); err != nil {
		return injectRewrite{}, err
	}
	return rewrite, nil
}

func prepareInjectedPacket(raw []byte, cfg config.NodeConfig) (*ipv4.Header, []byte, injectRewrite, error) {
	packet := gopacket.NewPacket(raw, layers.LayerTypeIPv4, gopacket.Default)

	ipLayer := packet.Layer(layers.LayerTypeIPv4)
	if ipLayer == nil {
		return nil, nil, injectRewrite{}, errors.New("missing ipv4 layer")
	}
	ip, _ := ipLayer.(*layers.IPv4)

	udpLayer := packet.Layer(layers.LayerTypeUDP)
	if udpLayer == nil {
		return nil, nil, injectRewrite{}, errors.New("missing udp layer")
	}
	udp, _ := udpLayer.(*layers.UDP)

	newDstIP := net.ParseIP(cfg.Inject.IP)
	if newDstIP == nil {
		return nil, nil, injectRewrite{}, fmt.Errorf("invalid inject ip: %s", cfg.Inject.IP)
	}

	rewrite := injectRewrite{
		OriginalSrcIP:  ip.SrcIP.String(),
		EffectiveSrcIP: ip.SrcIP.String(),
	}

	if cfg.Inject.SNMPv1AgentAddrOverride {
		agentAddr, found, err := extractSNMPv1AgentAddr(udp.Payload)
		if err != nil {
			return nil, nil, injectRewrite{}, fmt.Errorf("extract snmpv1 agent-addr: %w", err)
		}
		if found && agentAddr.To4() != nil && !agentAddr.Equal(ip.SrcIP.To4()) {
			ip.SrcIP = agentAddr.To4()
			rewrite.EffectiveSrcIP = ip.SrcIP.String()
			rewrite.SourceOverridden = true
		}
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
		return nil, nil, injectRewrite{}, err
	}

	header, err := ipv4.ParseHeader(raw)
	if err != nil {
		return nil, nil, injectRewrite{}, err
	}
	header.Src = ip.SrcIP
	header.Dst = newDstIP
	header.Checksum = 0
	return header, buffer.Bytes(), rewrite, nil
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

func closeConnNow(conn net.Conn) {
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.SetLinger(0)
	}
	_ = conn.Close()
}

func nextReconnectDelay(cfg config.NodeConfig, base time.Duration) time.Duration {
	if base <= 0 {
		base = 5 * time.Second
	}

	maxDelay := time.Duration(cfg.Tuning.EgressBackoffMaxMS) * time.Millisecond
	if maxDelay > 0 && base > maxDelay {
		base = maxDelay
	}

	jitterPct := cfg.Tuning.EgressBackoffJitterPct
	if jitterPct <= 0 {
		return base
	}

	jitterWindow := int64(base) * int64(jitterPct) / 100
	if jitterWindow <= 0 {
		return base
	}

	extra := time.Duration(rand.Int63n(jitterWindow + 1))
	return base + extra
}

func nextBackoff(cfg config.NodeConfig, current time.Duration, initial time.Duration) time.Duration {
	if current <= 0 {
		current = initial
	}
	if current <= 0 {
		current = 5 * time.Second
	}

	next := current * 2
	maxDelay := time.Duration(cfg.Tuning.EgressBackoffMaxMS) * time.Millisecond
	if maxDelay > 0 && next > maxDelay {
		next = maxDelay
	}
	return next
}
