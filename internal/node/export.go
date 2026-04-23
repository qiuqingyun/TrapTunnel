package node

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"traptunnel/internal/config"
	"traptunnel/internal/frame"
)

type exportHub struct {
	cfg       config.NodeConfig
	listener  net.Listener
	clientBuf int
	nextID    uint64
	mu        sync.Mutex
	clients   map[uint64]*exportClient
}

type exportClient struct {
	id     uint64
	conn   net.Conn
	frames chan frame.Frame
}

func startExportHub(ctx context.Context, cfg config.NodeConfig) (*exportHub, error) {
	listener, err := net.Listen("tcp", cfg.Export.Listen)
	if err != nil {
		return nil, err
	}

	hub := &exportHub{
		cfg:       cfg,
		listener:  listener,
		clientBuf: cfg.Tuning.ExportClientBufferSize,
		clients:   make(map[uint64]*exportClient),
	}

	go hub.acceptLoop(ctx)
	go func() {
		<-ctx.Done()
		_ = hub.listener.Close()
		hub.closeAll()
	}()

	slog.Info("export listener 已启动", "component", "node", "profile", cfg.Profile, "event", "ExportStartup", "listen", cfg.Export.Listen, "format", cfg.Export.Format)
	return hub, nil
}

func (h *exportHub) acceptLoop(ctx context.Context) {
	for {
		conn, err := h.listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			slog.Error("export accept 失败", "component", "node", "profile", h.cfg.Profile, "event", "ExportAcceptError", "error", err)
			continue
		}

		if !h.registerClient(conn) {
			slog.Error("export 客户端超过上限", "component", "node", "profile", h.cfg.Profile, "event", "ExportClientRejected", "client", conn.RemoteAddr().String(), "max_clients", h.cfg.Export.MaxClients)
			_ = conn.Close()
			continue
		}
	}
}

func (h *exportHub) registerClient(conn net.Conn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.clients) >= h.cfg.Export.MaxClients {
		return false
	}

	id := atomic.AddUint64(&h.nextID, 1)
	client := &exportClient{
		id:     id,
		conn:   conn,
		frames: make(chan frame.Frame, h.clientBuf),
	}
	h.clients[id] = client

	slog.Info("export 客户端已连接", "component", "node", "profile", h.cfg.Profile, "event", "ExportClientConnected", "client", conn.RemoteAddr().String(), "client_id", id)
	go h.clientLoop(client)
	go h.watchClient(client)
	return true
}

func (h *exportHub) clientLoop(client *exportClient) {
	defer h.closeClient(client.id, "client closed")

	for outgoing := range client.frames {
		_ = client.conn.SetWriteDeadline(time.Now().Add(time.Duration(h.cfg.Tuning.ExportWriteTimeoutMS) * time.Millisecond))
		if err := outgoing.WriteToWithLimit(client.conn, uint32(h.cfg.Tuning.MaxFrameTotalLength)); err != nil {
			if !errors.Is(err, io.EOF) {
				slog.Error("export 发送失败", "component", "node", "profile", h.cfg.Profile, "event", "ExportSendError", "client", client.conn.RemoteAddr().String(), "client_id", client.id, "error", err)
			}
			return
		}
	}
}

func (h *exportHub) watchClient(client *exportClient) {
	buf := make([]byte, 1)
	for {
		_, err := client.conn.Read(buf)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				h.closeClient(client.id, "client read error")
				return
			}
			h.closeClient(client.id, "peer closed")
			return
		}
		h.closeClient(client.id, "unexpected client input")
		return
	}
}

func (h *exportHub) Publish(incoming frame.Frame) {
	slow := make([]uint64, 0)

	h.mu.Lock()
	for _, client := range h.clients {
		if uint32(incoming.TotalLength()) > uint32(h.cfg.Tuning.MaxFrameTotalLength) {
			slog.Error("export 帧超过上限，已丢弃", "component", "node", "profile", h.cfg.Profile, "event", "ExportFrameTooLarge", "size", incoming.TotalLength(), "limit", h.cfg.Tuning.MaxFrameTotalLength)
			break
		}

		outgoing := incoming.Clone()
		select {
		case client.frames <- outgoing:
		default:
			switch h.cfg.Export.SlowClientPolicy {
			case "drop_oldest":
				select {
				case <-client.frames:
				default:
				}
				select {
				case client.frames <- outgoing:
				default:
					slow = append(slow, client.id)
				}
			case "drop_newest":
				slog.Error("export 客户端过慢，已丢弃最新帧", "component", "node", "profile", h.cfg.Profile, "event", "ExportClientDropNewest", "client", client.conn.RemoteAddr().String(), "client_id", client.id)
			default:
				slow = append(slow, client.id)
			}
		}
	}
	h.mu.Unlock()

	for _, id := range slow {
		h.closeClient(id, "slow client")
	}
}

func (h *exportHub) closeClient(id uint64, reason string) {
	h.mu.Lock()
	client, ok := h.clients[id]
	if !ok {
		h.mu.Unlock()
		return
	}
	delete(h.clients, id)
	h.mu.Unlock()

	close(client.frames)
	_ = client.conn.Close()
	slog.Info("export 客户端已断开", "component", "node", "profile", h.cfg.Profile, "event", "ExportClientClosed", "client", client.conn.RemoteAddr().String(), "client_id", client.id, "reason", reason)
}

func (h *exportHub) closeAll() {
	h.mu.Lock()
	ids := make([]uint64, 0, len(h.clients))
	for id := range h.clients {
		ids = append(ids, id)
	}
	h.mu.Unlock()

	for _, id := range ids {
		h.closeClient(id, "shutdown")
	}
}
