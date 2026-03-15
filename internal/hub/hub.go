package hub

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Hub tracks all WebSocket connections grouped by owner.
type Hub struct {
	mu    sync.Mutex
	conns map[string]map[*websocket.Conn]struct{}
}

func New() *Hub {
	return &Hub{conns: make(map[string]map[*websocket.Conn]struct{})}
}

func (h *Hub) Add(owner string, conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conns[owner] == nil {
		h.conns[owner] = make(map[*websocket.Conn]struct{})
	}
	h.conns[owner][conn] = struct{}{}
}

func (h *Hub) Remove(owner string, conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if set, ok := h.conns[owner]; ok {
		delete(set, conn)
		if len(set) == 0 {
			delete(h.conns, owner)
		}
	}
}

func (h *Hub) Broadcast(owner string, data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for conn := range h.conns[owner] {
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
			conn.Close()
			delete(h.conns[owner], conn)
		}
	}
}

// BroadcastAll calls getPayload for each connected owner and broadcasts the result.
func (h *Hub) BroadcastAll(getPayload func(owner string) []byte) {
	h.mu.Lock()
	owners := make([]string, 0, len(h.conns))
	for owner := range h.conns {
		owners = append(owners, owner)
	}
	h.mu.Unlock()

	for _, owner := range owners {
		data := getPayload(owner)
		h.Broadcast(owner, data)
	}
}
