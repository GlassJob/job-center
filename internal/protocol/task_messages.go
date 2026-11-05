package protocol

import "encoding/json"

type TaskExecute struct {
	ExecutionID    string          `json:"execution_id"`
	AttemptID      string          `json:"attempt_id"`
	AttemptNumber  int             `json:"attempt_number"`
	LeaseToken     string          `json:"lease_token"`
	LeaseExpiresAt string          `json:"lease_expires_at"`
	TaskCode       string          `json:"task_code"`
	TaskName       string          `json:"task_name,omitempty"`
	Params         json.RawMessage `json:"params"`
	TimeoutSeconds int             `json:"timeout_seconds"`
	DeadlineAt     string          `json:"deadline_at"`
	TriggerType    string          `json:"trigger_type,omitempty"`
	HandlerVersion string          `json:"handler_version,omitempty"`
}

type TaskExecuteAck struct {
	AttemptID  string     `json:"attempt_id"`
	LeaseToken string     `json:"lease_token"`
	Accepted   bool       `json:"accepted"`
	Error      *ErrorBody `json:"error,omitempty"`
}

type TaskStarted struct {
	AttemptID  string `json:"attempt_id"`
	LeaseToken string `json:"lease_token"`
	StartedAt  string `json:"started_at"`
}

type TaskProgress struct {
	AttemptID  string   `json:"attempt_id"`
	LeaseToken string   `json:"lease_token"`
	Percent    *float64 `json:"percent,omitempty"`
	Message    string   `json:"message,omitempty"`
}

type TaskLogEntry struct {
	Seq     int64           `json:"seq"`
	Level   string          `json:"level"`
	Message string          `json:"message"`
	Stream  string          `json:"stream,omitempty"`
	Fields  json.RawMessage `json:"fields,omitempty"`
	Ts      string          `json:"ts"`
}

type TaskLog struct {
	AttemptID  string         `json:"attempt_id"`
	LeaseToken string         `json:"lease_token"`
	Entries    []TaskLogEntry `json:"entries"`
	Dropped    int            `json:"dropped,omitempty"`
}

type TaskCompleted struct {
	AttemptID  string          `json:"attempt_id"`
	LeaseToken string          `json:"lease_token"`
	FinishedAt string          `json:"finished_at"`
	Result     json.RawMessage `json:"result,omitempty"`
}

type TaskFailedError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

type TaskFailed struct {
	AttemptID  string          `json:"attempt_id"`
	LeaseToken string          `json:"lease_token"`
	FinishedAt string          `json:"finished_at"`
	Reason     string          `json:"reason"`
	Error      TaskFailedError `json:"error"`
	ExitCode   *int            `json:"exit_code,omitempty"`
}

type TaskCancel struct {
	ExecutionID  string `json:"execution_id"`
	AttemptID    string `json:"attempt_id"`
	LeaseToken   string `json:"lease_token"`
	Reason       string `json:"reason"`
	GraceSeconds int    `json:"grace_seconds,omitempty"`
}

type TaskCancelAck struct {
	AttemptID string `json:"attempt_id"`
	State     string `json:"state"` // CANCELLING|NOT_FOUND|ALREADY_FINISHED
}

type TaskCancelled struct {
	AttemptID  string `json:"attempt_id"`
	LeaseToken string `json:"lease_token"`
	FinishedAt string `json:"finished_at"`
	StoppedBy  string `json:"stopped_by"`
}

type TaskResultAck struct {
	Status           string `json:"status"` // OK|REJECTED|RETRY_LATER
	AttemptID        string `json:"attempt_id"`
	ErrorCode        string `json:"error_code,omitempty"`
	Message          string `json:"message,omitempty"`
	AckedThroughSeq  *int64 `json:"acked_through_seq,omitempty"`
}
