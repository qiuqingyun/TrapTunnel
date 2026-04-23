package node

import (
	"context"
	"net"
	"testing"
	"time"

	"traptunnel/internal/config"
	"traptunnel/internal/frame"
)

func TestFanoutLoopFailoverAndFanout(t *testing.T) {
	t.Parallel()

	group1, recv1 := startFrameSink(t)
	group2, recv2 := startFrameSink(t)
	deadTarget := unavailableTCPAddress(t)

	cfg := config.NodeConfig{
		Profile: config.ProfileRelay,
		Egress: config.EgressConfig{
			Enabled:           true,
			ReconnectInterval: 1,
			Groups: []config.EgressGroup{
				{Members: []string{deadTarget, group1}},
				{Members: []string{group2}},
			},
		},
	}
	config.ApplyNodeDefaults(&cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in := make(chan frame.Frame, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- fanoutLoop(ctx, cfg, in)
	}()

	expected := frame.Frame{
		NodeID:   7,
		Sequence: 42,
		Payload:  []byte{1, 2, 3, 4},
	}
	in <- expected

	got1 := waitFrame(t, recv1)
	got2 := waitFrame(t, recv2)
	assertFrameEqual(t, expected, got1)
	assertFrameEqual(t, expected, got2)

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("fanoutLoop returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fanoutLoop did not stop after cancellation")
	}
}

func TestRelayIngressFramePassesToEgress(t *testing.T) {
	t.Parallel()

	upstreamAddr, received := startFrameSink(t)
	ingressListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen ingress tcp: %v", err)
	}
	ingressAddr := ingressListener.Addr().String()
	_ = ingressListener.Close()

	cfg := config.NodeConfig{
		Profile: config.ProfileRelay,
		Ingress: config.IngressConfig{
			Enabled: true,
			Listen:  ingressAddr,
		},
		Egress: config.EgressConfig{
			Enabled:           true,
			ReconnectInterval: 1,
			Groups: []config.EgressGroup{
				{Members: []string{upstreamAddr}},
			},
		},
	}
	config.ApplyNodeDefaults(&cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	frames := make(chan frame.Frame, 8)
	fanoutErrCh := make(chan error, 1)
	go func() {
		fanoutErrCh <- fanoutLoop(ctx, cfg, frames)
	}()

	ingressErrCh := make(chan error, 1)
	go func() {
		ingressErrCh <- ingressLoop(ctx, cfg, func(incoming frame.Frame, _ string) error {
			select {
			case frames <- incoming:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()

	waitForTCPListener(t, ingressAddr)

	conn, err := net.Dial("tcp", ingressAddr)
	if err != nil {
		t.Fatalf("dial relay ingress: %v", err)
	}

	expected := frame.Frame{
		NodeID:   19,
		Sequence: 77,
		Payload:  []byte("relay-frame"),
	}
	if err := expected.WriteTo(conn); err != nil {
		t.Fatalf("write relay frame: %v", err)
	}
	_ = conn.Close()

	got := waitFrame(t, received)
	assertFrameEqual(t, expected, got)

	cancel()

	select {
	case err := <-ingressErrCh:
		if err != nil {
			t.Fatalf("ingressLoop returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ingressLoop did not stop after cancellation")
	}

	select {
	case err := <-fanoutErrCh:
		if err != nil {
			t.Fatalf("fanoutLoop returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fanoutLoop did not stop after cancellation")
	}
}

func startFrameSink(t *testing.T) (string, <-chan frame.Frame) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
	})

	received := make(chan frame.Frame, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		decoder := frame.NewDecoder()
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			frames, err := decoder.Push(buf[:n])
			if err != nil {
				return
			}
			for _, incoming := range frames {
				select {
				case received <- incoming:
				default:
				}
				return
			}
		}
	}()

	return listener.Addr().String(), received
}

func unavailableTCPAddress(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve tcp port: %v", err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	return addr
}

func waitFrame(t *testing.T, ch <-chan frame.Frame) frame.Frame {
	t.Helper()

	select {
	case incoming := <-ch:
		return incoming
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for frame")
		return frame.Frame{}
	}
}

func waitForTCPListener(t *testing.T, addr string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for listener %s", addr)
}

func assertFrameEqual(t *testing.T, want, got frame.Frame) {
	t.Helper()

	if want.NodeID != got.NodeID {
		t.Fatalf("node id mismatch: want %d got %d", want.NodeID, got.NodeID)
	}
	if want.Sequence != got.Sequence {
		t.Fatalf("sequence mismatch: want %d got %d", want.Sequence, got.Sequence)
	}
	if string(want.Payload) != string(got.Payload) {
		t.Fatalf("payload mismatch: want %v got %v", want.Payload, got.Payload)
	}
}
