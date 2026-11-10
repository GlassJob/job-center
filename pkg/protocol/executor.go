package protocol

import "encoding/json"

type ErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

type HostInfo struct {
	Hostname string `json:"hostname"`
	IP       string `json:"ip,omitempty"`
	PID      int    `json:"pid,omitempty"`
}

type RuntimeInfo struct {
	Language string `json:"language"`
	Version  string `json:"version,omitempty"`
}

type DeploymentInfo struct {
	Version   string `json:"version,omitempty"`
	GitCommit string `json:"git_commit,omitempty"`
	BuildTime string `json:"build_time,omitempty"`
}

type CapacityInfo struct {
	MaxConcurrency int `json:"max_concurrency"`
}

type JobDecl struct {
	Code           string          `json:"job_code"`
	Name           string          `json:"job_name"`
	Description    string          `json:"description,omitempty"`
	Handler        string          `json:"handler,omitempty"`
	HandlerVersion string          `json:"handler_version"`
	DefaultCron    *string         `json:"default_cron,omitempty"`
	TimeoutSeconds int             `json:"timeout_seconds,omitempty"`
	MaxConcurrency int             `json:"max_concurrency,omitempty"`
	ParamsSchema   json.RawMessage `json:"params_schema,omitempty"`
}

// RegisterRequest / RegisterData：HTTP 与 WS service.register data 共用。
type RegisterRequest struct {
	ServiceCode string         `json:"code"`
	InstanceKey string         `json:"instance_key"`
	Environment string         `json:"environment,omitempty"`
	Host        *HostInfo      `json:"host,omitempty"`
	Runtime     RuntimeInfo    `json:"runtime"`
	Deployment  DeploymentInfo `json:"deployment"`
	Capacity    CapacityInfo   `json:"capacity"`
	SDKVersion  string         `json:"sdk_version,omitempty"`
	StartedAt   string         `json:"started_at,omitempty"`
	Jobs        []JobDecl      `json:"jobs"`
}

type AcceptedJob struct {
	JobCode string `json:"job_code"`
	JobID   string `json:"job_id"`
}

type JobIssue struct {
	JobCode string `json:"job_code,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

type RegisterResponse struct {
	OK             bool          `json:"ok"`
	InstanceID     string        `json:"instance_id,omitempty"`
	InstanceKey    string        `json:"instance_key,omitempty"`
	SessionID      string        `json:"session_id,omitempty"`
	SessionEpoch   int64         `json:"session_epoch,omitempty"`
	InstanceStatus string        `json:"instance_status,omitempty"`
	AcceptedJobs   []AcceptedJob `json:"accepted_jobs,omitempty"`
	RejectedJobs   []JobIssue    `json:"rejected_jobs,omitempty"`
	Warnings       []JobIssue    `json:"warnings,omitempty"`
	Error          *ErrorBody    `json:"error,omitempty"`
}

type HeartbeatRequest struct {
	Status         string `json:"status"`
	Running        int    `json:"running"`
	MaxConcurrency *int   `json:"max_concurrency,omitempty"`
	InstanceKey    string `json:"instance_key,omitempty"` // WS 可选回显
}

type HeartbeatResponse struct {
	ServerTime     string `json:"server_time"`
	InstanceStatus string `json:"instance_status"`
}

type UnregisterRequest struct {
	Reason      string `json:"reason,omitempty"`
	InstanceKey string `json:"instance_key,omitempty"`
}
