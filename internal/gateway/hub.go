package gateway

import "sync"

// Hub 按中心 instance_id 登记活跃 WS 连接。
type Hub struct {
	mu     sync.Mutex
	byInst map[string]*Conn
}

func NewHub() *Hub { return &Hub{byInst: map[string]*Conn{}} }

func (h *Hub) Bind(instanceID string, c *Conn) (old *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	old = h.byInst[instanceID]
	h.byInst[instanceID] = c
	return old
}

func (h *Hub) Unbind(instanceID string, c *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.byInst[instanceID] == c {
		delete(h.byInst, instanceID)
	}
}
