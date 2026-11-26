package realtime

import (
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type SeatClient struct {
	Conn    *websocket.Conn
	eventID uuid.UUID
	hub     *SeatHub
	send    chan []byte
	closed  atomic.Bool
	once    sync.Once
}

type SeatHub struct {
	mu          sync.RWMutex
	subscribers map[uuid.UUID]map[*SeatClient]struct{}
}

func NewSeatHub() *SeatHub {
	return &SeatHub{subscribers: make(map[uuid.UUID]map[*SeatClient]struct{})}
}

func (h *SeatHub) Subscribe(eventID uuid.UUID, conn *websocket.Conn) *SeatClient {
	client := &SeatClient{
		Conn:    conn,
		eventID: eventID,
		hub:     h,
		send:    make(chan []byte, 16),
	}
	h.mu.Lock()
	if h.subscribers[eventID] == nil {
		h.subscribers[eventID] = make(map[*SeatClient]struct{})
	}
	h.subscribers[eventID][client] = struct{}{}
	h.mu.Unlock()
	return client
}

func (h *SeatHub) Broadcast(eventID uuid.UUID, payload []byte) {
	h.mu.RLock()
	clients := make([]*SeatClient, 0, len(h.subscribers[eventID]))
	for client := range h.subscribers[eventID] {
		clients = append(clients, client)
	}
	h.mu.RUnlock()
	for _, client := range clients {
		client.Send(payload)
	}
}

func (c *SeatClient) Send(payload []byte) {
	defer func() { _ = recover() }()
	if c.closed.Load() {
		return
	}
	select {
	case c.send <- payload:
	default:
	}
}

func (c *SeatClient) Unsubscribe() {
	c.once.Do(func() {
		c.closed.Store(true)
		c.hub.mu.Lock()
		if clients := c.hub.subscribers[c.eventID]; clients != nil {
			delete(clients, c)
			if len(clients) == 0 {
				delete(c.hub.subscribers, c.eventID)
			}
		}
		c.hub.mu.Unlock()
		_ = c.Conn.Close()
	})
}

func (c *SeatClient) WritePump() {
	defer c.Unsubscribe()
	for payload := range c.send {
		if err := c.Conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			return
		}
	}
}

func (c *SeatClient) ReadPump() {
	defer c.Unsubscribe()
	for {
		if _, _, err := c.Conn.ReadMessage(); err != nil {
			return
		}
	}
}
