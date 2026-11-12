package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/GlassJob/job-center/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ListExecutionsFilter Admin 执行列表过滤。
type ListExecutionsFilter struct {
	Keyword     string
	ServiceID   string
	JobID       string
	Status      string
	InstanceID  string
	CreatedFrom *time.Time
	CreatedTo   *time.Time
	Page        int
	Limit       int
}

// ExecutionListRow JOIN 后的执行行。
type ExecutionListRow struct {
	model.Execution
	JobName     string `gorm:"column:job_name"`
	ServiceCode string `gorm:"column:service_code"`
	ServiceName string `gorm:"column:service_name"`
}

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

func (s *Store) GetExecutionByScheduleTick(ctx context.Context, scheduleID string, scheduledAt time.Time) (*model.Execution, error) {
	var e model.Execution
	err := s.db.WithContext(ctx).
		Where("schedule_id = ? AND scheduled_at = ?", scheduleID, scheduledAt).
		First(&e).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// CountActiveExecutionsForJob 统计 job 上非终态 execution（FORBID 并发用）。
func (s *Store) CountActiveExecutionsForJob(ctx context.Context, jobID string) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&model.Execution{}).
		Where("job_id = ? AND status IN ?", jobID, []string{
			model.ExecStatusPending,
			model.ExecStatusQueued,
			model.ExecStatusDispatching,
			model.ExecStatusRunning,
			model.ExecStatusRetryWait,
		}).Count(&n).Error
	return n, err
}

func (s *Store) ListQueuedExecutions(ctx context.Context, limit int) ([]model.Execution, error) {
	if limit < 1 {
		limit = 50
	}
	var list []model.Execution
	err := s.db.WithContext(ctx).
		Where("status = ?", model.ExecStatusQueued).
		Order("queued_at ASC, created_at ASC").
		Limit(limit).
		Find(&list).Error
	return list, err
}

func (s *Store) listExecutionsQuery(ctx context.Context, f ListExecutionsFilter) *gorm.DB {
	q := s.db.WithContext(ctx).Table("executions AS e").
		Joins("LEFT JOIN jobs AS j ON j.id = e.job_id").
		Joins("LEFT JOIN services AS s ON s.id = e.service_id")
	if id := strings.TrimSpace(f.ServiceID); id != "" {
		q = q.Where("e.service_id = ?", id)
	}
	if id := strings.TrimSpace(f.JobID); id != "" {
		q = q.Where("e.job_id = ?", id)
	}
	if st := strings.TrimSpace(f.Status); st != "" {
		q = q.Where("e.status = ?", st)
	}
	if id := strings.TrimSpace(f.InstanceID); id != "" {
		q = q.Where(`EXISTS (
			SELECT 1 FROM execution_attempts a
			WHERE a.execution_id = e.id AND a.instance_id = ?
		)`, id)
	}
	if f.CreatedFrom != nil {
		q = q.Where("e.created_at >= ?", *f.CreatedFrom)
	}
	if f.CreatedTo != nil {
		q = q.Where("e.created_at < ?", *f.CreatedTo)
	}
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		like := "%" + kw + "%"
		q = q.Where(
			"e.id LIKE ? OR e.job_code LIKE ? OR COALESCE(j.code,'') LIKE ? OR COALESCE(j.name,'') LIKE ? OR COALESCE(s.code,'') LIKE ? OR COALESCE(s.name,'') LIKE ?",
			like, like, like, like, like, like,
		)
	}
	return q
}

func (s *Store) ListExecutions(ctx context.Context, f ListExecutionsFilter) ([]ExecutionListRow, int64, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Limit < 1 {
		f.Limit = 20
	}
	if f.Limit > 100 {
		f.Limit = 100
	}
	countQ := s.listExecutionsQuery(ctx, f)
	var total int64
	if err := countQ.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var list []ExecutionListRow
	err := s.listExecutionsQuery(ctx, f).
		Select(`e.*, COALESCE(j.name,'') AS job_name, COALESCE(s.code,'') AS service_code, COALESCE(s.name,'') AS service_name`).
		Order("e.created_at DESC").
		Limit(f.Limit).Offset((f.Page - 1) * f.Limit).
		Scan(&list).Error
	if err != nil {
		return nil, 0, err
	}
	if list == nil {
		list = []ExecutionListRow{}
	}
	return list, total, nil
}

func (s *Store) GetAttemptsByIDs(ctx context.Context, ids []string) (map[string]model.Attempt, error) {
	out := map[string]model.Attempt{}
	if len(ids) == 0 {
		return out, nil
	}
	var list []model.Attempt
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&list).Error; err != nil {
		return nil, err
	}
	for i := range list {
		out[list[i].ID] = list[i]
	}
	return out, nil
}

func (s *Store) InsertExecutionLogs(ctx context.Context, logs []model.ExecutionLog) error {
	if len(logs) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Create(&logs).Error
}

func (s *Store) ListExecutionLogs(ctx context.Context, executionID, attemptID string, afterSeq uint64, limit int) ([]model.ExecutionLog, error) {
	if limit < 1 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	q := s.db.WithContext(ctx).Model(&model.ExecutionLog{}).Where("execution_id = ?", executionID)
	if attemptID != "" {
		q = q.Where("attempt_id = ?", attemptID)
	}
	if afterSeq > 0 {
		q = q.Where("sequence > ?", afterSeq)
	}
	var list []model.ExecutionLog
	err := q.Order("sequence ASC").Limit(limit).Find(&list).Error
	if list == nil {
		list = []model.ExecutionLog{}
	}
	return list, err
}

// LatestExecutionByJobIDs 批量取每个 job 最近一条执行（按 created_at）。
func (s *Store) LatestExecutionByJobIDs(ctx context.Context, jobIDs []string) (map[string]model.Execution, error) {
	out := map[string]model.Execution{}
	if len(jobIDs) == 0 {
		return out, nil
	}
	type row struct {
		JobID string `gorm:"column:job_id"`
		ID    string `gorm:"column:id"`
	}
	var ids []row
	err := s.db.WithContext(ctx).Raw(`
		SELECT e.job_id, e.id FROM executions e
		INNER JOIN (
			SELECT job_id, MAX(created_at) AS max_created
			FROM executions WHERE job_id IN ?
			GROUP BY job_id
		) t ON t.job_id = e.job_id AND t.max_created = e.created_at
		WHERE e.job_id IN ?`, jobIDs, jobIDs).Scan(&ids).Error
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return out, nil
	}
	execIDs := make([]string, 0, len(ids))
	for _, r := range ids {
		execIDs = append(execIDs, r.ID)
	}
	var list []model.Execution
	if err := s.db.WithContext(ctx).Where("id IN ?", execIDs).Find(&list).Error; err != nil {
		return nil, err
	}
	for i := range list {
		out[list[i].JobID] = list[i]
	}
	return out, nil
}

var activeAttemptStatuses = []string{
	model.AttemptDispatching,
	model.AttemptAcknowledged,
	model.AttemptRunning,
}

// RenewAttemptLeasesForInstance 心跳续租该实例上非终态 attempts。
func (s *Store) RenewAttemptLeasesForInstance(ctx context.Context, instanceID string, until time.Time) (int64, error) {
	now := time.Now().UTC()
	res := s.db.WithContext(ctx).Model(&model.Attempt{}).
		Where("instance_id = ? AND status IN ?", instanceID, activeAttemptStatuses).
		Updates(map[string]any{
			"lease_expires_at":  until,
			"last_heartbeat_at": now,
		})
	return res.RowsAffected, res.Error
}

func (s *Store) CountActiveAttemptsForInstance(ctx context.Context, instanceID string) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&model.Attempt{}).
		Where("instance_id = ? AND status IN ?", instanceID, activeAttemptStatuses).
		Count(&n).Error
	return n, err
}

func (s *Store) SetInstanceRunningCount(ctx context.Context, instanceID string, running uint) error {
	return s.db.WithContext(ctx).Model(&model.Instance{}).
		Where("id = ?", instanceID).
		Update("running_count", running).Error
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

// AttemptLogStatsDelta OnLog 后累加的入库/丢弃计数与 last_log_seq 水位。
type AttemptLogStatsDelta struct {
	InfoCollected  uint
	WarnCollected  uint
	ErrorCollected uint
	InfoDropped    uint
	WarnDropped    uint
	ErrorDropped   uint
	LastLogSeq     uint64
}

func (s *Store) IncrAttemptLogStats(ctx context.Context, attemptID string, d AttemptLogStatsDelta) error {
	res := s.db.WithContext(ctx).Exec(`
		UPDATE execution_attempts SET
			log_info_collected  = log_info_collected  + ?,
			log_warn_collected  = log_warn_collected  + ?,
			log_error_collected = log_error_collected + ?,
			log_info_dropped    = log_info_dropped    + ?,
			log_warn_dropped    = log_warn_dropped    + ?,
			log_error_dropped   = log_error_dropped   + ?,
			last_log_seq        = GREATEST(last_log_seq, ?)
		WHERE id = ?`,
		d.InfoCollected, d.WarnCollected, d.ErrorCollected,
		d.InfoDropped, d.WarnDropped, d.ErrorDropped,
		d.LastLogSeq, attemptID,
	)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// PickOnlineInstanceForJob 选 ONLINE 且已注册该 job、尚有容量的实例。
// HasOnlineInstanceForJob 是否存在 ONLINE 且已注册该 job 的实例（不看并发空位）。
func (s *Store) HasOnlineInstanceForJob(ctx context.Context, serviceID, jobID string) (bool, error) {
	var id string
	err := s.db.WithContext(ctx).Table("instances AS i").
		Select("i.id").
		Joins("INNER JOIN instance_jobs AS ij ON ij.instance_id = i.id AND ij.job_id = ?", jobID).
		Where("i.service_id = ? AND i.status = ?", serviceID, model.InstanceStatusOnline).
		Limit(1).
		Scan(&id).Error
	if err != nil {
		return false, err
	}
	return id != "", nil
}

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
