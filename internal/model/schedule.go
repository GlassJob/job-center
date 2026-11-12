package model

import "time"

const (
	ScheduleConfigSourceDefault = "DEFAULT"
	ScheduleConfigSourceAdmin   = "ADMIN"

	ConcurrencyAllow  = "ALLOW"
	ConcurrencyForbid = "FORBID"
	ConcurrencyQueue  = "QUEUE"

	ConcurrencyScopeJob            = "JOB"
	ConcurrencyScopeSchedule       = "SCHEDULE"
	ConcurrencyScopeIdempotencyKey = "IDEMPOTENCY_KEY"

	MisfireSkip     = "SKIP"
	MisfireFireOnce = "FIRE_ONCE"
	MisfireCatchUp  = "CATCH_UP"

	DispatchLeastLoad  = "LEAST_LOAD"
	DispatchRoundRobin = "ROUND_ROBIN"
	DispatchRandom     = "RANDOM"
)

type Schedule struct {
	ID                      string         `gorm:"column:id;primaryKey"`
	JobID                   string         `gorm:"column:job_id"`
	Code                    string         `gorm:"column:code"`
	Name                    string         `gorm:"column:name"`
	Enabled                 bool           `gorm:"column:enabled"`
	ManualEnabled           bool           `gorm:"column:manual_enabled"`
	CronExpression          string         `gorm:"column:cron_expression"`
	Timezone                string         `gorm:"column:timezone"`
	DefaultParams           map[string]any `gorm:"column:default_params;serializer:json"`
	TimeoutSeconds          uint           `gorm:"column:timeout_seconds"`
	MaxRetries              uint           `gorm:"column:max_retries"`
	RetryBackoff            map[string]any `gorm:"column:retry_backoff;serializer:json"`
	ConcurrencyPolicy       string         `gorm:"column:concurrency_policy"`
	ConcurrencyScope        string         `gorm:"column:concurrency_scope"`
	MaxConcurrency          uint           `gorm:"column:max_concurrency"`
	QueueMax                uint           `gorm:"column:queue_max"`
	MisfirePolicy           string         `gorm:"column:misfire_policy"`
	MisfireThresholdSeconds uint           `gorm:"column:misfire_threshold_seconds"`
	CatchUpMax              uint           `gorm:"column:catch_up_max"`
	DispatchStrategy        string         `gorm:"column:dispatch_strategy"`
	NextFireAt              *time.Time     `gorm:"column:next_fire_at"`
	LastFireAt              *time.Time     `gorm:"column:last_fire_at"`
	HorizonAt               *time.Time     `gorm:"column:horizon_at"`
	ConfigSource            string         `gorm:"column:config_source"`
	Version                 uint           `gorm:"column:version"`
	UpdatedBy               string         `gorm:"column:updated_by"`
	CreatedAt               time.Time      `gorm:"column:created_at"`
	UpdatedAt               time.Time      `gorm:"column:updated_at"`
}

func (Schedule) TableName() string { return "schedules" }

type ScheduleView struct {
	ID                      string         `json:"id"`
	JobID                   string         `json:"job_id"`
	Code                    string         `json:"code"`
	Name                    string         `json:"name"`
	Enabled                 bool           `json:"enabled"`
	ManualEnabled           bool           `json:"manual_enabled"`
	CronExpression          string         `json:"cron_expression,omitempty"`
	Timezone                string         `json:"timezone"`
	DefaultParams           map[string]any `json:"default_params,omitempty"`
	TimeoutSeconds          uint           `json:"timeout_seconds"`
	MaxRetries              uint           `json:"max_retries"`
	RetryBackoff            map[string]any `json:"retry_backoff"`
	ConcurrencyPolicy       string         `json:"concurrency_policy"`
	ConcurrencyScope        string         `json:"concurrency_scope"`
	MaxConcurrency          uint           `json:"max_concurrency"`
	QueueMax                uint           `json:"queue_max"`
	MisfirePolicy           string         `json:"misfire_policy"`
	MisfireThresholdSeconds uint           `json:"misfire_threshold_seconds"`
	CatchUpMax              uint           `json:"catch_up_max"`
	DispatchStrategy        string         `json:"dispatch_strategy"`
	NextFireAt              *time.Time     `json:"next_fire_at,omitempty"`
	LastFireAt              *time.Time     `json:"last_fire_at,omitempty"`
	HorizonAt               *time.Time     `json:"horizon_at,omitempty"`
	ConfigSource            string         `json:"config_source"`
	Version                 uint           `json:"version"`
	UpdatedBy               string         `json:"updated_by,omitempty"`
	CreatedAt               time.Time      `json:"created_at"`
	UpdatedAt               time.Time      `json:"updated_at"`
}

func (s *Schedule) View() ScheduleView {
	backoff := s.RetryBackoff
	if backoff == nil {
		backoff = map[string]any{"type": "fixed", "interval_seconds": 60}
	}
	return ScheduleView{
		ID:                      s.ID,
		JobID:                   s.JobID,
		Code:                    s.Code,
		Name:                    s.Name,
		Enabled:                 s.Enabled,
		ManualEnabled:           s.ManualEnabled,
		CronExpression:          s.CronExpression,
		Timezone:                s.Timezone,
		DefaultParams:           s.DefaultParams,
		TimeoutSeconds:          s.TimeoutSeconds,
		MaxRetries:              s.MaxRetries,
		RetryBackoff:            backoff,
		ConcurrencyPolicy:       s.ConcurrencyPolicy,
		ConcurrencyScope:        s.ConcurrencyScope,
		MaxConcurrency:          s.MaxConcurrency,
		QueueMax:                s.QueueMax,
		MisfirePolicy:           s.MisfirePolicy,
		MisfireThresholdSeconds: s.MisfireThresholdSeconds,
		CatchUpMax:              s.CatchUpMax,
		DispatchStrategy:        s.DispatchStrategy,
		NextFireAt:              s.NextFireAt,
		LastFireAt:              s.LastFireAt,
		HorizonAt:               s.HorizonAt,
		ConfigSource:            s.ConfigSource,
		Version:                 s.Version,
		UpdatedBy:               s.UpdatedBy,
		CreatedAt:               s.CreatedAt,
		UpdatedAt:               s.UpdatedAt,
	}
}
