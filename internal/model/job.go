package model

import "time"

const (
	JobStatusActive   = "ACTIVE"
	JobStatusOrphaned = "ORPHANED"
	JobStatusDisabled = "DISABLED"

	IdempotencyModeUnknown = "UNKNOWN"
	IdempotencyModeNone    = "NONE"
	IdempotencyModeKeyed   = "KEYED"
	IdempotencyModeNatural = "NATURAL"
)

type Job struct {
	ID                    string         `gorm:"column:id;primaryKey"`
	ServiceID             string         `gorm:"column:service_id"`
	Code                  string         `gorm:"column:code"`
	Name                  string         `gorm:"column:name"`
	Description           string         `gorm:"column:description"`
	Enabled               bool           `gorm:"column:enabled"`
	Language              string         `gorm:"column:language"`
	Handler               string         `gorm:"column:handler"`
	HandlerVersion        string         `gorm:"column:handler_version"`
	ParamsSchema          map[string]any `gorm:"column:params_schema;serializer:json"`
	SchemaHash            string         `gorm:"column:schema_hash"`
	MetadataVersion       uint           `gorm:"column:metadata_version"`
	IdempotencyMode       string         `gorm:"column:idempotency_mode"`
	IdempotencyVerified   bool           `gorm:"column:idempotency_verified"`
	SupportsCancel        bool           `gorm:"column:supports_cancel"`
	SupportsTimeout       bool           `gorm:"column:supports_timeout"`
	SupportsProgress      bool           `gorm:"column:supports_progress"`
	SupportsState         bool           `gorm:"column:supports_state"`
	DefaultTimeoutSeconds uint           `gorm:"column:default_timeout_seconds"`
	Capabilities          map[string]any `gorm:"column:capabilities;serializer:json"`
	Status                string         `gorm:"column:status"`
	FirstRegisteredAt     *time.Time     `gorm:"column:first_registered_at"`
	LastRegisteredAt      *time.Time     `gorm:"column:last_registered_at"`
	CreatedAt             time.Time      `gorm:"column:created_at"`
	UpdatedAt             time.Time      `gorm:"column:updated_at"`
}

func (Job) TableName() string { return "jobs" }

type JobView struct {
	ID                    string            `json:"id"`
	ServiceID             string            `json:"service_id"`
	ServiceCode           string            `json:"service_code,omitempty"`
	ServiceName           string            `json:"service_name,omitempty"`
	Code                  string            `json:"code"`
	Name                  string            `json:"name"`
	Description           string            `json:"description,omitempty"`
	Enabled               bool              `json:"enabled"`
	Language              string            `json:"language,omitempty"`
	Handler               string            `json:"handler,omitempty"`
	HandlerVersion        string            `json:"handler_version,omitempty"`
	ParamsSchema          map[string]any    `json:"params_schema,omitempty"`
	SchemaHash            string            `json:"schema_hash,omitempty"`
	MetadataVersion       uint              `json:"metadata_version"`
	IdempotencyMode       string            `json:"idempotency_mode"`
	IdempotencyVerified   bool              `json:"idempotency_verified"`
	SupportsCancel        bool              `json:"supports_cancel"`
	SupportsTimeout       bool              `json:"supports_timeout"`
	SupportsProgress      bool              `json:"supports_progress"`
	SupportsState         bool              `json:"supports_state"`
	DefaultTimeoutSeconds uint              `json:"default_timeout_seconds"`
	Capabilities          map[string]any    `json:"capabilities,omitempty"`
	Status                string            `json:"status"`
	FirstRegisteredAt     *time.Time        `json:"first_registered_at,omitempty"`
	LastRegisteredAt      *time.Time        `json:"last_registered_at,omitempty"`
	CreatedAt             time.Time         `json:"created_at"`
	UpdatedAt             time.Time         `json:"updated_at"`
	Schedules             []ScheduleSummary `json:"schedules,omitempty"`
	RecentExecutions      []ExecutionView   `json:"recent_executions,omitempty"`
	LastExecutionID       *string           `json:"last_execution_id,omitempty"`
	LastExecutionStatus   string            `json:"last_execution_status,omitempty"`
	LastExecutionAt       *time.Time        `json:"last_execution_at,omitempty"`
}

// ScheduleSummary 任务列表嵌套的调度摘要。
type ScheduleSummary struct {
	ID             string     `json:"id"`
	Code           string     `json:"code"`
	Name           string     `json:"name"`
	Enabled        bool       `json:"enabled"`
	CronExpression string     `json:"cron_expression,omitempty"`
	Timezone       string     `json:"timezone,omitempty"`
	NextFireAt     *time.Time `json:"next_fire_at,omitempty"`
	LastFireAt     *time.Time `json:"last_fire_at,omitempty"`
}

func (j *Job) View() JobView {
	caps := j.Capabilities
	if caps == nil {
		caps = map[string]any{}
	}
	return JobView{
		ID:                    j.ID,
		ServiceID:             j.ServiceID,
		Code:                  j.Code,
		Name:                  j.Name,
		Description:           j.Description,
		Enabled:               j.Enabled,
		Language:              j.Language,
		Handler:               j.Handler,
		HandlerVersion:        j.HandlerVersion,
		ParamsSchema:          j.ParamsSchema,
		SchemaHash:            j.SchemaHash,
		MetadataVersion:       j.MetadataVersion,
		IdempotencyMode:       j.IdempotencyMode,
		IdempotencyVerified:   j.IdempotencyVerified,
		SupportsCancel:        j.SupportsCancel,
		SupportsTimeout:       j.SupportsTimeout,
		SupportsProgress:      j.SupportsProgress,
		SupportsState:         j.SupportsState,
		DefaultTimeoutSeconds: j.DefaultTimeoutSeconds,
		Capabilities:          caps,
		Status:                j.Status,
		FirstRegisteredAt:     j.FirstRegisteredAt,
		LastRegisteredAt:      j.LastRegisteredAt,
		CreatedAt:             j.CreatedAt,
		UpdatedAt:             j.UpdatedAt,
	}
}
