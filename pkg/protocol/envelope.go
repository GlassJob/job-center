package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

const (
	Version     = "1.1"
	Subprotocol = "jobcenter.v1"
	TimeFormat  = "2006-01-02T15:04:05.000Z07:00"
)

const (
	TypeAuthRequest         = "auth.request"
	TypeAuthResponse        = "auth.response"
	TypeServiceRegister     = "service.register"
	TypeServiceRegisterAck  = "service.register_ack"
	TypeServiceHeartbeat    = "service.heartbeat"
	TypeServiceHeartbeatAck = "service.heartbeat_ack"
	TypeServiceUnregister   = "service.unregister"
	TypeSystemError         = "system.error"
	TypeSystemNotice        = "system.notice"
)

// Close codes (application).
const (
	CloseAuthFailed      = 4001
	CloseAuthTimeout     = 4002
	CloseSessionReplaced = 4003
	CloseUnsupported     = 4004
	CloseServiceDisabled = 4005
)

const (
	ErrAuthRequired            = "AUTH_REQUIRED"
	ErrAuthInvalidCredential   = "AUTH_INVALID_CREDENTIAL"
	ErrAuthTimeout             = "AUTH_TIMEOUT"
	ErrAuthServiceDisabled     = "AUTH_SERVICE_DISABLED"
	ErrAuthIPDenied            = "AUTH_IP_DENIED"
	ErrProtocolInvalidMessage  = "PROTOCOL_INVALID_MESSAGE"
	ErrProtocolUnknownType     = "PROTOCOL_UNKNOWN_TYPE"
	ErrProtocolUnsupported     = "PROTOCOL_UNSUPPORTED"
	ErrInstanceNotRegistered   = "INSTANCE_NOT_REGISTERED"
	ErrSessionReplaced         = "SESSION_REPLACED"
	ErrNotImplemented          = "NOT_IMPLEMENTED"
)

var ExecutorToCenter = map[string]bool{
	TypeAuthRequest: true, TypeServiceRegister: true, TypeServiceHeartbeat: true, TypeServiceUnregister: true,
	TypeTaskExecuteAck: true, TypeTaskStarted: true, TypeTaskProgress: true, TypeTaskLog: true,
	TypeTaskCheckpoint: true, TypeTaskCompleted: true, TypeTaskFailed: true,
	TypeTaskCancelAck: true, TypeTaskCancelled: true, TypeTaskSyncRunning: true,
}

var CenterToExecutor = map[string]bool{
	TypeAuthResponse: true, TypeServiceRegisterAck: true, TypeServiceHeartbeatAck: true,
	TypeTaskExecute: true, TypeTaskResultAck: true, TypeTaskCancel: true, TypeTaskSyncRunningAck: true,
	TypeSystemError: true, TypeSystemNotice: true,
}

var KnownTypes = map[string]bool{
	TypeAuthRequest: true, TypeAuthResponse: true,
	TypeServiceRegister: true, TypeServiceRegisterAck: true,
	TypeServiceHeartbeat: true, TypeServiceHeartbeatAck: true,
	TypeServiceUnregister: true,
	TypeTaskExecute: true, TypeTaskExecuteAck: true, TypeTaskStarted: true, TypeTaskProgress: true,
	TypeTaskLog: true, TypeTaskCheckpoint: true, TypeTaskCompleted: true, TypeTaskFailed: true,
	TypeTaskResultAck: true, TypeTaskCancel: true, TypeTaskCancelAck: true, TypeTaskCancelled: true,
	TypeTaskSyncRunning: true, TypeTaskSyncRunningAck: true,
	TypeSystemError: true, TypeSystemNotice: true,
}

type Envelope struct {
	ProtocolVersion string          `json:"protocol_version"`
	MessageID       string          `json:"message_id"`
	Type            string          `json:"type"`
	RequestID       *string         `json:"request_id,omitempty"`
	Timestamp       string          `json:"timestamp"`
	TraceID         *string         `json:"trace_id,omitempty"`
	Data            json.RawMessage `json:"data"`
}

func FormatTime(t time.Time) string { return t.UTC().Format(TimeFormat) }

func NewMessageID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "m_" + hex.EncodeToString(b[:])
}

func NewMessage(typ, requestID, traceID string, data any) ([]byte, string, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, "", err
	}
	id := NewMessageID()
	env := Envelope{
		ProtocolVersion: Version,
		MessageID:       id,
		Type:            typ,
		Timestamp:       FormatTime(time.Now()),
		Data:            raw,
	}
	if requestID != "" {
		env.RequestID = &requestID
	}
	if traceID != "" {
		env.TraceID = &traceID
	}
	b, err := json.Marshal(env)
	return b, id, err
}

func DecodeEnvelope(b []byte) (*Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}
	if env.ProtocolVersion == "" || env.MessageID == "" || env.Type == "" || env.Data == nil {
		return nil, fmt.Errorf("decode envelope: missing required fields")
	}
	return &env, nil
}

type SystemError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

type SystemNotice struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
