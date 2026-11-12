package mysql

import (
	"context"
	"errors"
	"strings"

	"github.com/GlassJob/job-center/internal/model"
	"gorm.io/gorm"
)

func (s *Store) CreateJob(ctx context.Context, j *model.Job) error {
	if j.Capabilities == nil {
		j.Capabilities = map[string]any{}
	}
	if j.IdempotencyMode == "" {
		j.IdempotencyMode = model.IdempotencyModeUnknown
	}
	if j.DefaultTimeoutSeconds == 0 {
		j.DefaultTimeoutSeconds = 300
	}
	if j.Status == "" {
		j.Status = model.JobStatusActive
	}
	err := s.db.WithContext(ctx).Create(j).Error
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return ErrConflict
	}
	return err
}

func (s *Store) GetJob(ctx context.Context, serviceID, id string) (*model.Job, error) {
	var j model.Job
	err := s.db.WithContext(ctx).Where("id = ? AND service_id = ?", id, serviceID).First(&j).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

func (s *Store) GetJobByID(ctx context.Context, id string) (*model.Job, error) {
	var j model.Job
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&j).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

func (s *Store) ExistsJobCode(ctx context.Context, serviceID, code, excludeID string) (bool, error) {
	q := s.db.WithContext(ctx).Model(&model.Job{}).Where("service_id = ? AND code = ?", serviceID, code)
	if excludeID != "" {
		q = q.Where("id <> ?", excludeID)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}

// JobListRow 任务列表（可带服务展示字段）。
type JobListRow struct {
	model.Job
	ServiceCode string `gorm:"column:service_code"`
	ServiceName string `gorm:"column:service_name"`
}

func (s *Store) ListJobs(ctx context.Context, serviceID, keyword, status string, enabled *bool, page, limit int) ([]JobListRow, int64, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	countQ := s.db.WithContext(ctx).Table("jobs AS j")
	if sid := strings.TrimSpace(serviceID); sid != "" {
		countQ = countQ.Where("j.service_id = ?", sid)
	}
	if kw := strings.TrimSpace(keyword); kw != "" {
		like := "%" + kw + "%"
		countQ = countQ.Where(
			"j.id LIKE ? OR j.code LIKE ? OR j.name LIKE ? OR j.handler LIKE ? OR EXISTS (SELECT 1 FROM services s WHERE s.id = j.service_id AND (s.code LIKE ? OR s.name LIKE ?))",
			like, like, like, like, like, like,
		)
	}
	if st := strings.TrimSpace(status); st != "" {
		countQ = countQ.Where("j.status = ?", st)
	}
	if enabled != nil {
		countQ = countQ.Where("j.enabled = ?", *enabled)
	}
	var total int64
	if err := countQ.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	q := s.db.WithContext(ctx).Table("jobs AS j").
		Select("j.*, COALESCE(s.code,'') AS service_code, COALESCE(s.name,'') AS service_name").
		Joins("LEFT JOIN services AS s ON s.id = j.service_id")
	if sid := strings.TrimSpace(serviceID); sid != "" {
		q = q.Where("j.service_id = ?", sid)
	}
	if kw := strings.TrimSpace(keyword); kw != "" {
		like := "%" + kw + "%"
		q = q.Where(
			"j.id LIKE ? OR j.code LIKE ? OR j.name LIKE ? OR j.handler LIKE ? OR s.code LIKE ? OR s.name LIKE ?",
			like, like, like, like, like, like,
		)
	}
	if st := strings.TrimSpace(status); st != "" {
		q = q.Where("j.status = ?", st)
	}
	if enabled != nil {
		q = q.Where("j.enabled = ?", *enabled)
	}
	var list []JobListRow
	err := q.Order("j.created_at DESC").Limit(limit).Offset((page - 1) * limit).Scan(&list).Error
	if list == nil {
		list = []JobListRow{}
	}
	return list, total, err
}

func (s *Store) ListSchedulesByJobIDs(ctx context.Context, jobIDs []string) ([]model.Schedule, error) {
	if len(jobIDs) == 0 {
		return []model.Schedule{}, nil
	}
	var list []model.Schedule
	err := s.db.WithContext(ctx).Where("job_id IN ?", jobIDs).Order("created_at ASC").Find(&list).Error
	if list == nil {
		list = []model.Schedule{}
	}
	return list, err
}

type JobPatch struct {
	Name                  *string
	Description           *string
	Enabled               *bool
	IdempotencyVerified   *bool
	DefaultTimeoutSeconds *uint
	Status                *string
	BumpMetadataVersion   bool
}

func (s *Store) PatchJob(ctx context.Context, serviceID, id string, p JobPatch) (*model.Job, error) {
	updates := map[string]any{}
	if p.Name != nil {
		updates["name"] = *p.Name
	}
	if p.Description != nil {
		if *p.Description == "" {
			updates["description"] = nil
		} else {
			updates["description"] = *p.Description
		}
	}
	if p.Enabled != nil {
		updates["enabled"] = *p.Enabled
	}
	if p.IdempotencyVerified != nil {
		updates["idempotency_verified"] = *p.IdempotencyVerified
	}
	if p.DefaultTimeoutSeconds != nil {
		updates["default_timeout_seconds"] = *p.DefaultTimeoutSeconds
	}
	if p.Status != nil {
		updates["status"] = *p.Status
	}
	if len(updates) == 0 {
		return s.GetJob(ctx, serviceID, id)
	}
	if p.BumpMetadataVersion {
		updates["metadata_version"] = gorm.Expr("metadata_version + 1")
	}
	res := s.db.WithContext(ctx).Model(&model.Job{}).
		Where("id = ? AND service_id = ?", id, serviceID).Updates(updates)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		if _, err := s.GetJob(ctx, serviceID, id); err != nil {
			return nil, err
		}
	}
	return s.GetJob(ctx, serviceID, id)
}

func (s *Store) SoftDisableJob(ctx context.Context, serviceID, id string) (*model.Job, error) {
	st := model.JobStatusDisabled
	return s.PatchJob(ctx, serviceID, id, JobPatch{Status: &st, BumpMetadataVersion: true})
}

type JobDependents struct {
	Versions     int64 `gorm:"column:versions"`
	Schedules    int64 `gorm:"column:schedules"`
	States       int64 `gorm:"column:states"`
	InstanceJobs int64 `gorm:"column:instance_jobs"`
	Executions   int64 `gorm:"column:executions"`
}

func (d JobDependents) Total() int64 {
	return d.Versions + d.Schedules + d.States + d.InstanceJobs + d.Executions
}

func (s *Store) CountJobDependents(ctx context.Context, jobID string) (JobDependents, error) {
	var r JobDependents
	err := s.db.WithContext(ctx).Raw(`
		SELECT
			(SELECT COUNT(*) FROM job_versions WHERE job_id = ?) AS versions,
			(SELECT COUNT(*) FROM schedules WHERE job_id = ?) AS schedules,
			(SELECT COUNT(*) FROM job_states WHERE job_id = ?) AS states,
			(SELECT COUNT(*) FROM instance_jobs WHERE job_id = ?) AS instance_jobs,
			(SELECT COUNT(*) FROM executions WHERE job_id = ?) AS executions
	`, jobID, jobID, jobID, jobID, jobID).Scan(&r).Error
	return r, err
}

func (s *Store) DeleteJob(ctx context.Context, serviceID, id string) error {
	res := s.db.WithContext(ctx).Where("id = ? AND service_id = ?", id, serviceID).Delete(&model.Job{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
