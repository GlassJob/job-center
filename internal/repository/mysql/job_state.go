package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/GlassJob/job-center/internal/model"
	"github.com/GlassJob/job-center/internal/pkg/idgen"
	"gorm.io/gorm"
)

func (s *Store) CreateJobState(ctx context.Context, st *model.JobState) error {
	if st.Value == nil {
		st.Value = map[string]any{}
	}
	if st.Version == 0 {
		st.Version = 1
	}
	err := s.db.WithContext(ctx).Create(st).Error
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return ErrConflict
	}
	return err
}

func (s *Store) GetJobState(ctx context.Context, jobID, id string) (*model.JobState, error) {
	var st model.JobState
	err := s.db.WithContext(ctx).Where("id = ? AND job_id = ?", id, jobID).First(&st).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

func (s *Store) GetJobStateByKey(ctx context.Context, jobID, key string) (*model.JobState, error) {
	var st model.JobState
	err := s.db.WithContext(ctx).Where("job_id = ? AND `key` = ?", jobID, key).First(&st).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// UpsertJobStateByKey 写入执行器运行时状态（key 通常为 default）；存在则 version+1。
func (s *Store) UpsertJobStateByKey(ctx context.Context, jobID, key string, value map[string]any, updatedBy, executionID string) (*model.JobState, error) {
	if value == nil {
		value = map[string]any{}
	}
	existing, err := s.GetJobStateByKey(ctx, jobID, key)
	if errors.Is(err, ErrNotFound) {
		st := &model.JobState{
			ID:                   idgen.New(),
			JobID:                jobID,
			Key:                  key,
			Value:                value,
			Version:              1,
			UpdatedBy:            updatedBy,
			UpdatedByExecutionID: executionID,
		}
		if err := s.CreateJobState(ctx, st); err != nil {
			return nil, err
		}
		return s.GetJobStateByKey(ctx, jobID, key)
	}
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	updates := map[string]any{
		"value":      json.RawMessage(raw),
		"updated_by": updatedBy,
		"version":    gorm.Expr("version + 1"),
	}
	if executionID != "" {
		updates["updated_by_execution_id"] = executionID
	}
	if err := s.db.WithContext(ctx).Model(&model.JobState{}).
		Where("id = ? AND job_id = ?", existing.ID, jobID).
		Updates(updates).Error; err != nil {
		return nil, err
	}
	return s.GetJobState(ctx, jobID, existing.ID)
}

func (s *Store) ExistsJobStateKey(ctx context.Context, jobID, key, excludeID string) (bool, error) {
	q := s.db.WithContext(ctx).Model(&model.JobState{}).Where("job_id = ? AND `key` = ?", jobID, key)
	if excludeID != "" {
		q = q.Where("id <> ?", excludeID)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) ListJobStates(ctx context.Context, jobID, keyword string, page, limit int) ([]model.JobState, int64, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	q := s.db.WithContext(ctx).Model(&model.JobState{}).Where("job_id = ?", jobID)
	if kw := strings.TrimSpace(keyword); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("id LIKE ? OR `key` LIKE ?", like, like)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var list []model.JobState
	err := q.Order("updated_at DESC").Limit(limit).Offset((page - 1) * limit).Find(&list).Error
	return list, total, err
}

// PatchJobStateCAS 按 version 乐观锁更新；成功后 version+1。
func (s *Store) PatchJobStateCAS(ctx context.Context, jobID, id string, expectedVersion uint64, value map[string]any, updatedBy string) (*model.JobState, error) {
	if value == nil {
		value = map[string]any{}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	res := s.db.WithContext(ctx).Model(&model.JobState{}).
		Where("id = ? AND job_id = ? AND version = ?", id, jobID, expectedVersion).
		Updates(map[string]any{
			"value":                   json.RawMessage(raw),
			"updated_by":              updatedBy,
			"updated_by_execution_id": nil,
			"version":                 gorm.Expr("version + 1"),
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		if _, err := s.GetJobState(ctx, jobID, id); err != nil {
			return nil, err
		}
		return nil, ErrConflict
	}
	return s.GetJobState(ctx, jobID, id)
}

func (s *Store) DeleteJobState(ctx context.Context, jobID, id string) error {
	res := s.db.WithContext(ctx).Where("id = ? AND job_id = ?", id, jobID).Delete(&model.JobState{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
