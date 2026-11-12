package mysql

import (
	"context"
	"time"

	"github.com/GlassJob/job-center/internal/model"
)

func (s *Store) GetOverview(ctx context.Context) (*model.Overview, error) {
	out := &model.Overview{
		Upcoming:     []model.OverviewUpcomingItem{},
		RecentFailed: []model.OverviewFailedItem{},
	}
	db := s.db.WithContext(ctx)

	if err := db.Model(&model.Service{}).Count(&out.ServiceCount).Error; err != nil {
		return nil, err
	}
	if err := db.Model(&model.Job{}).Count(&out.JobCount).Error; err != nil {
		return nil, err
	}
	if err := db.Model(&model.Job{}).Where("enabled = ?", true).Count(&out.EnabledJobCount).Error; err != nil {
		return nil, err
	}

	type statusCount struct {
		Status string `gorm:"column:status"`
		Cnt    int64  `gorm:"column:cnt"`
	}
	var instRows []statusCount
	if err := db.Model(&model.Instance{}).Select("status, COUNT(*) AS cnt").Group("status").Scan(&instRows).Error; err != nil {
		return nil, err
	}
	for _, r := range instRows {
		switch r.Status {
		case model.InstanceStatusRegistering:
			out.InstanceCounts.Registering = r.Cnt
		case model.InstanceStatusOnline:
			out.InstanceCounts.Online = r.Cnt
		case model.InstanceStatusSuspect:
			out.InstanceCounts.Suspect = r.Cnt
		case model.InstanceStatusOffline:
			out.InstanceCounts.Offline = r.Cnt
		case model.InstanceStatusDraining:
			out.InstanceCounts.Draining = r.Cnt
		}
	}

	now := time.Now().UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	dayEnd := dayStart.Add(24 * time.Hour)

	if err := db.Model(&model.Execution{}).
		Where("created_at >= ? AND created_at < ?", dayStart, dayEnd).
		Count(&out.ExecutionToday.Total).Error; err != nil {
		return nil, err
	}
	if err := db.Model(&model.Execution{}).
		Where("created_at >= ? AND created_at < ? AND status IN ?", dayStart, dayEnd,
			[]string{model.ExecStatusPending, model.ExecStatusQueued, model.ExecStatusDispatching, model.ExecStatusRunning, model.ExecStatusRetryWait}).
		Count(&out.ExecutionToday.Running).Error; err != nil {
		return nil, err
	}
	if err := db.Model(&model.Execution{}).
		Where("created_at >= ? AND created_at < ? AND status = ?", dayStart, dayEnd, model.ExecStatusSucceeded).
		Count(&out.ExecutionToday.Success).Error; err != nil {
		return nil, err
	}
	if err := db.Model(&model.Execution{}).
		Where("created_at >= ? AND created_at < ? AND status IN ?", dayStart, dayEnd,
			[]string{model.ExecStatusFailed, model.ExecStatusTimedOut}).
		Count(&out.ExecutionToday.Failed).Error; err != nil {
		return nil, err
	}
	if err := db.Model(&model.Execution{}).
		Where("created_at >= ? AND created_at < ? AND status = ?", dayStart, dayEnd, model.ExecStatusCancelled).
		Count(&out.ExecutionToday.Canceled).Error; err != nil {
		return nil, err
	}

	if err := db.Raw(`
		SELECT sch.id AS schedule_id, sch.code AS schedule_code, sch.name AS schedule_name,
			sch.cron_expression, sch.timezone, sch.next_fire_at,
			j.id AS job_id, j.code AS job_code, j.name AS job_name,
			svc.id AS service_id, svc.code AS service_code, svc.name AS service_name
		FROM schedules sch
		INNER JOIN jobs j ON j.id = sch.job_id
		INNER JOIN services svc ON svc.id = j.service_id
		WHERE sch.enabled = 1 AND sch.next_fire_at IS NOT NULL
		ORDER BY sch.next_fire_at ASC
		LIMIT 8`).Scan(&out.Upcoming).Error; err != nil {
		return nil, err
	}
	if out.Upcoming == nil {
		out.Upcoming = []model.OverviewUpcomingItem{}
	}

	if err := db.Raw(`
		SELECT e.id AS execution_id, e.status, e.error_code, e.error_message, e.finished_at,
			e.job_id, e.job_code, COALESCE(j.name,'') AS job_name,
			e.service_id, COALESCE(svc.code,'') AS service_code, COALESCE(svc.name,'') AS service_name
		FROM executions e
		LEFT JOIN jobs j ON j.id = e.job_id
		LEFT JOIN services svc ON svc.id = e.service_id
		WHERE e.status IN (?, ?)
		ORDER BY COALESCE(e.finished_at, e.updated_at) DESC
		LIMIT 10`, model.ExecStatusFailed, model.ExecStatusTimedOut).Scan(&out.RecentFailed).Error; err != nil {
		return nil, err
	}
	if out.RecentFailed == nil {
		out.RecentFailed = []model.OverviewFailedItem{}
	}
	return out, nil
}
