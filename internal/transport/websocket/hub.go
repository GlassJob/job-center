package websocket

import (
	"errors"
	"sync"

	"github.com/GlassJob/job-center/internal/protocol"
)

// Hub 按 instance_id 登记活跃连接。
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

// Send 向指定实例连接推送一条中心→执行器消息。
func (h *Hub) Send(instanceID, typ string, data any) error {
	h.mu.Lock()
	c := h.byInst[instanceID]
	h.mu.Unlock()
	if c == nil {
		return ErrInstanceOffline
	}
	b, _, err := protocol.NewMessage(typ, "", "", data)
	if err != nil {
		return err
	}
	return c.enqueue(b)
}

var ErrInstanceOffline = errors.New("instance offline")
