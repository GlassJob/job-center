package model

import (
	"encoding/json"
	"time"
)

const (
	ExecStatusPending      = "PENDING"
	ExecStatusQueued       = "QUEUED"
	ExecStatusDispatching  = "DISPATCHING"
	ExecStatusRunning      = "RUNNING"
	ExecStatusRetryWait    = "RETRY_WAIT"
	ExecStatusSucceeded    = "SUCCEEDED"
	ExecStatusFailed       = "FAILED"
	ExecStatusCancelled    = "CANCELLED"
	ExecStatusTimedOut     = "TIMED_OUT"
	ExecStatusSkipped      = "SKIPPED"
	ExecStatusUnknown      = "UNKNOWN"

	TriggerManual = "MANUAL"
	TriggerAPI    = "API"
	TriggerCron   = "CRON"
	TriggerSystem = "SYSTEM"

	AttemptCreated      = "CREATED"
	AttemptDispatching  = "DISPATCHING"
	AttemptAcknowledged = "ACKNOWLEDGED"
	AttemptRunning      = "RUNNING"
	AttemptSucceeded    = "SUCCEEDED"
	AttemptFailed       = "FAILED"
	AttemptCancelled    = "CANCELLED"
	AttemptTimedOut     = "TIMED_OUT"
	AttemptLost         = "LOST"

	CancelNone               = "NONE"
	CancelRequested          = "REQUESTED"
	CancelSent               = "SENT"
	CancelAcked              = "ACKED"
	CancelStopped            = "STOPPED"
	CancelFailed             = "FAILED"
	CancelUnconfirmed        = "CANCEL_UNCONFIRMED"
)

type Execution struct {
	ID                string          `gorm:"column:id;primaryKey"`
	ServiceID         string          `gorm:"column:service_id"`
	JobID             string          `gorm:"column:job_id"`
	JobCode           string          `gorm:"column:job_code"`
	ScheduleID        *string         `gorm:"column:schedule_id"`
	TriggerType       string          `gorm:"column:trigger_type"`
	TriggeredBy       string          `gorm:"column:triggered_by"`
	TriggerContext    map[string]any  `gorm:"column:trigger_context;serializer:json"`
	Params            json.RawMessage `gorm:"column:params;type:json"`
	PolicySnapshot    map[string]any  `gorm:"column:policy_snapshot;serializer:json"`
	IdempotencyKey    *string         `gorm:"column:idempotency_key"`
	Status            string          `gorm:"column:status"`
	StatusReason      string          `gorm:"column:status_reason"`
	HandlerVersion    string          `gorm:"column:handler_version"`
	HandlerMajor      *uint           `gorm:"column:handler_major"`
	SchemaHash        string          `gorm:"column:schema_hash"`
	TimeoutSeconds    uint            `gorm:"column:timeout_seconds"`
	MaxAttempts       uint            `gorm:"column:max_attempts"`
	AttemptCount      uint            `gorm:"column:attempt_count"`
	CurrentAttemptNo  uint            `gorm:"column:current_attempt_no"`
	CurrentAttemptID  *string         `gorm:"column:current_attempt_id"`
	ConcurrencySlot   *string         `gorm:"column:concurrency_slot"`
	NextAttemptAt     *time.Time      `gorm:"column:next_attempt_at"`
	DeadlineAt        *time.Time      `gorm:"column:deadline_at"`
	CancelRequestedAt *time.Time      `gorm:"column:cancel_requested_at"`
	CancelRequestedBy string          `gorm:"column:cancel_requested_by"`
	Result            json.RawMessage `gorm:"column:result;type:json"`
	ResultSummary     string          `gorm:"column:result_summary"`
	ErrorCode         string          `gorm:"column:error_code"`
	ErrorMessage      string          `gorm:"column:error_message"`
	ScheduledAt       *time.Time      `gorm:"column:scheduled_at"`
	QueuedAt          *time.Time      `gorm:"column:queued_at"`
	StartedAt         *time.Time      `gorm:"column:started_at"`
	FinishedAt        *time.Time      `gorm:"column:finished_at"`
	Version           uint            `gorm:"column:version"`
	CreatedAt         time.Time       `gorm:"column:created_at"`
	UpdatedAt         time.Time       `gorm:"column:updated_at"`
}

func (Execution) TableName() string { return "executions" }

type Attempt struct {
	ID                string          `gorm:"column:id;primaryKey"`
	ExecutionID       string          `gorm:"column:execution_id"`
	AttemptNo         uint            `gorm:"column:attempt_no"`
	JobID             string          `gorm:"column:job_id"`
	ServiceID         string          `gorm:"column:service_id"`
	InstanceID        *string         `gorm:"column:instance_id"`
	InstanceKey       string          `gorm:"column:instance_key"`
	Hostname          string          `gorm:"column:hostname"`
	GitCommit         string          `gorm:"column:git_commit"`
	AppVersion        string          `gorm:"column:app_version"`
	HandlerVersion    string          `gorm:"column:handler_version"`
	LeaseToken        string          `gorm:"column:lease_token"`
	LeaseExpiresAt    *time.Time      `gorm:"column:lease_expires_at"`
	LastHeartbeatAt   *time.Time      `gorm:"column:last_heartbeat_at"`
	Status            string          `gorm:"column:status"`
	StatusReason      string          `gorm:"column:status_reason"`
	DispatchCount     uint            `gorm:"column:dispatch_count"`
	DispatchedAt      *time.Time      `gorm:"column:dispatched_at"`
	AckedAt           *time.Time      `gorm:"column:acked_at"`
	StartedAt         *time.Time      `gorm:"column:started_at"`
	FinishedAt        *time.Time      `gorm:"column:finished_at"`
	DeadlineAt        *time.Time      `gorm:"column:deadline_at"`
	DurationMs        *uint64         `gorm:"column:duration_ms"`
	ProgressPercent   *float64        `gorm:"column:progress_percent"`
	ProgressMessage   string          `gorm:"column:progress_message"`
	ProgressUpdatedAt *time.Time      `gorm:"column:progress_updated_at"`
	CancelState       string          `gorm:"column:cancel_state"`
	CancelRequestedAt *time.Time      `gorm:"column:cancel_requested_at"`
	CancelAckedAt     *time.Time      `gorm:"column:cancel_acked_at"`
	ExitCode          *int            `gorm:"column:exit_code"`
	ErrorCode         string          `gorm:"column:error_code"`
	ErrorMessage      string          `gorm:"column:error_message"`
	ErrorDetail       string          `gorm:"column:error_detail"`
	Result            json.RawMessage `gorm:"column:result;type:json"`
	LastLogSeq        uint64          `gorm:"column:last_log_seq"`
	LogInfoCollected  uint            `gorm:"column:log_info_collected"`
	LogWarnCollected  uint            `gorm:"column:log_warn_collected"`
	LogErrorCollected uint            `gorm:"column:log_error_collected"`
	LogInfoDropped    uint            `gorm:"column:log_info_dropped"`
	LogWarnDropped    uint            `gorm:"column:log_warn_dropped"`
	LogErrorDropped   uint            `gorm:"column:log_error_dropped"`
	LogWarnCount      uint            `gorm:"column:log_warn_count;->"`  // GENERATED
	LogErrorCount     uint            `gorm:"column:log_error_count;->"` // GENERATED
	LostAt            *time.Time      `gorm:"column:lost_at"`
	Version           uint            `gorm:"column:version"`
	CreatedAt         time.Time       `gorm:"column:created_at"`
	UpdatedAt         time.Time       `gorm:"column:updated_at"`
}

func (Attempt) TableName() string { return "execution_attempts" }

type ExecutionView struct {
	ID               string          `json:"id"`
	ServiceID        string          `json:"service_id"`
	ServiceCode      string          `json:"service_code,omitempty"`
	ServiceName      string          `json:"service_name,omitempty"`
	JobID            string          `json:"job_id"`
	JobCode          string          `json:"job_code"`
	JobName          string          `json:"job_name,omitempty"`
	TriggerType      string          `json:"trigger_type"`
	TriggeredBy      string          `json:"triggered_by"`
	Params           json.RawMessage `json:"params,omitempty"`
	Status           string          `json:"status"`
	StatusReason     string          `json:"status_reason,omitempty"`
	HandlerVersion   string          `json:"handler_version,omitempty"`
	TimeoutSeconds   uint            `json:"timeout_seconds"`
	MaxAttempts      uint            `json:"max_attempts"`
	AttemptCount     uint            `json:"attempt_count"`
	CurrentAttemptNo uint            `json:"current_attempt_no"`
	CurrentAttemptID *string         `json:"current_attempt_id,omitempty"`
	Result           json.RawMessage `json:"result,omitempty"`
	ResultSummary    string          `json:"result_summary,omitempty"`
	ErrorCode        string          `json:"error_code,omitempty"`
	ErrorMessage     string          `json:"error_message,omitempty"`
	QueuedAt         *time.Time      `json:"queued_at,omitempty"`
	StartedAt        *time.Time      `json:"started_at,omitempty"`
	FinishedAt       *time.Time      `json:"finished_at,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	Attempt          *AttemptView    `json:"attempt,omitempty"`
}

// ExecutionLog 对应 execution_logs 热表。
type ExecutionLog struct {
	ID          uint64          `gorm:"column:id;primaryKey" json:"id"`
	ExecutionID string          `gorm:"column:execution_id" json:"execution_id"`
	AttemptID   string          `gorm:"column:attempt_id" json:"attempt_id"`
	Sequence    uint64          `gorm:"column:sequence" json:"sequence"`
	Level       string          `gorm:"column:level" json:"level"`
	Stream      *string         `gorm:"column:stream" json:"stream,omitempty"`
	Message     string          `gorm:"column:message" json:"message"`
	Fields      json.RawMessage `gorm:"column:fields;type:json" json:"fields,omitempty"`
	Truncated   bool            `gorm:"column:truncated" json:"truncated"`
	LoggedAt    time.Time       `gorm:"column:logged_at" json:"logged_at"`
	CreatedAt   time.Time       `gorm:"column:created_at" json:"created_at"`
}

func (ExecutionLog) TableName() string { return "execution_logs" }

type ExecutionLogView struct {
	ID        uint64          `json:"id"`
	AttemptID string          `json:"attempt_id"`
	Sequence  uint64          `json:"sequence"`
	Level     string          `json:"level"`
	Stream    string          `json:"stream,omitempty"`
	Message   string          `json:"message"`
	Fields    json.RawMessage `json:"fields,omitempty"`
	Truncated bool            `json:"truncated,omitempty"`
	LoggedAt  time.Time       `json:"logged_at"`
	CreatedAt time.Time       `json:"created_at"`
}

func (l *ExecutionLog) View() ExecutionLogView {
	stream := ""
	if l.Stream != nil {
		stream = *l.Stream
	}
	return ExecutionLogView{
		ID: l.ID, AttemptID: l.AttemptID, Sequence: l.Sequence, Level: l.Level,
		Stream: stream, Message: l.Message, Fields: l.Fields, Truncated: l.Truncated,
		LoggedAt: l.LoggedAt, CreatedAt: l.CreatedAt,
	}
}

type AttemptView struct {
	ID                string          `json:"id"`
	AttemptNo         uint            `json:"attempt_no"`
	InstanceID        *string         `json:"instance_id,omitempty"`
	InstanceKey       string          `json:"instance_key,omitempty"`
	Status            string          `json:"status"`
	StatusReason      string          `json:"status_reason,omitempty"`
	HandlerVersion    string          `json:"handler_version,omitempty"`
	LeaseExpiresAt    *time.Time      `json:"lease_expires_at,omitempty"`
	DispatchedAt      *time.Time      `json:"dispatched_at,omitempty"`
	AckedAt           *time.Time      `json:"acked_at,omitempty"`
	StartedAt         *time.Time      `json:"started_at,omitempty"`
	FinishedAt        *time.Time      `json:"finished_at,omitempty"`
	ProgressPercent   *float64        `json:"progress_percent,omitempty"`
	ProgressMessage   string          `json:"progress_message,omitempty"`
	CancelState       string          `json:"cancel_state,omitempty"`
	Result            json.RawMessage `json:"result,omitempty"`
	ErrorCode         string          `json:"error_code,omitempty"`
	ErrorMessage      string          `json:"error_message,omitempty"`
	LogInfoCollected  uint            `json:"log_info_collected"`
	LogWarnCollected  uint            `json:"log_warn_collected"`
	LogErrorCollected uint            `json:"log_error_collected"`
	LogInfoDropped    uint            `json:"log_info_dropped"`
	LogWarnDropped    uint            `json:"log_warn_dropped"`
	LogErrorDropped   uint            `json:"log_error_dropped"`
	LogWarnCount      uint            `json:"log_warn_count"`
	LogErrorCount     uint            `json:"log_error_count"`
}

func (e *Execution) View(a *Attempt) ExecutionView {
	v := ExecutionView{
		ID: e.ID, ServiceID: e.ServiceID, JobID: e.JobID, JobCode: e.JobCode,
		TriggerType: e.TriggerType, TriggeredBy: e.TriggeredBy, Params: e.Params,
		Status: e.Status, StatusReason: e.StatusReason, HandlerVersion: e.HandlerVersion,
		TimeoutSeconds: e.TimeoutSeconds, MaxAttempts: e.MaxAttempts, AttemptCount: e.AttemptCount,
		CurrentAttemptNo: e.CurrentAttemptNo, CurrentAttemptID: e.CurrentAttemptID,
		Result: e.Result, ResultSummary: e.ResultSummary, ErrorCode: e.ErrorCode, ErrorMessage: e.ErrorMessage,
		QueuedAt: e.QueuedAt, StartedAt: e.StartedAt, FinishedAt: e.FinishedAt,
		CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
	}
	if a != nil {
		av := AttemptView{
			ID: a.ID, AttemptNo: a.AttemptNo, InstanceID: a.InstanceID, InstanceKey: a.InstanceKey,
			Status: a.Status, StatusReason: a.StatusReason, HandlerVersion: a.HandlerVersion,
			LeaseExpiresAt: a.LeaseExpiresAt, DispatchedAt: a.DispatchedAt, AckedAt: a.AckedAt,
			StartedAt: a.StartedAt, FinishedAt: a.FinishedAt,
			ProgressPercent: a.ProgressPercent, ProgressMessage: a.ProgressMessage,
			CancelState: a.CancelState,
			Result: a.Result, ErrorCode: a.ErrorCode, ErrorMessage: a.ErrorMessage,
			LogInfoCollected: a.LogInfoCollected, LogWarnCollected: a.LogWarnCollected, LogErrorCollected: a.LogErrorCollected,
			LogInfoDropped: a.LogInfoDropped, LogWarnDropped: a.LogWarnDropped, LogErrorDropped: a.LogErrorDropped,
			LogWarnCount: a.LogWarnCount, LogErrorCount: a.LogErrorCount,
		}
		v.Attempt = &av
	}
	return v
}
