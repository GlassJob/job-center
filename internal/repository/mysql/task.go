package mysql

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type DefinitionLock struct {
	ID             int64
	HandlerVersion string
	Status         string
}

func (q *Queries) LockDefinition(ctx context.Context, serviceID int64, taskCode string) (*DefinitionLock, error) {
	var d DefinitionLock
	err := q.db.QueryRowContext(ctx, "SELECT id, handler_version, status FROM job_definition WHERE service_id=? AND task_code=? FOR UPDATE", serviceID, taskCode).
		Scan(&d.ID, &d.HandlerVersion, &d.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &d, err
}

// DefinitionInput 注册上报的任务定义（已校验）。
type DefinitionInput struct {
	ServiceID      int64
	TaskCode       string
	TaskName       string
	Description    string
	HandlerVersion string
	HandlerMajor   int
	InputSchema    []byte // 规范化 JSON；nil=未声明
	SchemaHash     string
	DefaultCron    *string
	TimeoutSeconds int
	MaxConcurrency int
}

func schemaArg(b []byte) any {
	if b == nil {
		return nil
	}
	return string(b)
}

func (q *Queries) InsertDefinition(ctx context.Context, d DefinitionInput) (int64, error) {
	var to any
	if d.TimeoutSeconds > 0 {
		to = d.TimeoutSeconds
	}
	res, err := q.db.ExecContext(ctx, `INSERT INTO job_definition(service_id, task_code, task_name, description, handler_version, input_schema, schema_hash,
		default_cron, default_timeout_seconds, status) VALUES (?,?,?,?,?,?,?,?,?,'ACTIVE')`,
		d.ServiceID, d.TaskCode, d.TaskName, nullStr(d.Description), d.HandlerVersion, schemaArg(d.InputSchema), d.SchemaHash, d.DefaultCron, to)
	if err != nil {
		if IsDuplicate(err) {
			return 0, ErrDuplicate
		}
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateDefinitionContent 仅在上报版本 ≥ 已记录版本时调用（由应用层比较），更新展示用的最新定义。
// 注意：不修改 status=DISABLED（管理员禁用优先）。
func (q *Queries) UpdateDefinitionContent(ctx context.Context, id int64, d DefinitionInput) error {
	var to any
	if d.TimeoutSeconds > 0 {
		to = d.TimeoutSeconds
	}
	_, err := q.db.ExecContext(ctx, `UPDATE job_definition SET task_name=?, description=?, handler_version=?, input_schema=?, schema_hash=?,
		default_cron=?, default_timeout_seconds=?, last_registered_at=NOW(3), status=IF(status='DISABLED','DISABLED','ACTIVE') WHERE id=?`,
		d.TaskName, nullStr(d.Description), d.HandlerVersion, schemaArg(d.InputSchema), d.SchemaHash, d.DefaultCron, to, id)
	return err
}

// TouchDefinition 旧版本实例注册：只刷新注册时间与 ORPHANED→ACTIVE，不回退定义内容。
func (q *Queries) TouchDefinition(ctx context.Context, id int64) error {
	_, err := q.db.ExecContext(ctx, `UPDATE job_definition SET last_registered_at=NOW(3), status=IF(status='DISABLED','DISABLED','ACTIVE') WHERE id=?`, id)
	return err
}

func (q *Queries) UpsertDefinitionVersion(ctx context.Context, defID int64, d DefinitionInput) error {
	_, err := q.db.ExecContext(ctx, `INSERT INTO job_definition_version(definition_id, handler_version, handler_major, schema_hash, input_schema)
		VALUES (?,?,?,?,?) ON DUPLICATE KEY UPDATE last_seen_at=NOW(3)`, defID, d.HandlerVersion, d.HandlerMajor, d.SchemaHash, schemaArg(d.InputSchema))
	return err
}

// EnsureDefaultSchedule 仅当该任务还没有调度配置时插入默认配置（enabled=0, config_source=DEFAULT）。
// 已有配置（无论 DEFAULT 还是 ADMIN）一律不修改——注册永不覆盖调度配置（任务书 4.3）。
func (q *Queries) EnsureDefaultSchedule(ctx context.Context, taskID int64, cronExpr *string, timeout, maxConc int) (bool, error) {
	typ := "NONE"
	if cronExpr != nil {
		typ = "CRON"
	}
	if timeout <= 0 {
		timeout = 3600
	}
	if maxConc <= 0 {
		maxConc = 1
	}
	res, err := q.db.ExecContext(ctx, `INSERT INTO job_schedule(task_id, schedule_type, cron_expression, timezone, enabled, manual_enabled, timeout_seconds,
		max_concurrency, retry_policy, config_source) VALUES (?,?,?,NULL,0,1,?,?,'{"max_attempts":1}','DEFAULT')
		ON DUPLICATE KEY UPDATE task_id=task_id`, taskID, typ, cronExpr, timeout, maxConc)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

type InstanceTask struct {
	DefinitionID   int64
	HandlerVersion string
	HandlerMajor   int
	SchemaHash     string
	MaxConcurrency int
}

// ReplaceInstanceTasks 用本次注册的任务清单替换实例能力（同一实例重复注册不会产生重复行）。
func (q *Queries) ReplaceInstanceTasks(ctx context.Context, instancePK int64, tasks []InstanceTask) error {
	if _, err := q.db.ExecContext(ctx, "DELETE FROM job_instance_task WHERE instance_pk=?", instancePK); err != nil {
		return err
	}
	if len(tasks) == 0 {
		return nil
	}
	var sb strings.Builder
	args := make([]any, 0, len(tasks)*6)
	sb.WriteString("INSERT INTO job_instance_task(instance_pk, definition_id, handler_version, handler_major, schema_hash, max_concurrency) VALUES ")
	for i, t := range tasks {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(?,?,?,?,?,?)")
		var mc any
		if t.MaxConcurrency > 0 {
			mc = t.MaxConcurrency
		}
		args = append(args, instancePK, t.DefinitionID, t.HandlerVersion, t.HandlerMajor, t.SchemaHash, mc)
	}
	_, err := q.db.ExecContext(ctx, sb.String(), args...)
	return err
}

// Schedule 调度配置。
type Schedule struct {
	TaskID                  int64           `json:"task_id"`
	ScheduleType            string          `json:"schedule_type"`
	CronExpression          *string         `json:"cron_expression"`
	Timezone                *string         `json:"timezone"`
	DefaultParams           json.RawMessage `json:"default_params"`
	EffectiveTimezone       string          `json:"effective_timezone"`
	Enabled                 bool            `json:"enabled"`
	ManualEnabled           bool            `json:"manual_enabled"`
	TimeoutSeconds          int             `json:"timeout_seconds"`
	ConcurrencyPolicy       string          `json:"concurrency_policy"`
	ConcurrencyScope        string          `json:"concurrency_scope"`
	MaxConcurrency          int             `json:"max_concurrency"`
	QueueMax                int             `json:"queue_max"`
	RetryPolicy             json.RawMessage `json:"retry_policy"`
	MisfirePolicy           string          `json:"misfire_policy"`
	MisfireThresholdSeconds int             `json:"misfire_threshold_seconds"`
	CatchUpMax              int             `json:"catch_up_max"`
	DispatchStrategy        string          `json:"dispatch_strategy"`
	NextFireAt              *time.Time      `json:"next_fire_at"`
	LastFireAt              *time.Time      `json:"last_fire_at"`
	ConfigSource            string          `json:"config_source"`
	Version                 int64           `json:"version"`
	UpdatedBy               *string         `json:"updated_by"`
}

const scheduleCols = `task_id, schedule_type, cron_expression, timezone, default_params, enabled, manual_enabled, timeout_seconds, concurrency_policy, concurrency_scope,
  max_concurrency, queue_max, retry_policy, misfire_policy, misfire_threshold_seconds, catch_up_max, dispatch_strategy, next_fire_at, last_fire_at,
  config_source, version, updated_by`

func scanSchedule(sc interface{ Scan(...any) error }) (*Schedule, error) {
	var s Schedule
	var cronE, tz, ub sql.NullString
	var nf, lf sql.NullTime
	var rp, dp []byte
	if err := sc.Scan(&s.TaskID, &s.ScheduleType, &cronE, &tz, &dp, &s.Enabled, &s.ManualEnabled, &s.TimeoutSeconds, &s.ConcurrencyPolicy, &s.ConcurrencyScope,
		&s.MaxConcurrency, &s.QueueMax, &rp, &s.MisfirePolicy, &s.MisfireThresholdSeconds, &s.CatchUpMax, &s.DispatchStrategy, &nf, &lf,
		&s.ConfigSource, &s.Version, &ub); err != nil {
		return nil, err
	}
	s.CronExpression, s.Timezone, s.UpdatedBy = ptrStr(cronE), ptrStr(tz), ptrStr(ub)
	s.NextFireAt, s.LastFireAt = ptrTime(nf), ptrTime(lf)
	s.RetryPolicy = json.RawMessage(rp)
	if len(dp) > 0 {
		s.DefaultParams = json.RawMessage(dp)
	}
	return &s, nil
}

func (q *Queries) GetSchedule(ctx context.Context, taskID int64) (*Schedule, error) {
	s, err := scanSchedule(q.db.QueryRowContext(ctx, "SELECT "+scheduleCols+" FROM job_schedule WHERE task_id=?", taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return s, err
}

// UpdateScheduleCAS 乐观锁更新调度参数（不含 enabled）；成功后 config_source=ADMIN、version+1。
// 返回受影响行数（0=版本冲突或不存在）。
func defaultParamsArg(raw json.RawMessage) any {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return nil
	}
	return string(t)
}

func (q *Queries) UpdateScheduleCAS(ctx context.Context, expectedVersion int64, s *Schedule, updatedBy string) (int64, error) {
	res, err := q.db.ExecContext(ctx, `UPDATE job_schedule SET schedule_type=?, cron_expression=?, timezone=?, default_params=?, manual_enabled=?, timeout_seconds=?,
		concurrency_policy=?, concurrency_scope=?, max_concurrency=?, queue_max=?, retry_policy=?, misfire_policy=?, misfire_threshold_seconds=?,
		catch_up_max=?, dispatch_strategy=?, next_fire_at=?, config_source='ADMIN', version=version+1, updated_by=?
		WHERE task_id=? AND version=?`,
		s.ScheduleType, s.CronExpression, s.Timezone, defaultParamsArg(s.DefaultParams), s.ManualEnabled, s.TimeoutSeconds, s.ConcurrencyPolicy, s.ConcurrencyScope,
		s.MaxConcurrency, s.QueueMax, string(s.RetryPolicy), s.MisfirePolicy, s.MisfireThresholdSeconds, s.CatchUpMax, s.DispatchStrategy,
		s.NextFireAt, updatedBy, s.TaskID, expectedVersion)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SetEnabledCAS 仅改 enabled（及 next_fire_at）；成功后 config_source=ADMIN、version+1。
func (q *Queries) SetEnabledCAS(ctx context.Context, taskID, expectedVersion int64, enabled bool, nextFire *time.Time, updatedBy string) (int64, error) {
	res, err := q.db.ExecContext(ctx, `UPDATE job_schedule SET enabled=?, next_fire_at=?, config_source='ADMIN', version=version+1, updated_by=?
		WHERE task_id=? AND version=?`, enabled, nextFire, updatedBy, taskID, expectedVersion)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Task 任务列表项。
type Task struct {
	ID               int64      `json:"id"`
	ServiceID        int64      `json:"service_id"`
	ServiceCode      string     `json:"service_code"`
	TaskCode         string     `json:"task_code"`
	TaskName         string     `json:"task_name"`
	Description      *string    `json:"description"`
	HandlerVersion   string     `json:"handler_version"`
	Status           string     `json:"status"`
	LastRegisteredAt time.Time  `json:"last_registered_at"`
	Schedule         *Schedule  `json:"schedule"`
	LastExecution    *time.Time `json:"-"`
}

type TaskProvider struct {
	InstanceID     string `json:"instance_id"`
	HandlerVersion string `json:"handler_version"`
	HandlerMajor   int    `json:"handler_major"`
	SchemaHash     string `json:"schema_hash"`
	Status         string `json:"status"`
	AppVersion     *string `json:"app_version"`
	GitCommit      *string `json:"git_commit"`
}

type TaskVersion struct {
	HandlerVersion string    `json:"handler_version"`
	HandlerMajor   int       `json:"handler_major"`
	SchemaHash     string    `json:"schema_hash"`
	FirstSeenAt    time.Time `json:"first_seen_at"`
	LastSeenAt     time.Time `json:"last_seen_at"`
}

type TaskDetail struct {
	Task
	InputSchema json.RawMessage `json:"input_schema"`
	SchemaHash  *string         `json:"schema_hash"`
	DefaultCron *string         `json:"default_cron"`
	TaskState   *TaskState      `json:"task_state"`
	Providers   []TaskProvider  `json:"providers"`
	Versions    []TaskVersion   `json:"versions"`
}

type TaskFilter struct {
	ServiceID   int64
	Keyword     string
	Enabled     *bool
	Page, Limit int
}

const taskSelect = `SELECT d.id, d.service_id, s.service_code, d.task_code, d.task_name, d.description, d.handler_version, d.status, d.last_registered_at,
  ` + "sc.task_id, sc.schedule_type, sc.cron_expression, sc.timezone, sc.default_params, sc.enabled, sc.manual_enabled, sc.timeout_seconds, sc.concurrency_policy, sc.concurrency_scope, sc.max_concurrency, sc.queue_max, sc.retry_policy, sc.misfire_policy, sc.misfire_threshold_seconds, sc.catch_up_max, sc.dispatch_strategy, sc.next_fire_at, sc.last_fire_at, sc.config_source, sc.version, sc.updated_by" + `
FROM job_definition d JOIN job_service s ON s.id=d.service_id JOIN job_schedule sc ON sc.task_id=d.id`

func scanTask(sc interface{ Scan(...any) error }) (*Task, error) {
	var t Task
	var desc sql.NullString
	var s Schedule
	var cronE, tz, ub sql.NullString
	var nf, lf sql.NullTime
	var rp, dp []byte
	ptrs := []any{&t.ID, &t.ServiceID, &t.ServiceCode, &t.TaskCode, &t.TaskName, &desc, &t.HandlerVersion, &t.Status, &t.LastRegisteredAt,
		&s.TaskID, &s.ScheduleType, &cronE, &tz, &dp, &s.Enabled, &s.ManualEnabled, &s.TimeoutSeconds, &s.ConcurrencyPolicy, &s.ConcurrencyScope,
		&s.MaxConcurrency, &s.QueueMax, &rp, &s.MisfirePolicy, &s.MisfireThresholdSeconds, &s.CatchUpMax, &s.DispatchStrategy, &nf, &lf,
		&s.ConfigSource, &s.Version, &ub}
	if err := sc.Scan(ptrs...); err != nil {
		return nil, err
	}
	t.Description = ptrStr(desc)
	t.LastRegisteredAt = t.LastRegisteredAt.UTC()
	s.CronExpression, s.Timezone, s.UpdatedBy = ptrStr(cronE), ptrStr(tz), ptrStr(ub)
	s.NextFireAt, s.LastFireAt = ptrTime(nf), ptrTime(lf)
	s.RetryPolicy = json.RawMessage(rp)
	if len(dp) > 0 {
		s.DefaultParams = json.RawMessage(dp)
	}
	t.Schedule = &s
	return &t, nil
}

func (q *Queries) ListTasks(ctx context.Context, f TaskFilter) ([]*Task, int, error) {
	where, args := " WHERE 1=1", []any{}
	if f.ServiceID > 0 {
		where += " AND d.service_id=?"
		args = append(args, f.ServiceID)
	}
	if f.Keyword != "" {
		where += " AND (d.task_code LIKE ? OR d.task_name LIKE ?)"
		k := "%" + escapeLike(f.Keyword) + "%"
		args = append(args, k, k)
	}
	if f.Enabled != nil {
		where += " AND sc.enabled=?"
		args = append(args, *f.Enabled)
	}
	var total int
	if err := q.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM job_definition d JOIN job_schedule sc ON sc.task_id=d.id"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := q.db.QueryContext(ctx, taskSelect+where+" ORDER BY d.id LIMIT ? OFFSET ?", append(args, f.Limit, (f.Page-1)*f.Limit)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, t)
	}
	return out, total, rows.Err()
}

func (q *Queries) GetTask(ctx context.Context, id int64) (*TaskDetail, error) {
	t, err := scanTask(q.db.QueryRowContext(ctx, taskSelect+" WHERE d.id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d := &TaskDetail{Task: *t, Providers: []TaskProvider{}, Versions: []TaskVersion{}}
	var schema []byte
	var hash, dc sql.NullString
	if err := q.db.QueryRowContext(ctx, "SELECT input_schema, schema_hash, default_cron FROM job_definition WHERE id=?", id).Scan(&schema, &hash, &dc); err != nil {
		return nil, err
	}
	if schema != nil {
		d.InputSchema = json.RawMessage(schema)
	}
	d.SchemaHash, d.DefaultCron = ptrStr(hash), ptrStr(dc)
	ts, err := q.GetTaskState(ctx, id)
	if err != nil {
		return nil, err
	}
	d.TaskState = ts
	rows, err := q.db.QueryContext(ctx, `SELECT i.instance_id, it.handler_version, it.handler_major, it.schema_hash, i.status, i.app_version, i.git_commit
		FROM job_instance_task it JOIN job_instance i ON i.id=it.instance_pk WHERE it.definition_id=? AND i.status<>'OFFLINE' ORDER BY i.id`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p TaskProvider
		var av, gc sql.NullString
		if err := rows.Scan(&p.InstanceID, &p.HandlerVersion, &p.HandlerMajor, &p.SchemaHash, &p.Status, &av, &gc); err != nil {
			rows.Close()
			return nil, err
		}
		p.AppVersion, p.GitCommit = ptrStr(av), ptrStr(gc)
		d.Providers = append(d.Providers, p)
	}
	rows.Close()
	rows, err = q.db.QueryContext(ctx, `SELECT handler_version, handler_major, schema_hash, first_seen_at, last_seen_at FROM job_definition_version
		WHERE definition_id=? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v TaskVersion
		if err := rows.Scan(&v.HandlerVersion, &v.HandlerMajor, &v.SchemaHash, &v.FirstSeenAt, &v.LastSeenAt); err != nil {
			return nil, err
		}
		v.FirstSeenAt, v.LastSeenAt = v.FirstSeenAt.UTC(), v.LastSeenAt.UTC()
		d.Versions = append(d.Versions, v)
	}
	return d, rows.Err()
}

// AttemptSnapshot 重连裁决所需的 Attempt 信息。
type AttemptSnapshot struct {
	AttemptID, ExecutionID, Status, LeaseToken, InstanceID string
	IsCurrent                                              bool
	LostAt, LeaseExpiresAt                                 *time.Time
	DBNow                                                  time.Time
}

func (q *Queries) GetAttemptSnapshot(ctx context.Context, attemptID string) (*AttemptSnapshot, error) {
	var a AttemptSnapshot
	var tok, inst sql.NullString
	var lost, exp sql.NullTime
	err := q.db.QueryRowContext(ctx, `SELECT a.attempt_id, a.execution_id, a.status, a.lease_token, a.instance_id, a.lost_at, a.lease_expires_at,
		COALESCE(e.current_attempt_id = a.attempt_id, 0), NOW(3)
		FROM job_execution_attempt a LEFT JOIN job_execution e ON e.execution_id=a.execution_id WHERE a.attempt_id=?`, attemptID).
		Scan(&a.AttemptID, &a.ExecutionID, &a.Status, &tok, &inst, &lost, &exp, &a.IsCurrent, &a.DBNow)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.LeaseToken, a.InstanceID = tok.String, inst.String
	a.LostAt, a.LeaseExpiresAt = ptrTime(lost), ptrTime(exp)
	return &a, nil
}

func (q *Queries) GetTaskIDByCode(ctx context.Context, serviceID int64, taskCode string) (int64, error) {
	var id int64
	err := q.db.QueryRowContext(ctx, "SELECT id FROM job_definition WHERE service_id=? AND task_code=?", serviceID, taskCode).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}
