package protocol

import "encoding/json"

// ErrorBody 通用错误对象。
type ErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

type SDKInfo struct {
	Language string `json:"language"`
	Version  string `json:"version"`
}

type AuthRequest struct {
	ServiceCode      string   `json:"service_code"`
	Credential       string   `json:"credential"`
	InstanceID       string   `json:"instance_id"`
	ProtocolVersions []string `json:"protocol_versions"`
	SDK              SDKInfo  `json:"sdk"`
	Resume           *struct {
		PreviousSessionEpoch int64 `json:"previous_session_epoch"`
	} `json:"resume,omitempty"`
}

type AuthResponse struct {
	OK                       bool       `json:"ok"`
	SessionID                string     `json:"session_id,omitempty"`
	SessionEpoch             int64      `json:"session_epoch,omitempty"`
	ProtocolVersion          string     `json:"protocol_version,omitempty"`
	ServerTime               string     `json:"server_time,omitempty"`
	HeartbeatIntervalSeconds int        `json:"heartbeat_interval_seconds,omitempty"`
	LeaseTTLSeconds          int        `json:"lease_ttl_seconds,omitempty"`
	MaxMessageBytes          int64      `json:"max_message_bytes,omitempty"`
	LogBatchMaxEntries       int        `json:"log_batch_max_entries,omitempty"`
	Error                    *ErrorBody `json:"error,omitempty"`
}

type HostInfo struct {
	Hostname string `json:"hostname"`
	IP       string `json:"ip,omitempty"`
	PID      int    `json:"pid,omitempty"`
}

type RuntimeInfo struct {
	Language string `json:"language"`
	Version  string `json:"version"`
}

type DeploymentInfo struct {
	Version   string `json:"version"`
	GitCommit string `json:"git_commit"`
	BuildTime string `json:"build_time,omitempty"`
}

type TaskDecl struct {
	TaskCode       string          `json:"task_code"`
	TaskName       string          `json:"task_name"`
	Description    string          `json:"description,omitempty"`
	HandlerVersion string          `json:"handler_version"`
	DefaultCron    *string         `json:"default_cron,omitempty"`
	TimeoutSeconds int             `json:"timeout_seconds,omitempty"`
	MaxConcurrency int             `json:"max_concurrency,omitempty"`
	InputSchema    json.RawMessage `json:"input_schema,omitempty"`
}

type RunningAttempt struct {
	AttemptID   string `json:"attempt_id"`
	ExecutionID string `json:"execution_id"`
	LeaseToken  string `json:"lease_token"`
	LocalState  string `json:"local_state"`
	StartedAt   string `json:"started_at,omitempty"`
}

type RegisterData struct {
	ServiceCode     string            `json:"service_code"`
	ServiceName     string            `json:"service_name"`
	InstanceID      string            `json:"instance_id"`
	Environment     string            `json:"environment,omitempty"`
	Host            *HostInfo         `json:"host,omitempty"`
	Runtime         RuntimeInfo       `json:"runtime"`
	Deployment      DeploymentInfo    `json:"deployment"`
	Capacity        struct {
		MaxConcurrency int `json:"max_concurrency"`
	} `json:"capacity"`
	StartedAt       string            `json:"started_at,omitempty"`
	Tasks           []TaskDecl        `json:"tasks"`
	RunningAttempts []RunningAttempt  `json:"running_attempts,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
}

type AcceptedTask struct {
	TaskCode string `json:"task_code"`
	TaskID   int64  `json:"task_id"`
}

type TaskIssue struct {
	TaskCode string `json:"task_code,omitempty"`
	Code     string `json:"code"`
	Message  string `json:"message,omitempty"`
}

type AttemptDecision struct {
	AttemptID      string `json:"attempt_id"`
	Decision       string `json:"decision"` // CONTINUE|ABORT|SEND_RESULT
	LeaseExpiresAt string `json:"lease_expires_at,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

type RegisterAck struct {
	OK             bool              `json:"ok"`
	InstanceStatus string            `json:"instance_status,omitempty"`
	AcceptedTasks  []AcceptedTask    `json:"accepted_tasks,omitempty"`
	RejectedTasks  []TaskIssue       `json:"rejected_tasks,omitempty"`
	Warnings       []TaskIssue       `json:"warnings,omitempty"`
	Attempts       []AttemptDecision `json:"attempts,omitempty"`
	Error          *ErrorBody        `json:"error,omitempty"`
}

type HeartbeatAttempt struct {
	AttemptID  string `json:"attempt_id"`
	LeaseToken string `json:"lease_token"`
}

type Heartbeat struct {
	InstanceID     string             `json:"instance_id"`
	Status         string             `json:"status"` // ONLINE|DRAINING
	Running        int                `json:"running"`
	MaxConcurrency *int               `json:"max_concurrency,omitempty"`
	Attempts       []HeartbeatAttempt `json:"attempts,omitempty"`
	Metrics        map[string]any     `json:"metrics,omitempty"`
}

type LeaseState struct {
	AttemptID      string `json:"attempt_id"`
	Valid          bool   `json:"valid"`
	LeaseExpiresAt string `json:"lease_expires_at,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

type HeartbeatAck struct {
	ServerTime     string       `json:"server_time"`
	InstanceStatus string       `json:"instance_status,omitempty"`
	Leases         []LeaseState `json:"leases,omitempty"`
}

type Unregister struct {
	InstanceID          string `json:"instance_id"`
	Reason              string `json:"reason"`
	DrainTimeoutSeconds int    `json:"drain_timeout_seconds,omitempty"`
}

type SystemError struct {
	Code         string         `json:"code"`
	Message      string         `json:"message"`
	Retryable    bool           `json:"retryable,omitempty"`
	RefMessageID *string        `json:"ref_message_id,omitempty"`
	Details      map[string]any `json:"details,omitempty"`
}

type SystemNotice struct {
	Code    string         `json:"code"`
	Message string         `json:"message,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

// 错误码（protocol-websocket.md §10）。
const (
	ErrAuthRequired             = "AUTH_REQUIRED"
	ErrAuthInvalidCredential    = "AUTH_INVALID_CREDENTIAL"
	ErrAuthServiceDisabled      = "AUTH_SERVICE_DISABLED"
	ErrAuthTimeout              = "AUTH_TIMEOUT"
	ErrProtocolUnsupported      = "PROTOCOL_UNSUPPORTED_VERSION"
	ErrProtocolInvalidMessage   = "PROTOCOL_INVALID_MESSAGE"
	ErrProtocolUnknownType      = "PROTOCOL_UNKNOWN_TYPE"
	ErrProtocolMessageTooLarge  = "PROTOCOL_MESSAGE_TOO_LARGE"
	ErrRateLimited              = "RATE_LIMITED"
	ErrRegisterTaskNotAllowed   = "REGISTER_TASK_NOT_ALLOWED"
	ErrRegisterInvalidSchema    = "REGISTER_INVALID_SCHEMA"
	ErrRegisterInvalidCron      = "REGISTER_INVALID_CRON"
	ErrRegisterInvalidVersion   = "REGISTER_INVALID_VERSION"
	ErrRegisterDuplicateTask    = "REGISTER_DUPLICATE_TASK"
	ErrInstanceNotRegistered    = "INSTANCE_NOT_REGISTERED"
	ErrSessionReplaced          = "SESSION_REPLACED"
	ErrAttemptNotFound          = "ATTEMPT_NOT_FOUND"
	ErrLeaseInvalid             = "LEASE_INVALID"
	ErrStorageUnavailable       = "STORAGE_UNAVAILABLE"
	ErrConnectionLimit          = "CONNECTION_LIMIT"
	ErrNotImplemented           = "NOT_IMPLEMENTED"
	ErrInternal                 = "INTERNAL_ERROR"
)

// WebSocket 关闭码。
const (
	CloseAuthFailed      = 4001
	CloseAuthTimeout     = 4002
	CloseSessionReplaced = 4003
	CloseUnsupported     = 4004
	CloseRateLimited     = 4008
	CloseServiceDisabled = 4009
)
