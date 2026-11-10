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
