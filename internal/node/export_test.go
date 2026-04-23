package node

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"traptunnel/internal/config"
	"traptunnel/internal/frame"
)

func TestExportHubBroadcastsToMultipleClients(t *testing.T) {
	t.Parallel()

	cfg := config.NodeConfig{
		Profile: config.ProfileRelay,
		Export: config.ExportConfig{
			Enabled:    true,
			Listen:     "127.0.0.1:0",
			Format:     "frame",
			MaxClients: 8,
		},
	}
	config.ApplyNodeDefaults(&cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hub, err := startExportHub(ctx, cfg)
	if err != nil {
		t.Fatalf("startExportHub returned error: %v", err)
	}

	connA, err := net.Dial("tcp", hub.listener.Addr().String())
	if err != nil {
		t.Fatalf("dial client A: %v", err)
	}
	defer connA.Close()

	connB, err := net.Dial("tcp", hub.listener.Addr().String())
	if err != nil {
		t.Fatalf("dial client B: %v", err)
	}
	defer connB.Close()

	waitForExportClients(t, hub, 2)

	expected := frame.Frame{
		NodeID:   12,
		Sequence: 34,
		Payload:  []byte("export-frame"),
	}
	hub.Publish(expected)

	gotA := readExportFrame(t, connA)
	gotB := readExportFrame(t, connB)
	assertFrameEqual(t, expected, gotA)
	assertFrameEqual(t, expected, gotB)
}

func TestExportHubDisconnectsSlowClient(t *testing.T) {
	t.Parallel()

	blocked := newBlockingConn()
	hub := &exportHub{
		cfg: config.NodeConfig{
			Profile: config.ProfileRelay,
			Export: config.ExportConfig{
				Enabled:          true,
				Format:           "frame",
				MaxClients:       1,
				SlowClientPolicy: "disconnect",
			},
		},
		clientBuf: 1,
		clients:   make(map[uint64]*exportClient),
	}
	config.ApplyNodeDefaults(&hub.cfg)

	client := &exportClient{
		id:     1,
		conn:   blocked,
		frames: make(chan frame.Frame, 1),
	}
	hub.clients[client.id] = client

	done := make(chan struct{})
	go func() {
		hub.clientLoop(client)
		close(done)
	}()

	frame1 := frame.Frame{NodeID: 1, Sequence: 1, Payload: []byte("one")}
	frame2 := frame.Frame{NodeID: 1, Sequence: 2, Payload: []byte("two")}
	frame3 := frame.Frame{NodeID: 1, Sequence: 3, Payload: []byte("three")}

	hub.Publish(frame1)
	waitForBlockingConnWrite(t, blocked)
	hub.Publish(frame2)
	hub.Publish(frame3)

	waitForExportClients(t, hub, 0)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("slow client loop did not exit")
	}
}

func TestExportHubDropOldestPolicy(t *testing.T) {
	t.Parallel()

	hub := &exportHub{
		cfg: config.NodeConfig{
			Profile: config.ProfileRelay,
			Export: config.ExportConfig{
				Enabled:          true,
				Format:           "frame",
				MaxClients:       1,
				SlowClientPolicy: "drop_oldest",
			},
		},
		clientBuf: 1,
		clients:   make(map[uint64]*exportClient),
	}
	config.ApplyNodeDefaults(&hub.cfg)

	client := &exportClient{
		id:     1,
		conn:   newBlockingConn(),
		frames: make(chan frame.Frame, 1),
	}
	hub.clients[client.id] = client

	oldest := frame.Frame{NodeID: 1, Sequence: 1, Payload: []byte("old")}
	newest := frame.Frame{NodeID: 1, Sequence: 2, Payload: []byte("new")}
	hub.Publish(oldest)
	hub.Publish(newest)

	got := <-client.frames
	assertFrameEqual(t, newest, got)
}

func TestExportHubDropNewestPolicy(t *testing.T) {
	t.Parallel()

	hub := &exportHub{
		cfg: config.NodeConfig{
			Profile: config.ProfileRelay,
			Export: config.ExportConfig{
				Enabled:          true,
				Format:           "frame",
				MaxClients:       1,
				SlowClientPolicy: "drop_newest",
			},
		},
		clientBuf: 1,
		clients:   make(map[uint64]*exportClient),
	}
	config.ApplyNodeDefaults(&hub.cfg)

	client := &exportClient{
		id:     1,
		conn:   newBlockingConn(),
		frames: make(chan frame.Frame, 1),
	}
	hub.clients[client.id] = client

	oldest := frame.Frame{NodeID: 1, Sequence: 1, Payload: []byte("old")}
	newest := frame.Frame{NodeID: 1, Sequence: 2, Payload: []byte("new")}
	hub.Publish(oldest)
	hub.Publish(newest)

	got := <-client.frames
	assertFrameEqual(t, oldest, got)

	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.clients) != 1 {
		t.Fatalf("drop_newest should keep slow client connected, got %d clients", len(hub.clients))
	}
}

func readExportFrame(t *testing.T, conn net.Conn) frame.Frame {
	t.Helper()

	decoder := frame.NewDecoder()
	buf := make([]byte, 4096)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	for {
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("read export frame: %v", err)
		}
		frames, err := decoder.Push(buf[:n])
		if err != nil {
			t.Fatalf("decode export frame: %v", err)
		}
		if len(frames) > 0 {
			return frames[0]
		}
	}
}

func waitForExportClients(t *testing.T, hub *exportHub, want int) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		hub.mu.Lock()
		got := len(hub.clients)
		hub.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for export clients=%d", want)
}

type blockingConn struct {
	mu       sync.Mutex
	closed   bool
	writeHit chan struct{}
	closeCh  chan struct{}
}

func newBlockingConn() *blockingConn {
	return &blockingConn{
		writeHit: make(chan struct{}, 1),
		closeCh:  make(chan struct{}),
	}
}

func (c *blockingConn) Read(_ []byte) (int, error) {
	<-c.closeCh
	return 0, io.EOF
}

func (c *blockingConn) Write(_ []byte) (int, error) {
	select {
	case c.writeHit <- struct{}{}:
	default:
	}
	<-c.closeCh
	return 0, errors.New("closed")
}

func (c *blockingConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	close(c.closeCh)
	return nil
}

func (c *blockingConn) LocalAddr() net.Addr  { return dummyAddr("local") }
func (c *blockingConn) RemoteAddr() net.Addr { return dummyAddr("remote") }
func (c *blockingConn) SetDeadline(_ time.Time) error {
	return nil
}
func (c *blockingConn) SetReadDeadline(_ time.Time) error {
	return nil
}
func (c *blockingConn) SetWriteDeadline(_ time.Time) error {
	return nil
}

func waitForBlockingConnWrite(t *testing.T, conn *blockingConn) {
	t.Helper()
	select {
	case <-conn.writeHit:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for blocking conn write")
	}
}

type dummyAddr string

func (a dummyAddr) Network() string { return "test" }
func (a dummyAddr) String() string  { return string(a) }
