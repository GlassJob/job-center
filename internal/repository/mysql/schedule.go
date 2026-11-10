package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/GlassJob/job-center/internal/model"
	"gorm.io/gorm"
)

func (s *Store) CreateSchedule(ctx context.Context, sch *model.Schedule) error {
	if sch.RetryBackoff == nil {
		sch.RetryBackoff = map[string]any{"type": "fixed", "interval_seconds": 60}
	}
	if sch.Timezone == "" {
		sch.Timezone = "UTC"
	}
	if sch.TimeoutSeconds == 0 {
		sch.TimeoutSeconds = 300
	}
	if sch.ConcurrencyPolicy == "" {
		sch.ConcurrencyPolicy = model.ConcurrencyForbid
	}
	if sch.ConcurrencyScope == "" {
		sch.ConcurrencyScope = model.ConcurrencyScopeJob
	}
	if sch.MaxConcurrency == 0 {
		sch.MaxConcurrency = 1
	}
	if sch.QueueMax == 0 {
		sch.QueueMax = 10
	}
	if sch.MisfirePolicy == "" {
		sch.MisfirePolicy = model.MisfireSkip
	}
	if sch.MisfireThresholdSeconds == 0 {
		sch.MisfireThresholdSeconds = 60
	}
	if sch.CatchUpMax == 0 {
		sch.CatchUpMax = 10
	}
	if sch.DispatchStrategy == "" {
		sch.DispatchStrategy = model.DispatchLeastLoad
	}
	if sch.ConfigSource == "" {
		sch.ConfigSource = model.ScheduleConfigSourceAdmin
	}
	err := s.db.WithContext(ctx).Create(sch).Error
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return ErrConflict
	}
	return err
}

func (s *Store) GetSchedule(ctx context.Context, jobID, id string) (*model.Schedule, error) {
	var sch model.Schedule
	err := s.db.WithContext(ctx).Where("id = ? AND job_id = ?", id, jobID).First(&sch).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sch, nil
}

func (s *Store) ExistsScheduleCode(ctx context.Context, jobID, code, excludeID string) (bool, error) {
	q := s.db.WithContext(ctx).Model(&model.Schedule{}).Where("job_id = ? AND code = ?", jobID, code)
	if excludeID != "" {
		q = q.Where("id <> ?", excludeID)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) ListSchedules(ctx context.Context, jobID, keyword string, enabled *bool, page, limit int) ([]model.Schedule, int64, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	q := s.db.WithContext(ctx).Model(&model.Schedule{}).Where("job_id = ?", jobID)
	if kw := strings.TrimSpace(keyword); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("id LIKE ? OR code LIKE ? OR name LIKE ?", like, like, like)
	}
	if enabled != nil {
		q = q.Where("enabled = ?", *enabled)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var list []model.Schedule
	err := q.Order("created_at DESC").Limit(limit).Offset((page - 1) * limit).Find(&list).Error
	return list, total, err
}

type SchedulePatch struct {
	Name                    *string
	Enabled                 *bool
	ManualEnabled           *bool
	CronExpression          *string
	ClearCron               bool
	Timezone                *string
	DefaultParams           *map[string]any
	ClearDefaultParams      bool
	TimeoutSeconds          *uint
	MaxRetries              *uint
	RetryBackoff            *map[string]any
	ConcurrencyPolicy       *string
	ConcurrencyScope        *string
	MaxConcurrency          *uint
	QueueMax                *uint
	MisfirePolicy           *string
	MisfireThresholdSeconds *uint
	CatchUpMax              *uint
	DispatchStrategy        *string
	UpdatedBy               string
	ExpectedVersion         uint
}

func (s *Store) PatchScheduleCAS(ctx context.Context, jobID, id string, p SchedulePatch) (*model.Schedule, error) {
	updates := map[string]any{
		"updated_by": p.UpdatedBy,
		"version":    gorm.Expr("version + 1"),
	}
	if p.Name != nil {
		updates["name"] = *p.Name
	}
	if p.Enabled != nil {
		updates["enabled"] = *p.Enabled
	}
	if p.ManualEnabled != nil {
		updates["manual_enabled"] = *p.ManualEnabled
	}
	if p.ClearCron {
		updates["cron_expression"] = nil
	} else if p.CronExpression != nil {
		if *p.CronExpression == "" {
			updates["cron_expression"] = nil
		} else {
			updates["cron_expression"] = *p.CronExpression
		}
	}
	if p.Timezone != nil {
		updates["timezone"] = *p.Timezone
	}
	if p.ClearDefaultParams {
		updates["default_params"] = nil
	} else if p.DefaultParams != nil {
		raw, err := json.Marshal(*p.DefaultParams)
		if err != nil {
			return nil, err
		}
		updates["default_params"] = json.RawMessage(raw)
	}
	if p.TimeoutSeconds != nil {
		updates["timeout_seconds"] = *p.TimeoutSeconds
	}
	if p.MaxRetries != nil {
		updates["max_retries"] = *p.MaxRetries
	}
	if p.RetryBackoff != nil {
		raw, err := json.Marshal(*p.RetryBackoff)
		if err != nil {
			return nil, err
		}
		updates["retry_backoff"] = json.RawMessage(raw)
	}
	if p.ConcurrencyPolicy != nil {
		updates["concurrency_policy"] = *p.ConcurrencyPolicy
	}
	if p.ConcurrencyScope != nil {
		updates["concurrency_scope"] = *p.ConcurrencyScope
	}
	if p.MaxConcurrency != nil {
		updates["max_concurrency"] = *p.MaxConcurrency
	}
	if p.QueueMax != nil {
		updates["queue_max"] = *p.QueueMax
	}
	if p.MisfirePolicy != nil {
		updates["misfire_policy"] = *p.MisfirePolicy
	}
	if p.MisfireThresholdSeconds != nil {
		updates["misfire_threshold_seconds"] = *p.MisfireThresholdSeconds
	}
	if p.CatchUpMax != nil {
		updates["catch_up_max"] = *p.CatchUpMax
	}
	if p.DispatchStrategy != nil {
		updates["dispatch_strategy"] = *p.DispatchStrategy
	}

	res := s.db.WithContext(ctx).Model(&model.Schedule{}).
		Where("id = ? AND job_id = ? AND version = ?", id, jobID, p.ExpectedVersion).
		Updates(updates)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		if _, err := s.GetSchedule(ctx, jobID, id); err != nil {
			return nil, err
		}
		return nil, ErrConflict
	}
	return s.GetSchedule(ctx, jobID, id)
}

func (s *Store) SoftDisableSchedule(ctx context.Context, jobID, id, updatedBy string, expectedVersion uint) (*model.Schedule, error) {
	enabled := false
	return s.PatchScheduleCAS(ctx, jobID, id, SchedulePatch{
		Enabled:         &enabled,
		UpdatedBy:       updatedBy,
		ExpectedVersion: expectedVersion,
	})
}

func (s *Store) CountScheduleExecutions(ctx context.Context, scheduleID string) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Table("executions").Where("schedule_id = ?", scheduleID).Count(&n).Error
	return n, err
}

func (s *Store) DeleteSchedule(ctx context.Context, jobID, id string) error {
	res := s.db.WithContext(ctx).Where("id = ? AND job_id = ?", id, jobID).Delete(&model.Schedule{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
