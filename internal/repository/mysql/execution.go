package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/GlassJob/job-center/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Store) CreateExecution(ctx context.Context, e *model.Execution) error {
	return s.db.WithContext(ctx).Create(e).Error
}

func (s *Store) CreateAttempt(ctx context.Context, a *model.Attempt) error {
	return s.db.WithContext(ctx).Create(a).Error
}

func (s *Store) GetExecution(ctx context.Context, id string) (*model.Execution, error) {
	var e model.Execution
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&e).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *Store) GetExecutionByJobIdempotency(ctx context.Context, jobID, key string) (*model.Execution, error) {
	var e model.Execution
	err := s.db.WithContext(ctx).Where("job_id = ? AND idempotency_key = ?", jobID, key).First(&e).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *Store) GetAttempt(ctx context.Context, id string) (*model.Attempt, error) {
	var a model.Attempt
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *Store) UpdateExecutionFields(ctx context.Context, id string, fields map[string]any) error {
	res := s.db.WithContext(ctx).Model(&model.Execution{}).Where("id = ?", id).Updates(fields)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateAttemptFields(ctx context.Context, id string, fields map[string]any) error {
	res := s.db.WithContext(ctx).Model(&model.Attempt{}).Where("id = ?", id).Updates(fields)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// PickOnlineInstanceForJob 选 ONLINE 且已注册该 job、尚有容量的实例。
func (s *Store) PickOnlineInstanceForJob(ctx context.Context, serviceID, jobID, preferInstanceID string) (*model.Instance, error) {
	q := s.db.WithContext(ctx).Table("instances AS i").
		Select("i.*").
		Joins("INNER JOIN instance_jobs AS ij ON ij.instance_id = i.id AND ij.job_id = ?", jobID).
		Where("i.service_id = ? AND i.status = ? AND i.running_count < i.max_concurrency", serviceID, model.InstanceStatusOnline)
	if preferInstanceID != "" {
		var prefer model.Instance
		err := q.Session(&gorm.Session{}).Where("i.id = ?", preferInstanceID).Take(&prefer).Error
		if err == nil {
			return &prefer, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	var inst model.Instance
	err := q.Order("i.running_count ASC, i.last_heartbeat_at DESC").Limit(1).Take(&inst).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &inst, nil
}

func (s *Store) AdjustInstanceRunning(ctx context.Context, instanceID string, delta int) error {
	if delta == 0 || instanceID == "" {
		return nil
	}
	if delta > 0 {
		return s.db.WithContext(ctx).Model(&model.Instance{}).
			Where("id = ?", instanceID).
			UpdateColumn("running_count", gorm.Expr("running_count + ?", delta)).Error
	}
	return s.db.WithContext(ctx).Model(&model.Instance{}).
		Where("id = ? AND running_count >= ?", instanceID, -delta).
		UpdateColumn("running_count", gorm.Expr("GREATEST(CAST(running_count AS SIGNED) + ?, 0)", delta)).Error
}

func (s *Store) SetExecutionDispatching(ctx context.Context, execID, attemptID string, attemptNo uint) error {
	return s.UpdateExecutionFields(ctx, execID, map[string]any{
		"status":             model.ExecStatusDispatching,
		"attempt_count":      attemptNo,
		"current_attempt_no": attemptNo,
		"current_attempt_id": attemptID,
	})
}

func MarshalJSONObject(v any) (json.RawMessage, error) {
	if v == nil {
		return json.RawMessage(`{}`), nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 || string(b) == "null" {
		return json.RawMessage(`{}`), nil
	}
	return b, nil
}

// LockAttemptForUpdate 行锁读取 attempt（校验 lease 用）。
func (s *Store) LockAttemptForUpdate(ctx context.Context, attemptID string) (*model.Attempt, error) {
	var a model.Attempt
	err := s.db.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", attemptID).First(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *Store) WithTx(ctx context.Context, fn func(tx *Store) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&Store{db: tx})
	})
}

func PtrTime(t time.Time) *time.Time { return &t }
