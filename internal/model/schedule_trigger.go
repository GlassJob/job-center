package model

import "time"

const (
	TriggerPending   = "PENDING"
	TriggerClaimed   = "CLAIMED"
	TriggerFired     = "FIRED"
	TriggerSkipped   = "SKIPPED"
	TriggerCancelled = "CANCELLED"
)

type ScheduleTrigger struct {
	ID          string     `gorm:"column:id;primaryKey"`
	ScheduleID  string     `gorm:"column:schedule_id"`
	JobID       string     `gorm:"column:job_id"`
	FireAt      time.Time  `gorm:"column:fire_at"`
	Status      string     `gorm:"column:status"`
	ClaimedBy   string     `gorm:"column:claimed_by"`
	ClaimedAt   *time.Time `gorm:"column:claimed_at"`
	ClaimUntil  *time.Time `gorm:"column:claim_until"`
	ExecutionID *string    `gorm:"column:execution_id"`
	SkipReason  string     `gorm:"column:skip_reason"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
	UpdatedAt   time.Time  `gorm:"column:updated_at"`
}

func (ScheduleTrigger) TableName() string { return "schedule_triggers" }

// ScheduleTriggerView Admin 触点列表视图（含服务/任务/排班摘要）。
type ScheduleTriggerView struct {
	ID           string     `json:"id"`
	ScheduleID   string     `json:"schedule_id"`
	ScheduleCode string     `json:"schedule_code,omitempty"`
	ScheduleName string     `json:"schedule_name,omitempty"`
	JobID        string     `json:"job_id"`
	JobCode      string     `json:"job_code,omitempty"`
	JobName      string     `json:"job_name,omitempty"`
	ServiceID    string     `json:"service_id,omitempty"`
	ServiceCode  string     `json:"service_code,omitempty"`
	ServiceName  string     `json:"service_name,omitempty"`
	FireAt       time.Time  `json:"fire_at"`
	Status       string     `json:"status"`
	ClaimedBy    string     `json:"claimed_by,omitempty"`
	ClaimedAt    *time.Time `json:"claimed_at,omitempty"`
	ClaimUntil   *time.Time `json:"claim_until,omitempty"`
	ExecutionID  *string    `json:"execution_id,omitempty"`
	SkipReason   string     `json:"skip_reason,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}
