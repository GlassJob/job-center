package gateway

import (
	"errors"
	"sync"

	"github.com/GlassJob/job-center/pkg/protocol"
)

var ErrNoConnection = errors.New("instance not connected")

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

func (h *Hub) Get(instanceID string) *Conn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.byInst[instanceID]
}

// Send 向指定实例连接入队一帧（实现 service.TaskDispatcher）。
func (h *Hub) Send(instanceID, typ string, data any) error {
	c := h.Get(instanceID)
	if c == nil {
		return ErrNoConnection
	}
	b, _, err := protocol.NewMessage(typ, "", "", data)
	if err != nil {
		return err
	}
	return c.enqueue(b)
}
