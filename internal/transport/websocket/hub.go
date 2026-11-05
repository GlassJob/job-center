package websocket

import (
	"sync"
)

// Hub 按 instance_id 登记活跃连接，用于同实例重连时顶替旧会话。
type Hub struct {
	mu    sync.Mutex
	byInst map[string]*Conn
}

func NewHub() *Hub { return &Hub{byInst: map[string]*Conn{}} }

// Bind 绑定 instance_id → conn；若已有旧连接则返回它以便关闭。
func (h *Hub) Bind(instanceID string, c *Conn) (old *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	old = h.byInst[instanceID]
	h.byInst[instanceID] = c
	return old
}

// Unbind 仅当当前仍是 c 时移除。
func (h *Hub) Unbind(instanceID string, c *Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.byInst[instanceID] == c {
		delete(h.byInst, instanceID)
	}
}
