package model

import "time"

const (
	InstanceStatusRegistering = "REGISTERING"
	InstanceStatusOnline      = "ONLINE"
	InstanceStatusSuspect     = "SUSPECT"
	InstanceStatusOffline     = "OFFLINE"
	InstanceStatusDraining    = "DRAINING"
)

type Instance struct {
	ID              string         `gorm:"column:id;primaryKey"`
	ServiceID       string         `gorm:"column:service_id"`
	InstanceKey     string         `gorm:"column:instance_key"`
	SessionID       string         `gorm:"column:session_id"`
	SessionEpoch    uint64         `gorm:"column:session_epoch"`
	Environment     string         `gorm:"column:environment"`
	Hostname        string         `gorm:"column:hostname"`
	IPAddress       string         `gorm:"column:ip_address"`
	ReportedIP      string         `gorm:"column:reported_ip"`
	PID             *uint          `gorm:"column:pid"`
	Language        string         `gorm:"column:language"`
	RuntimeVersion  string         `gorm:"column:runtime_version"`
	SDKVersion      string         `gorm:"column:sdk_version"`
	ProtocolVersion string         `gorm:"column:protocol_version"`
	AppVersion      string         `gorm:"column:app_version"`
	GitCommit       string         `gorm:"column:git_commit"`
	BuildTime       *time.Time     `gorm:"column:build_time"`
	StartedAt       time.Time      `gorm:"column:started_at"`
	RegisteredAt    time.Time      `gorm:"column:registered_at"`
	ConnectedAt     *time.Time     `gorm:"column:connected_at"`
	DisconnectedAt  *time.Time     `gorm:"column:disconnected_at"`
	LastHeartbeatAt *time.Time     `gorm:"column:last_heartbeat_at"`
	Status          string         `gorm:"column:status"`
	MaxConcurrency  uint           `gorm:"column:max_concurrency"`
	RunningCount    uint           `gorm:"column:running_count"`
	GatewayNodeID   string         `gorm:"column:gateway_node_id"`
	ConnectionID    string         `gorm:"column:connection_id"`
	Metadata        map[string]any `gorm:"column:metadata;serializer:json"`
	CreatedAt       time.Time      `gorm:"column:created_at"`
	UpdatedAt       time.Time      `gorm:"column:updated_at"`
}

func (Instance) TableName() string { return "instances" }

type InstanceJobView struct {
	JobID          string    `json:"job_id"`
	JobCode        string    `json:"job_code"`
	HandlerVersion string    `json:"handler_version"`
	MaxConcurrency *uint     `json:"max_concurrency,omitempty"`
	RegisteredAt   time.Time `json:"registered_at"`
}

type InstanceView struct {
	ID              string            `json:"id"`
	ServiceID       string            `json:"service_id"`
	ServiceCode     string            `json:"service_code,omitempty"`
	ServiceName     string            `json:"service_name,omitempty"`
	InstanceKey     string            `json:"instance_key"`
	SessionID       string            `json:"session_id,omitempty"`
	SessionEpoch    uint64            `json:"session_epoch"`
	Environment     string            `json:"environment,omitempty"`
	Hostname        string            `json:"hostname,omitempty"`
	IPAddress       string            `json:"ip_address,omitempty"`
	ReportedIP      string            `json:"reported_ip,omitempty"`
	PID             *uint             `json:"pid,omitempty"`
	Language        string            `json:"language,omitempty"`
	RuntimeVersion  string            `json:"runtime_version,omitempty"`
	SDKVersion      string            `json:"sdk_version,omitempty"`
	ProtocolVersion string            `json:"protocol_version,omitempty"`
	AppVersion      string            `json:"app_version,omitempty"`
	GitCommit       string            `json:"git_commit,omitempty"`
	BuildTime       *time.Time        `json:"build_time,omitempty"`
	StartedAt       time.Time         `json:"started_at"`
	RegisteredAt    time.Time         `json:"registered_at"`
	ConnectedAt     *time.Time        `json:"connected_at,omitempty"`
	DisconnectedAt  *time.Time        `json:"disconnected_at,omitempty"`
	LastHeartbeatAt *time.Time        `json:"last_heartbeat_at,omitempty"`
	Status          string            `json:"status"`
	MaxConcurrency  uint              `json:"max_concurrency"`
	RunningCount    uint              `json:"running_count"`
	GatewayNodeID   string            `json:"gateway_node_id,omitempty"`
	Metadata        map[string]any    `json:"metadata,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	Jobs            []InstanceJobView `json:"jobs,omitempty"`
}

func (i Instance) View(jobs []InstanceJobView) InstanceView {
	meta := i.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	if jobs == nil {
		jobs = []InstanceJobView{}
	}
	return InstanceView{
		ID: i.ID, ServiceID: i.ServiceID, InstanceKey: i.InstanceKey,
		SessionID: i.SessionID, SessionEpoch: i.SessionEpoch, Environment: i.Environment,
		Hostname: i.Hostname, IPAddress: i.IPAddress, ReportedIP: i.ReportedIP, PID: i.PID,
		Language: i.Language, RuntimeVersion: i.RuntimeVersion, SDKVersion: i.SDKVersion,
		ProtocolVersion: i.ProtocolVersion, AppVersion: i.AppVersion, GitCommit: i.GitCommit,
		BuildTime: i.BuildTime, StartedAt: i.StartedAt, RegisteredAt: i.RegisteredAt,
		ConnectedAt: i.ConnectedAt, DisconnectedAt: i.DisconnectedAt, LastHeartbeatAt: i.LastHeartbeatAt,
		Status: i.Status, MaxConcurrency: i.MaxConcurrency, RunningCount: i.RunningCount,
		GatewayNodeID: i.GatewayNodeID, Metadata: meta, CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt,
		Jobs: jobs,
	}
}

type InstanceJob struct {
	ID              string    `gorm:"column:id;primaryKey"`
	InstanceID      string    `gorm:"column:instance_id"`
	JobID           string    `gorm:"column:job_id"`
	HandlerVersion  string    `gorm:"column:handler_version"`
	HandlerMajor    uint      `gorm:"column:handler_major"`
	SchemaHash      string    `gorm:"column:schema_hash"`
	MaxConcurrency  *uint     `gorm:"column:max_concurrency"`
	RegisteredAt    time.Time `gorm:"column:registered_at"`
}

func (InstanceJob) TableName() string { return "instance_jobs" }

type JobVersion struct {
	ID                 string         `gorm:"column:id;primaryKey"`
	JobID              string         `gorm:"column:job_id"`
	HandlerVersion     string         `gorm:"column:handler_version"`
	HandlerMajor       uint           `gorm:"column:handler_major"`
	SchemaHash         string         `gorm:"column:schema_hash"`
	ParamsSchema       map[string]any `gorm:"column:params_schema;serializer:json"`
	IdempotencyMode    string         `gorm:"column:idempotency_mode"`
	CapabilitySnapshot map[string]any `gorm:"column:capability_snapshot;serializer:json"`
	FirstSeenAt        time.Time      `gorm:"column:first_seen_at"`
	LastSeenAt         time.Time      `gorm:"column:last_seen_at"`
}

func (JobVersion) TableName() string { return "job_versions" }
