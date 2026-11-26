package realtime

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
)

type AdminClient struct {
	hub    *AdminHub
	Conn   *websocket.Conn
	send   chan []byte
	closed atomic.Bool
	once   sync.Once
}

type AdminHub struct {
	mu             sync.RWMutex
	subscribers    map[*AdminClient]struct{}
	allowedOrigins map[string]struct{}
}

func NewAdminHub(allowedOrigins []string) *AdminHub {
	origins := make(map[string]struct{}, len(allowedOrigins))
	for _, o := range allowedOrigins {
		trimmed := strings.ToLower(strings.TrimRight(strings.TrimSpace(o), "/"))
		if trimmed != "" {
			origins[trimmed] = struct{}{}
		}
	}
	return &AdminHub{
		subscribers:    make(map[*AdminClient]struct{}),
		allowedOrigins: origins,
	}
}

func (h *AdminHub) CheckOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	if h == nil || len(h.allowedOrigins) == 0 {
		return true
	}
	_, ok := h.allowedOrigins[strings.ToLower(strings.TrimRight(origin, "/"))]
	return ok
}

func (h *AdminHub) Subscribe(conn *websocket.Conn) *AdminClient {
	c := &AdminClient{
		hub:  h,
		Conn: conn,
		send: make(chan []byte, 64),
	}
	h.mu.Lock()
	h.subscribers[c] = struct{}{}
	h.mu.Unlock()
	return c
}

func (c *AdminClient) Send(payload []byte) {
	defer func() { _ = recover() }()
	if c.closed.Load() {
		return
	}
	select {
	case c.send <- payload:
	default:
	}
}

func (c *AdminClient) Unsubscribe() {
	c.once.Do(func() {
		c.closed.Store(true)
		if c.hub != nil {
			c.hub.mu.Lock()
			delete(c.hub.subscribers, c)
			c.hub.mu.Unlock()
		}
		_ = c.Conn.Close()
	})
}

func (c *AdminClient) WritePump() {
	defer c.Unsubscribe()
	for payload := range c.send {
		if err := c.Conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			return
		}
	}
}

func (h *AdminHub) Broadcast(v any) {
	if h == nil {
		return
	}
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	h.mu.RLock()
	clients := make([]*AdminClient, 0, len(h.subscribers))
	for client := range h.subscribers {
		clients = append(clients, client)
	}
	h.mu.RUnlock()

	for _, client := range clients {
		client.Send(data)
	}
}
