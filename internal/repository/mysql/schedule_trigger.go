package mysql

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/GlassJob/job-center/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ListTriggersFilter Admin 触点列表过滤。
type ListTriggersFilter struct {
	ScheduleID string
	JobID      string
	ServiceID  string
	Status     string
	FireFrom   *time.Time
	FireTo     *time.Time
	Page       int
	Limit      int
}

// TriggerListRow JOIN 后的触点行。
type TriggerListRow struct {
	model.ScheduleTrigger
	ScheduleCode string `gorm:"column:schedule_code"`
	ScheduleName string `gorm:"column:schedule_name"`
	JobCode      string `gorm:"column:job_code"`
	JobName      string `gorm:"column:job_name"`
	ServiceID    string `gorm:"column:service_id"`
	ServiceCode  string `gorm:"column:service_code"`
	ServiceName  string `gorm:"column:service_name"`
}

func (r TriggerListRow) View() model.ScheduleTriggerView {
	return model.ScheduleTriggerView{
		ID:           r.ID,
		ScheduleID:   r.ScheduleID,
		ScheduleCode: r.ScheduleCode,
		ScheduleName: r.ScheduleName,
		JobID:        r.JobID,
		JobCode:      r.JobCode,
		JobName:      r.JobName,
		ServiceID:    r.ServiceID,
		ServiceCode:  r.ServiceCode,
		ServiceName:  r.ServiceName,
		FireAt:       r.FireAt,
		Status:       r.Status,
		ClaimedBy:    r.ClaimedBy,
		ClaimedAt:    r.ClaimedAt,
		ClaimUntil:   r.ClaimUntil,
		ExecutionID:  r.ExecutionID,
		SkipReason:   r.SkipReason,
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
	}
}

func (s *Store) listTriggersQuery(ctx context.Context, f ListTriggersFilter) *gorm.DB {
	q := s.db.WithContext(ctx).Table("schedule_triggers AS t").
		Joins("LEFT JOIN schedules AS sch ON sch.id = t.schedule_id").
		Joins("LEFT JOIN jobs AS j ON j.id = t.job_id").
		Joins("LEFT JOIN services AS svc ON svc.id = j.service_id")
	if id := strings.TrimSpace(f.ScheduleID); id != "" {
		q = q.Where("t.schedule_id = ?", id)
	}
	if id := strings.TrimSpace(f.JobID); id != "" {
		q = q.Where("t.job_id = ?", id)
	}
	if id := strings.TrimSpace(f.ServiceID); id != "" {
		q = q.Where("j.service_id = ?", id)
	}
	if st := strings.TrimSpace(f.Status); st != "" {
		q = q.Where("t.status = ?", st)
	}
	if f.FireFrom != nil {
		q = q.Where("t.fire_at >= ?", *f.FireFrom)
	}
	if f.FireTo != nil {
		q = q.Where("t.fire_at < ?", *f.FireTo)
	}
	return q
}

func (s *Store) ListTriggers(ctx context.Context, f ListTriggersFilter) ([]TriggerListRow, int64, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Limit < 1 {
		f.Limit = 20
	}
	if f.Limit > 100 {
		f.Limit = 100
	}
	var total int64
	if err := s.listTriggersQuery(ctx, f).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var list []TriggerListRow
	err := s.listTriggersQuery(ctx, f).
		Select(`t.*,
			COALESCE(sch.code,'') AS schedule_code, COALESCE(sch.name,'') AS schedule_name,
			COALESCE(j.code,'') AS job_code, COALESCE(j.name,'') AS job_name,
			COALESCE(j.service_id,'') AS service_id,
			COALESCE(svc.code,'') AS service_code, COALESCE(svc.name,'') AS service_name`).
		Order("t.fire_at ASC, t.id ASC").
		Limit(f.Limit).Offset((f.Page - 1) * f.Limit).
		Scan(&list).Error
	if err != nil {
		return nil, 0, err
	}
	if list == nil {
		list = []TriggerListRow{}
	}
	return list, total, nil
}

func (s *Store) ListEnabledSchedulesWithCron(ctx context.Context) ([]model.Schedule, error) {
	var list []model.Schedule
	err := s.db.WithContext(ctx).
		Where("enabled = ? AND cron_expression IS NOT NULL AND cron_expression <> ?", true, "").
		Find(&list).Error
	return list, err
}

func (s *Store) GetScheduleByID(ctx context.Context, id string) (*model.Schedule, error) {
	var sch model.Schedule
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&sch).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sch, nil
}

func (s *Store) UpdateScheduleHorizon(ctx context.Context, id string, horizonAt time.Time, nextFireAt *time.Time) error {
	fields := map[string]any{"horizon_at": horizonAt}
	if nextFireAt != nil {
		fields["next_fire_at"] = *nextFireAt
	}
	return s.db.WithContext(ctx).Model(&model.Schedule{}).Where("id = ?", id).Updates(fields).Error
}

func (s *Store) TouchScheduleLastFire(ctx context.Context, id string, lastFireAt time.Time, nextFireAt *time.Time) error {
	fields := map[string]any{"last_fire_at": lastFireAt}
	if nextFireAt != nil {
		fields["next_fire_at"] = *nextFireAt
	}
	return s.db.WithContext(ctx).Model(&model.Schedule{}).Where("id = ?", id).Updates(fields).Error
}

func (s *Store) ClearScheduleHorizon(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Model(&model.Schedule{}).
		Where("id = ?", id).
		Updates(map[string]any{"horizon_at": nil}).Error
}

func (s *Store) InsertTriggersIgnore(ctx context.Context, triggers []model.ScheduleTrigger) error {
	if len(triggers) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		CreateInBatches(&triggers, 100).Error
}

func (s *Store) NextPendingFireAt(ctx context.Context, scheduleID string, after time.Time) (*time.Time, error) {
	var t model.ScheduleTrigger
	err := s.db.WithContext(ctx).
		Where("schedule_id = ? AND status = ? AND fire_at >= ?", scheduleID, model.TriggerPending, after).
		Order("fire_at ASC").
		Limit(1).
		Take(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t.FireAt, nil
}

// EarliestPendingFireAt 全局最早 PENDING 触点；无则 nil。
func (s *Store) EarliestPendingFireAt(ctx context.Context) (*time.Time, error) {
	var t model.ScheduleTrigger
	err := s.db.WithContext(ctx).
		Where("status = ?", model.TriggerPending).
		Order("fire_at ASC").
		Limit(1).
		Take(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t.FireAt, nil
}

// ClaimDueTriggers 用 SKIP LOCKED 领取到期 PENDING triggers。
func (s *Store) ClaimDueTriggers(ctx context.Context, nodeID string, now time.Time, limit int, claimTTL time.Duration) ([]model.ScheduleTrigger, error) {
	if limit < 1 {
		limit = 50
	}
	if claimTTL <= 0 {
		claimTTL = 30 * time.Second
	}
	var claimed []model.ScheduleTrigger
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []model.ScheduleTrigger
		err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("status = ? AND fire_at <= ?", model.TriggerPending, now).
			Order("fire_at ASC").
			Limit(limit).
			Find(&rows).Error
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		ids := make([]string, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		until := now.Add(claimTTL)
		if err := tx.Model(&model.ScheduleTrigger{}).
			Where("id IN ? AND status = ?", ids, model.TriggerPending).
			Updates(map[string]any{
				"status":      model.TriggerClaimed,
				"claimed_by":  nodeID,
				"claimed_at":  now,
				"claim_until": until,
			}).Error; err != nil {
			return err
		}
		return tx.Where("id IN ?", ids).Find(&claimed).Error
	})
	return claimed, err
}

func (s *Store) MarkTriggerFired(ctx context.Context, id, executionID string) error {
	return s.db.WithContext(ctx).Model(&model.ScheduleTrigger{}).
		Where("id = ? AND status = ?", id, model.TriggerClaimed).
		Updates(map[string]any{
			"status":       model.TriggerFired,
			"execution_id": executionID,
		}).Error
}

func (s *Store) MarkTriggerSkipped(ctx context.Context, id, reason string) error {
	return s.db.WithContext(ctx).Model(&model.ScheduleTrigger{}).
		Where("id = ? AND status IN ?", id, []string{model.TriggerClaimed, model.TriggerPending}).
		Updates(map[string]any{
			"status":      model.TriggerSkipped,
			"skip_reason": reason,
		}).Error
}

// DeleteFuturePendingTriggers 删除 fire_at > after 的 PENDING，供重生「先删再插」。
// 同时删掉同窗口 CLAIMED / CANCELLED，避免占住 uk_schedule_fire 导致插不进去。
func (s *Store) DeleteFuturePendingTriggers(ctx context.Context, scheduleID string, after time.Time) (int64, error) {
	res := s.db.WithContext(ctx).
		Where("schedule_id = ? AND fire_at >= ? AND status IN ?",
			scheduleID,
			after,
			[]string{model.TriggerPending, model.TriggerClaimed, model.TriggerCancelled},
		).
		Delete(&model.ScheduleTrigger{})
	return res.RowsAffected, res.Error
}

// CancelFutureTriggers 保留给需要软作废的场景；排班重生请用 DeleteFuturePendingTriggers。
func (s *Store) CancelFutureTriggers(ctx context.Context, scheduleID string, now time.Time, reason string) (int64, error) {
	res := s.db.WithContext(ctx).Model(&model.ScheduleTrigger{}).
		Where("schedule_id = ? AND status IN ? AND fire_at > ?",
			scheduleID,
			[]string{model.TriggerPending, model.TriggerClaimed},
			now,
		).
		Updates(map[string]any{
			"status":      model.TriggerCancelled,
			"skip_reason": reason,
		})
	return res.RowsAffected, res.Error
}

func (s *Store) CancelAllOpenTriggers(ctx context.Context, scheduleID, reason string) (int64, error) {
	res := s.db.WithContext(ctx).Model(&model.ScheduleTrigger{}).
		Where("schedule_id = ? AND status IN ?",
			scheduleID,
			[]string{model.TriggerPending, model.TriggerClaimed},
		).
		Updates(map[string]any{
			"status":      model.TriggerCancelled,
			"skip_reason": reason,
		})
	return res.RowsAffected, res.Error
}
