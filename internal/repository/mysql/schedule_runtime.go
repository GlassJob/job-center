package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// DueSchedule 到期待触发的调度行（含任务元数据）。
type DueSchedule struct {
	ScheduleID              int64
	TaskID                  int64
	ServiceID               int64
	TaskCode                string
	TaskName                string
	HandlerVersion          string
	SchemaHash              *string
	CronExpression          string
	Timezone                *string
	TimeoutSeconds          int
	ConcurrencyPolicy       string
	ConcurrencyScope        string
	MaxConcurrency          int
	QueueMax                int
	RetryPolicy             json.RawMessage
	MisfirePolicy           string
	MisfireThresholdSeconds int
	CatchUpMax              int
	NextFireAt              time.Time
	Version                 int64
}

// ListDueSchedules 拉取到期 CRON（不加锁；真正触发在 FireDueSchedule 事务内锁行）。
func (q *Queries) ListDueSchedules(ctx context.Context, limit int) ([]*DueSchedule, error) {
	if limit < 1 {
		limit = 200
	}
	rows, err := q.db.QueryContext(ctx, `SELECT sc.id, sc.task_id, d.service_id, d.task_code, d.task_name, d.handler_version, d.schema_hash,
		sc.cron_expression, sc.timezone, sc.timeout_seconds, sc.concurrency_policy, sc.concurrency_scope, sc.max_concurrency, sc.queue_max,
		sc.retry_policy, sc.misfire_policy, sc.misfire_threshold_seconds, sc.catch_up_max, sc.next_fire_at, sc.version
		FROM job_schedule sc
		JOIN job_definition d ON d.id=sc.task_id
		WHERE sc.enabled=1 AND sc.schedule_type='CRON' AND sc.next_fire_at IS NOT NULL AND sc.next_fire_at <= UTC_TIMESTAMP(3)
		ORDER BY sc.next_fire_at ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DueSchedule
	for rows.Next() {
		var s DueSchedule
		var tz, sh sql.NullString
		var rp []byte
		if err := rows.Scan(&s.ScheduleID, &s.TaskID, &s.ServiceID, &s.TaskCode, &s.TaskName, &s.HandlerVersion, &sh,
			&s.CronExpression, &tz, &s.TimeoutSeconds, &s.ConcurrencyPolicy, &s.ConcurrencyScope, &s.MaxConcurrency, &s.QueueMax,
			&rp, &s.MisfirePolicy, &s.MisfireThresholdSeconds, &s.CatchUpMax, &s.NextFireAt, &s.Version); err != nil {
			return nil, err
		}
		s.Timezone, s.SchemaHash = ptrStr(tz), ptrStr(sh)
		s.RetryPolicy = json.RawMessage(rp)
		s.NextFireAt = s.NextFireAt.UTC()
		out = append(out, &s)
	}
	return out, rows.Err()
}

// LockScheduleForUpdate 在事务内锁调度行；若已非到期返回 ErrNotFound。
func (q *Queries) LockScheduleForUpdate(ctx context.Context, scheduleID int64) (*DueSchedule, error) {
	row := q.db.QueryRowContext(ctx, `SELECT sc.id, sc.task_id, d.service_id, d.task_code, d.task_name, d.handler_version, d.schema_hash,
		sc.cron_expression, sc.timezone, sc.timeout_seconds, sc.concurrency_policy, sc.concurrency_scope, sc.max_concurrency, sc.queue_max,
		sc.retry_policy, sc.misfire_policy, sc.misfire_threshold_seconds, sc.catch_up_max, sc.next_fire_at, sc.version
		FROM job_schedule sc
		JOIN job_definition d ON d.id=sc.task_id
		WHERE sc.id=? AND sc.enabled=1 AND sc.schedule_type='CRON' AND sc.next_fire_at IS NOT NULL AND sc.next_fire_at <= UTC_TIMESTAMP(3)
		FOR UPDATE`, scheduleID)
	var s DueSchedule
	var tz, sh sql.NullString
	var rp []byte
	err := row.Scan(&s.ScheduleID, &s.TaskID, &s.ServiceID, &s.TaskCode, &s.TaskName, &s.HandlerVersion, &sh,
		&s.CronExpression, &tz, &s.TimeoutSeconds, &s.ConcurrencyPolicy, &s.ConcurrencyScope, &s.MaxConcurrency, &s.QueueMax,
		&rp, &s.MisfirePolicy, &s.MisfireThresholdSeconds, &s.CatchUpMax, &s.NextFireAt, &s.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s.Timezone, s.SchemaHash = ptrStr(tz), ptrStr(sh)
	s.RetryPolicy = json.RawMessage(rp)
	s.NextFireAt = s.NextFireAt.UTC()
	return &s, nil
}

// AdvanceSchedule 更新 next/last_fire_at。
func (q *Queries) AdvanceSchedule(ctx context.Context, scheduleID int64, nextFire, lastFire time.Time) error {
	_, err := q.db.ExecContext(ctx, `UPDATE job_schedule SET next_fire_at=?, last_fire_at=?, version=version+1 WHERE id=?`,
		nextFire.UTC(), lastFire.UTC(), scheduleID)
	return err
}

// HoldNodeLease 抢占/续约节点租约。返回是否持有。
func (q *Queries) HoldNodeLease(ctx context.Context, leaseName, nodeID string, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		ttl = 15 * time.Second
	}
	// 先尝试插入
	_, err := q.db.ExecContext(ctx, `INSERT INTO job_node_lease(lease_name, owner_node_id, fencing_token, lease_until)
		VALUES (?,?,1,DATE_ADD(UTC_TIMESTAMP(3), INTERVAL ? MICROSECOND))
		ON DUPLICATE KEY UPDATE lease_name=lease_name`, leaseName, nodeID, ttl.Microseconds())
	if err != nil {
		return false, err
	}
	res, err := q.db.ExecContext(ctx, `UPDATE job_node_lease SET
		fencing_token = fencing_token + IF(owner_node_id=?, 0, 1),
		owner_node_id=?,
		lease_until=DATE_ADD(UTC_TIMESTAMP(3), INTERVAL ? MICROSECOND)
		WHERE lease_name=? AND (owner_node_id=? OR lease_until < UTC_TIMESTAMP(3))`,
		nodeID, nodeID, ttl.Microseconds(), leaseName, nodeID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// CronExecutionInput 调度创建 Execution 的参数。
type CronExecutionInput struct {
	ExecutionID      string
	ScheduleID       int64
	TaskID           int64
	ServiceID        int64
	TaskCode         string
	TriggerType      string // CRON | CATCH_UP
	ScheduledAt      time.Time
	Params           json.RawMessage
	Status           string // PENDING | QUEUED | SKIPPED
	StatusReason     string
	ConcurrencySlot  *string
	HandlerVersion   string
	HandlerMajor     int
	SchemaHash       *string
	TimeoutSeconds   int
	RetryPolicy      json.RawMessage
	MaxAttempts      int
	QueuedAt         *time.Time
}

// InsertCronExecution 插入调度触发的执行；uk_schedule_tick / uk_concurrency_slot 冲突返回 ErrDuplicate。
func (q *Queries) InsertCronExecution(ctx context.Context, e CronExecutionInput) error {
	if len(e.Params) == 0 {
		e.Params = json.RawMessage(`{}`)
	}
	if len(e.RetryPolicy) == 0 {
		e.RetryPolicy = json.RawMessage(`{"max_attempts":1}`)
	}
	var nextAttempt any
	if e.Status == "PENDING" {
		nextAttempt = time.Now().UTC()
	}
	var queued any
	if e.QueuedAt != nil {
		queued = e.QueuedAt.UTC()
	}
	_, err := q.db.ExecContext(ctx, `INSERT INTO job_execution(
		execution_id, task_id, service_id, task_code, schedule_id, trigger_type, triggered_by, params,
		status, status_reason, handler_version, handler_major, schema_hash, timeout_seconds, retry_policy,
		max_attempts, attempt_count, concurrency_slot, next_attempt_at, scheduled_at, queued_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0,?,?,?,?)`,
		e.ExecutionID, e.TaskID, e.ServiceID, e.TaskCode, e.ScheduleID, e.TriggerType, "system:scheduler", string(e.Params),
		e.Status, nullStr(e.StatusReason), e.HandlerVersion, e.HandlerMajor, e.SchemaHash, e.TimeoutSeconds, string(e.RetryPolicy),
		e.MaxAttempts, e.ConcurrencySlot, nextAttempt, e.ScheduledAt.UTC(), queued)
	if IsDuplicate(err) {
		return ErrDuplicate
	}
	return err
}

// CountActiveExecutions 活跃 = PENDING|RUNNING（QUEUED 不占槽）。
func (q *Queries) CountActiveExecutions(ctx context.Context, taskID int64) (int, error) {
	var n int
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_execution WHERE task_id=? AND status IN ('PENDING','RUNNING')`, taskID).Scan(&n)
	return n, err
}

// CountQueuedExecutions 排队数。
func (q *Queries) CountQueuedExecutions(ctx context.Context, taskID int64) (int, error) {
	var n int
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_execution WHERE task_id=? AND status='QUEUED'`, taskID).Scan(&n)
	return n, err
}

// CountActiveBySlot 按 concurrency_slot 前缀不适用；直接看唯一槽是否被占：查 status 活跃且 slot=。
func (q *Queries) SlotOccupied(ctx context.Context, slot string) (bool, error) {
	var n int
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_execution WHERE concurrency_slot=? AND status IN ('PENDING','RUNNING')`, slot).Scan(&n)
	return n > 0, err
}

// WakeNextQueued 终态后唤醒队首 QUEUED → PENDING 并占槽。返回被唤醒的 execution_id（空=无）。
func (q *Queries) WakeNextQueued(ctx context.Context, taskID int64, slot string) (string, error) {
	row := q.db.QueryRowContext(ctx, `SELECT execution_id FROM job_execution
		WHERE task_id=? AND status='QUEUED' ORDER BY id ASC LIMIT 1 FOR UPDATE`, taskID)
	var execID string
	if err := row.Scan(&execID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	_, err := q.db.ExecContext(ctx, `UPDATE job_execution SET status='PENDING', concurrency_slot=?, next_attempt_at=UTC_TIMESTAMP(3),
		queued_at=COALESCE(queued_at, created_at), status_reason=NULL, version=version+1 WHERE execution_id=? AND status='QUEUED'`,
		slot, execID)
	if err != nil {
		return "", err
	}
	return execID, nil
}

// DBNow 返回数据库 UTC 时间。
func (q *Queries) DBNow(ctx context.Context) (time.Time, error) {
	var t time.Time
	err := q.db.QueryRowContext(ctx, `SELECT UTC_TIMESTAMP(3)`).Scan(&t)
	return t.UTC(), err
}

// SetScheduleNextFire 直接设置 next_fire_at（测试/恢复用）。
func (q *Queries) SetScheduleNextFire(ctx context.Context, taskID int64, next *time.Time, enabled bool) error {
	_, err := q.db.ExecContext(ctx, `UPDATE job_schedule SET next_fire_at=?, enabled=? WHERE task_id=?`, next, enabled, taskID)
	return err
}

// GetScheduleID 按 task_id 取 schedule.id。
func (q *Queries) GetScheduleID(ctx context.Context, taskID int64) (int64, error) {
	var id int64
	err := q.db.QueryRowContext(ctx, `SELECT id FROM job_schedule WHERE task_id=?`, taskID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

// CountExecutionsByScheduleTick 同一计划点执行数（含 SKIPPED）。
func (q *Queries) CountExecutionsByScheduleTick(ctx context.Context, scheduleID int64, scheduledAt time.Time) (int, error) {
	var n int
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_execution WHERE schedule_id=? AND scheduled_at=?`,
		scheduleID, scheduledAt.UTC()).Scan(&n)
	return n, err
}

// CountExecutionsForSchedule 某 schedule 下执行总数（可选 status）。
func (q *Queries) CountExecutionsForSchedule(ctx context.Context, scheduleID int64, status string) (int, error) {
	var n int
	var err error
	if status == "" {
		err = q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_execution WHERE schedule_id=?`, scheduleID).Scan(&n)
	} else {
		err = q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_execution WHERE schedule_id=? AND status=?`, scheduleID, status).Scan(&n)
	}
	return n, err
}

// EnsureNodeLeaseRow 保证租约行存在（启动时）。
func (q *Queries) EnsureNodeLeaseRow(ctx context.Context, leaseName, nodeID string) error {
	_, err := q.db.ExecContext(ctx, `INSERT INTO job_node_lease(lease_name, owner_node_id, fencing_token, lease_until)
		VALUES (?,?,0,'1970-01-01 00:00:00.000') ON DUPLICATE KEY UPDATE lease_name=lease_name`, leaseName, nodeID)
	return err
}

