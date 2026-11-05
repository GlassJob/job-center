package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type Execution struct {
	ID               int64           `json:"-"`
	ExecutionID      string          `json:"execution_id"`
	TaskID           int64           `json:"task_id"`
	ServiceID        int64           `json:"service_id"`
	TaskCode         string          `json:"task_code"`
	TriggerType      string          `json:"trigger_type"`
	TriggeredBy      string          `json:"triggered_by"`
	Params           json.RawMessage `json:"params"`
	IdempotencyKey   *string         `json:"idempotency_key,omitempty"`
	Status           string          `json:"status"`
	StatusReason     *string         `json:"status_reason,omitempty"`
	HandlerVersion   *string         `json:"handler_version,omitempty"`
	HandlerMajor     *int            `json:"handler_major,omitempty"`
	SchemaHash       *string         `json:"schema_hash,omitempty"`
	TimeoutSeconds   int             `json:"timeout_seconds"`
	RetryPolicy      json.RawMessage `json:"retry_policy"`
	MaxAttempts      int             `json:"max_attempts"`
	AttemptCount     int             `json:"attempt_count"`
	CurrentAttemptID *string         `json:"current_attempt_id,omitempty"`
	StartedAt        *time.Time      `json:"started_at,omitempty"`
	FinishedAt       *time.Time      `json:"finished_at,omitempty"`
	Result           json.RawMessage `json:"result,omitempty"`
	ErrorCode        *string         `json:"error_code,omitempty"`
	ErrorMessage     *string         `json:"error_message,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

type Attempt struct {
	AttemptID        string          `json:"attempt_id"`
	ExecutionID      string          `json:"execution_id"`
	AttemptNumber    int             `json:"attempt_number"`
	TaskID           int64           `json:"task_id"`
	ServiceID        int64           `json:"service_id"`
	InstanceID       *string         `json:"instance_id,omitempty"`
	Hostname         *string         `json:"hostname,omitempty"`
	GitCommit        *string         `json:"git_commit,omitempty"`
	AppVersion       *string         `json:"app_version,omitempty"`
	HandlerVersion   *string         `json:"handler_version,omitempty"`
	LeaseToken       *string         `json:"lease_token,omitempty"`
	LeaseExpiresAt   *time.Time      `json:"lease_expires_at,omitempty"`
	Status           string          `json:"status"`
	StatusReason     *string         `json:"status_reason,omitempty"`
	DispatchCount    int             `json:"dispatch_count"`
	DispatchedAt     *time.Time      `json:"dispatched_at,omitempty"`
	AckedAt          *time.Time      `json:"acked_at,omitempty"`
	StartedAt        *time.Time      `json:"started_at,omitempty"`
	FinishedAt       *time.Time      `json:"finished_at,omitempty"`
	DeadlineAt       *time.Time      `json:"deadline_at,omitempty"`
	ProgressPercent  *float64        `json:"progress_percent,omitempty"`
	ProgressMessage  *string         `json:"progress_message,omitempty"`
	CancelState      string          `json:"cancel_state"`
	Result           json.RawMessage `json:"result,omitempty"`
	ErrorCode        *string         `json:"error_code,omitempty"`
	ErrorMessage     *string         `json:"error_message,omitempty"`
	LastLogSeq       int64           `json:"last_log_seq"`
	CreatedAt        time.Time       `json:"created_at"`
}

type CandidateInstance struct {
	InstanceID     string
	Hostname       string
	GitCommit      string
	AppVersion     string
	HandlerVersion string
	HandlerMajor   int
	RunningCount   int
	MaxConcurrency int
}

func (q *Queries) GetExecutionByIdempotency(ctx context.Context, taskID int64, key string) (*Execution, error) {
	row := q.db.QueryRowContext(ctx, `SELECT id, execution_id, task_id, service_id, task_code, trigger_type, triggered_by, params, idempotency_key,
		status, status_reason, handler_version, handler_major, schema_hash, timeout_seconds, retry_policy, max_attempts, attempt_count,
		current_attempt_id, started_at, finished_at, result, error_code, error_message, created_at, updated_at
		FROM job_execution WHERE task_id=? AND idempotency_key=?`, taskID, key)
	return scanExecution(row)
}

func (q *Queries) GetExecution(ctx context.Context, executionID string) (*Execution, error) {
	row := q.db.QueryRowContext(ctx, `SELECT id, execution_id, task_id, service_id, task_code, trigger_type, triggered_by, params, idempotency_key,
		status, status_reason, handler_version, handler_major, schema_hash, timeout_seconds, retry_policy, max_attempts, attempt_count,
		current_attempt_id, started_at, finished_at, result, error_code, error_message, created_at, updated_at
		FROM job_execution WHERE execution_id=?`, executionID)
	return scanExecution(row)
}

func scanExecution(sc interface{ Scan(...any) error }) (*Execution, error) {
	var e Execution
	var params, retry, result []byte
	var idem, reason, hv, sh, cur, ec, em sql.NullString
	var hm sql.NullInt64
	var st, ft sql.NullTime
	err := sc.Scan(&e.ID, &e.ExecutionID, &e.TaskID, &e.ServiceID, &e.TaskCode, &e.TriggerType, &e.TriggeredBy, &params, &idem,
		&e.Status, &reason, &hv, &hm, &sh, &e.TimeoutSeconds, &retry, &e.MaxAttempts, &e.AttemptCount,
		&cur, &st, &ft, &result, &ec, &em, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	e.Params, e.RetryPolicy, e.Result = params, retry, result
	e.IdempotencyKey, e.StatusReason = ptrStr(idem), ptrStr(reason)
	e.HandlerVersion, e.SchemaHash, e.CurrentAttemptID = ptrStr(hv), ptrStr(sh), ptrStr(cur)
	e.ErrorCode, e.ErrorMessage = ptrStr(ec), ptrStr(em)
	e.StartedAt, e.FinishedAt = ptrTime(st), ptrTime(ft)
	if hm.Valid {
		v := int(hm.Int64)
		e.HandlerMajor = &v
	}
	e.CreatedAt, e.UpdatedAt = e.CreatedAt.UTC(), e.UpdatedAt.UTC()
	return &e, nil
}

func (q *Queries) InsertExecution(ctx context.Context, e *Execution) error {
	_, err := q.db.ExecContext(ctx, `INSERT INTO job_execution(execution_id, task_id, service_id, task_code, trigger_type, triggered_by, trigger_context,
		params, idempotency_key, status, handler_version, handler_major, schema_hash, timeout_seconds, retry_policy, max_attempts,
		attempt_count, scheduled_at, queued_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,UTC_TIMESTAMP(3))`,
		e.ExecutionID, e.TaskID, e.ServiceID, e.TaskCode, e.TriggerType, e.TriggeredBy, nil,
		nullJSON(e.Params), e.IdempotencyKey, e.Status, e.HandlerVersion, e.HandlerMajor, e.SchemaHash, e.TimeoutSeconds,
		string(e.RetryPolicy), e.MaxAttempts, e.AttemptCount, time.Now().UTC())
	return err
}

func nullJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

func (q *Queries) InsertAttempt(ctx context.Context, a *Attempt) error {
	_, err := q.db.ExecContext(ctx, `INSERT INTO job_execution_attempt(attempt_id, execution_id, attempt_number, task_id, service_id, instance_id,
		hostname, git_commit, app_version, handler_version, lease_token, lease_expires_at, status, dispatch_count, dispatched_at, deadline_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.AttemptID, a.ExecutionID, a.AttemptNumber, a.TaskID, a.ServiceID, a.InstanceID,
		a.Hostname, a.GitCommit, a.AppVersion, a.HandlerVersion, a.LeaseToken, a.LeaseExpiresAt, a.Status, a.DispatchCount, a.DispatchedAt, a.DeadlineAt)
	return err
}

func (q *Queries) SetExecutionCurrentAttempt(ctx context.Context, executionID, attemptID, status string, attemptCount int) error {
	_, err := q.db.ExecContext(ctx, `UPDATE job_execution SET current_attempt_id=?, status=?, attempt_count=?, next_attempt_at=NULL WHERE execution_id=?`,
		attemptID, status, attemptCount, executionID)
	return err
}

func (q *Queries) GetAttempt(ctx context.Context, attemptID string) (*Attempt, error) {
	row := q.db.QueryRowContext(ctx, `SELECT attempt_id, execution_id, attempt_number, task_id, service_id, instance_id, hostname, git_commit, app_version,
		handler_version, lease_token, lease_expires_at, status, status_reason, dispatch_count, dispatched_at, acked_at, started_at, finished_at, deadline_at,
		progress_percent, progress_message, cancel_state, result, error_code, error_message, last_log_seq, created_at
		FROM job_execution_attempt WHERE attempt_id=?`, attemptID)
	return scanAttempt(row)
}

func scanAttempt(sc interface{ Scan(...any) error }) (*Attempt, error) {
	var a Attempt
	var inst, host, gc, av, hv, tok, reason, pm, ec, em sql.NullString
	var lease, disp, ack, st, ft, dl sql.NullTime
	var pct sql.NullFloat64
	var result []byte
	err := sc.Scan(&a.AttemptID, &a.ExecutionID, &a.AttemptNumber, &a.TaskID, &a.ServiceID, &inst, &host, &gc, &av,
		&hv, &tok, &lease, &a.Status, &reason, &a.DispatchCount, &disp, &ack, &st, &ft, &dl,
		&pct, &pm, &a.CancelState, &result, &ec, &em, &a.LastLogSeq, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.InstanceID, a.Hostname, a.GitCommit, a.AppVersion = ptrStr(inst), ptrStr(host), ptrStr(gc), ptrStr(av)
	a.HandlerVersion, a.LeaseToken, a.StatusReason = ptrStr(hv), ptrStr(tok), ptrStr(reason)
	a.LeaseExpiresAt, a.DispatchedAt, a.AckedAt = ptrTime(lease), ptrTime(disp), ptrTime(ack)
	a.StartedAt, a.FinishedAt, a.DeadlineAt = ptrTime(st), ptrTime(ft), ptrTime(dl)
	a.ProgressMessage, a.ErrorCode, a.ErrorMessage = ptrStr(pm), ptrStr(ec), ptrStr(em)
	a.Result = result
	if pct.Valid {
		v := pct.Float64
		a.ProgressPercent = &v
	}
	a.CreatedAt = a.CreatedAt.UTC()
	return &a, nil
}

func (q *Queries) ListAttempts(ctx context.Context, executionID string) ([]*Attempt, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT attempt_id, execution_id, attempt_number, task_id, service_id, instance_id, hostname, git_commit, app_version,
		handler_version, lease_token, lease_expires_at, status, status_reason, dispatch_count, dispatched_at, acked_at, started_at, finished_at, deadline_at,
		progress_percent, progress_message, cancel_state, result, error_code, error_message, last_log_seq, created_at
		FROM job_execution_attempt WHERE execution_id=? ORDER BY attempt_number`, executionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Attempt
	for rows.Next() {
		a, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (q *Queries) PickCandidate(ctx context.Context, definitionID int64, handlerMajor int) (*CandidateInstance, error) {
	row := q.db.QueryRowContext(ctx, `SELECT i.instance_id, COALESCE(i.hostname,''), COALESCE(i.git_commit,''), COALESCE(i.app_version,''),
		it.handler_version, it.handler_major, i.running_count, i.max_concurrency
		FROM job_instance_task it
		JOIN job_instance i ON i.id=it.instance_pk
		WHERE it.definition_id=? AND it.handler_major=? AND i.status IN ('ONLINE','DRAINING')
		AND i.running_count < i.max_concurrency
		ORDER BY i.running_count ASC, i.last_heartbeat_at DESC LIMIT 1`, definitionID, handlerMajor)
	var c CandidateInstance
	err := row.Scan(&c.InstanceID, &c.Hostname, &c.GitCommit, &c.AppVersion, &c.HandlerVersion, &c.HandlerMajor, &c.RunningCount, &c.MaxConcurrency)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &c, err
}

func (q *Queries) MarkAttemptAcked(ctx context.Context, attemptID string) error {
	_, err := q.db.ExecContext(ctx, `UPDATE job_execution_attempt SET status='ACKNOWLEDGED', acked_at=UTC_TIMESTAMP(3), version=version+1 WHERE attempt_id=? AND status IN ('CREATED','DISPATCHING')`, attemptID)
	return err
}

func (q *Queries) MarkAttemptStarted(ctx context.Context, attemptID string, startedAt time.Time) error {
	_, err := q.db.ExecContext(ctx, `UPDATE job_execution_attempt SET status='RUNNING', started_at=?, version=version+1 WHERE attempt_id=? AND status IN ('CREATED','DISPATCHING','ACKNOWLEDGED')`, startedAt, attemptID)
	return err
}

func (q *Queries) MarkExecutionStarted(ctx context.Context, executionID string, startedAt time.Time) error {
	_, err := q.db.ExecContext(ctx, `UPDATE job_execution SET status='RUNNING', started_at=COALESCE(started_at, ?) WHERE execution_id=? AND status IN ('PENDING','QUEUED')`, startedAt, executionID)
	return err
}

func (q *Queries) UpdateProgress(ctx context.Context, attemptID string, pct *float64, msg string) error {
	_, err := q.db.ExecContext(ctx, `UPDATE job_execution_attempt SET progress_percent=?, progress_message=?, progress_updated_at=UTC_TIMESTAMP(3) WHERE attempt_id=? AND status='RUNNING'`, pct, nullStr(msg), attemptID)
	return err
}

func (q *Queries) InsertLogs(ctx context.Context, executionID, attemptID string, entries []struct {
	Seq int64; Level, Stream, Message string; Fields json.RawMessage; LoggedAt time.Time
}) (int64, error) {
	var maxSeq int64
	for _, e := range entries {
		fields := any(nil)
		if len(e.Fields) > 0 {
			fields = string(e.Fields)
		}
		_, err := q.db.ExecContext(ctx, `INSERT INTO job_execution_log(execution_id, attempt_id, sequence, level, stream, message, fields, logged_at)
			VALUES (?,?,?,?,?,?,?,?)`,
			executionID, attemptID, e.Seq, e.Level, nullStr(e.Stream), e.Message, fields, e.LoggedAt)
		if err != nil {
			return maxSeq, err
		}
		if e.Seq > maxSeq {
			maxSeq = e.Seq
		}
	}
	if maxSeq > 0 {
		_, _ = q.db.ExecContext(ctx, `UPDATE job_execution_attempt SET last_log_seq=GREATEST(last_log_seq, ?) WHERE attempt_id=?`, maxSeq, attemptID)
	}
	return maxSeq, nil
}

func (q *Queries) FinalizeAttempt(ctx context.Context, attemptID, status, reason string, finishedAt time.Time, result json.RawMessage, errCode, errMsg string, cancelState string) error {
	_, err := q.db.ExecContext(ctx, `UPDATE job_execution_attempt SET status=?, status_reason=?, finished_at=?, result=?, error_code=?, error_message=?,
		cancel_state=IF(?='', cancel_state, ?), version=version+1 WHERE attempt_id=? AND status NOT IN ('SUCCEEDED','FAILED','CANCELLED','TIMED_OUT')`,
		status, nullStr(reason), finishedAt, nullJSON(result), nullStr(errCode), nullStr(errMsg), cancelState, cancelState, attemptID)
	return err
}

func (q *Queries) FinalizeExecution(ctx context.Context, executionID, status, reason string, finishedAt time.Time, result json.RawMessage, errCode, errMsg string) error {
	_, err := q.db.ExecContext(ctx, `UPDATE job_execution SET status=?, status_reason=?, finished_at=?, result=?, error_code=?, error_message=?, concurrency_slot=NULL, version=version+1
		WHERE execution_id=? AND status NOT IN ('SUCCEEDED','FAILED','CANCELLED','TIMED_OUT','SKIPPED')`,
		status, nullStr(reason), finishedAt, nullJSON(result), nullStr(errCode), nullStr(errMsg), executionID)
	return err
}

func (q *Queries) InsertEvent(ctx context.Context, executionID, attemptID, eventType, from, to, actorType, actorID, instanceID string, payload any) error {
	var p any
	if payload != nil {
		b, _ := json.Marshal(payload)
		p = string(b)
	}
	_, err := q.db.ExecContext(ctx, `INSERT INTO job_execution_event(execution_id, attempt_id, event_type, from_status, to_status, actor_type, actor_id, instance_id, payload)
		VALUES (?,?,?,?,?,?,?,?,?)`, executionID, nullStr(attemptID), eventType, nullStr(from), nullStr(to), actorType, nullStr(actorID), nullStr(instanceID), p)
	return err
}

func (q *Queries) SetCancelState(ctx context.Context, attemptID, state string) error {
	_, err := q.db.ExecContext(ctx, `UPDATE job_execution_attempt SET cancel_state=?, cancel_requested_at=COALESCE(cancel_requested_at, UTC_TIMESTAMP(3)),
		cancel_acked_at=IF(? IN ('ACKED','STOPPED'), UTC_TIMESTAMP(3), cancel_acked_at) WHERE attempt_id=?`, state, state, attemptID)
	return err
}

func (q *Queries) MarkExecutionCancelRequested(ctx context.Context, executionID, by string) error {
	_, err := q.db.ExecContext(ctx, `UPDATE job_execution SET cancel_requested_at=UTC_TIMESTAMP(3), cancel_requested_by=? WHERE execution_id=?`, by, executionID)
	return err
}

func (q *Queries) ListExecutions(ctx context.Context, taskID, serviceID int64, status string, limit int) ([]*Execution, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	where, args := " WHERE 1=1", []any{}
	if taskID > 0 {
		where += " AND task_id=?"
		args = append(args, taskID)
	}
	if serviceID > 0 {
		where += " AND service_id=?"
		args = append(args, serviceID)
	}
	if status != "" {
		where += " AND status=?"
		args = append(args, status)
	}
	args = append(args, limit)
	rows, err := q.db.QueryContext(ctx, `SELECT id, execution_id, task_id, service_id, task_code, trigger_type, triggered_by, params, idempotency_key,
		status, status_reason, handler_version, handler_major, schema_hash, timeout_seconds, retry_policy, max_attempts, attempt_count,
		current_attempt_id, started_at, finished_at, result, error_code, error_message, created_at, updated_at
		FROM job_execution`+where+` ORDER BY created_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Execution
	for rows.Next() {
		e, err := scanExecution(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (q *Queries) ListLogs(ctx context.Context, executionID, attemptID string, afterSeq int64, limit int) ([]map[string]any, int64, error) {
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	rows, err := q.db.QueryContext(ctx, `SELECT sequence, level, stream, message, fields, logged_at FROM job_execution_log
		WHERE execution_id=? AND (?='' OR attempt_id=?) AND sequence>? ORDER BY sequence LIMIT ?`,
		executionID, attemptID, attemptID, afterSeq, limit)
	if err != nil {
		return nil, afterSeq, err
	}
	defer rows.Close()
	var items []map[string]any
	var next = afterSeq
	for rows.Next() {
		var seq int64
		var level, msg string
		var stream sql.NullString
		var fields []byte
		var logged time.Time
		if err := rows.Scan(&seq, &level, &stream, &msg, &fields, &logged); err != nil {
			return nil, next, err
		}
		item := map[string]any{"seq": seq, "level": level, "message": msg, "logged_at": logged.UTC()}
		if stream.Valid {
			item["stream"] = stream.String
		}
		if len(fields) > 0 {
			item["fields"] = json.RawMessage(fields)
		}
		items = append(items, item)
		next = seq
	}
	return items, next, rows.Err()
}
