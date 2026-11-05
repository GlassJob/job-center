// Package protocol 定义 Executor ↔ Job Center WebSocket 协议 v1（docs: GlassJob/job-docs protocol-websocket.md）。
// 只依赖标准库与 JSON Schema 校验，不依赖传输或存储实现。
package protocol

import (
	"encoding/json"
	"time"

	"github.com/GlassJob/job-center/internal/infrastructure/idgen"
)

const (
	Version     = "1.0"
	Subprotocol = "jobcenter.v1"
)

// 消息类型。
const (
	TypeAuthRequest        = "auth.request"
	TypeAuthResponse       = "auth.response"
	TypeServiceRegister    = "service.register"
	TypeServiceRegisterAck = "service.register_ack"
	TypeServiceHeartbeat   = "service.heartbeat"
	TypeServiceHeartbeatAck = "service.heartbeat_ack"
	TypeServiceUnregister  = "service.unregister"
	TypeTaskExecute        = "task.execute"
	TypeTaskExecuteAck     = "task.execute_ack"
	TypeTaskStarted        = "task.started"
	TypeTaskProgress       = "task.progress"
	TypeTaskLog            = "task.log"
	TypeTaskCompleted      = "task.completed"
	TypeTaskFailed         = "task.failed"
	TypeTaskCancel         = "task.cancel"
	TypeTaskCancelAck      = "task.cancel_ack"
	TypeTaskCancelled      = "task.cancelled"
	TypeTaskResultAck      = "task.result_ack"
	TypeSystemError        = "system.error"
	TypeSystemNotice       = "system.notice"
)

// ExecutorToCenter 列出 Executor 可发送的类型（其余为中心下行类型，收到即 PROTOCOL_INVALID_MESSAGE）。
var ExecutorToCenter = map[string]bool{
	TypeAuthRequest: true, TypeServiceRegister: true, TypeServiceHeartbeat: true, TypeServiceUnregister: true,
	TypeTaskExecuteAck: true, TypeTaskStarted: true, TypeTaskProgress: true, TypeTaskLog: true,
	TypeTaskCompleted: true, TypeTaskFailed: true, TypeTaskCancelAck: true, TypeTaskCancelled: true,
	TypeSystemError: true,
}

// KnownTypes 全部已知类型。
var KnownTypes = map[string]bool{
	TypeAuthResponse: true, TypeServiceRegisterAck: true, TypeServiceHeartbeatAck: true, TypeTaskExecute: true,
	TypeTaskCancel: true, TypeTaskResultAck: true, TypeSystemNotice: true,
}

func init() {
	for k := range ExecutorToCenter {
		KnownTypes[k] = true
	}
}

// Envelope 统一消息信封（任务书 3.3）。
type Envelope struct {
	ProtocolVersion string          `json:"protocol_version"`
	MessageID       string          `json:"message_id"`
	Type            string          `json:"type"`
	RequestID       *string         `json:"request_id,omitempty"`
	Timestamp       string          `json:"timestamp"`
	TraceID         *string         `json:"trace_id,omitempty"`
	Data            json.RawMessage `json:"data"`
}

// TimeFormat RFC 3339 UTC 毫秒。
const TimeFormat = "2006-01-02T15:04:05.000Z07:00"

func FormatTime(t time.Time) string { return t.UTC().Format(TimeFormat) }

// NewMessage 构造中心下行消息；requestID 为空表示非响应类。
func NewMessage(typ string, requestID, traceID string, data any) ([]byte, string, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, "", err
	}
	env := Envelope{ProtocolVersion: Version, MessageID: idgen.NewULID(), Type: typ, Timestamp: FormatTime(time.Now()), Data: raw}
	if requestID != "" {
		env.RequestID = &requestID
	}
	if traceID != "" {
		env.TraceID = &traceID
	}
	b, err := json.Marshal(env)
	return b, env.MessageID, err
}
