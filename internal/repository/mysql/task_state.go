package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// TaskState 任务持久化游标/状态；未建行时逻辑视图为 state={}, version=0。
type TaskState struct {
	State           json.RawMessage `json:"state"`
	Version         uint64          `json:"version"`
	UpdatedBy       *string         `json:"updated_by"`
	LastExecutionID *string         `json:"last_execution_id"`
	LastAttemptID   *string         `json:"last_attempt_id"`
	UpdatedAt       *time.Time      `json:"updated_at"`
}

func emptyTaskState() *TaskState {
	return &TaskState{
		State:   json.RawMessage(`{}`),
		Version: 0,
	}
}

func scanTaskState(sc interface{ Scan(...any) error }) (*TaskState, error) {
	var ts TaskState
	var raw []byte
	var ub, le, la sql.NullString
	var ua sql.NullTime
	if err := sc.Scan(&raw, &ts.Version, &ub, &le, &la, &ua); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		ts.State = json.RawMessage(`{}`)
	} else {
		ts.State = json.RawMessage(raw)
	}
	ts.UpdatedBy = ptrStr(ub)
	ts.LastExecutionID = ptrStr(le)
	ts.LastAttemptID = ptrStr(la)
	ts.UpdatedAt = ptrTime(ua)
	return &ts, nil
}

func (q *Queries) TaskExists(ctx context.Context, taskID int64) (bool, error) {
	var id int64
	err := q.db.QueryRowContext(ctx, "SELECT id FROM job_definition WHERE id=?", taskID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// GetTaskState 获取任务持久化状态；未初始化时返回逻辑视图 {state:{}, version:0}。
func (q *Queries) GetTaskState(ctx context.Context, taskID int64) (*TaskState, error) {
	ts, err := scanTaskState(q.db.QueryRowContext(ctx, `SELECT state, version, updated_by, last_execution_id, last_attempt_id, updated_at
		FROM job_task_state WHERE task_id=?`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return emptyTaskState(), nil
	}
	return ts, err
}

// GetTaskStateStrict 先确认任务存在，不存在返回 ErrNotFound；未初始化返回逻辑视图。
func (q *Queries) GetTaskStateStrict(ctx context.Context, taskID int64) (*TaskState, error) {
	ok, err := q.TaskExists(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	return q.GetTaskState(ctx, taskID)
}

// GetTaskStateForUpdate 在事务中锁定任务状态行；若尚未初始化返回 (emptyTaskState(), false, nil)。
func (q *Queries) GetTaskStateForUpdate(ctx context.Context, taskID int64) (*TaskState, bool, error) {
	ts, err := scanTaskState(q.db.QueryRowContext(ctx, `SELECT state, version, updated_by, last_execution_id, last_attempt_id, updated_at
		FROM job_task_state WHERE task_id=? FOR UPDATE`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return emptyTaskState(), false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return ts, true, nil
}

// UpsertTaskStateByAttempt 在事务内由有效 Attempt 推进任务状态（首次 version=1，后续 version+1）。
func (q *Queries) UpsertTaskStateByAttempt(ctx context.Context, taskID int64, state json.RawMessage, executionID, attemptID string) (uint64, error) {
	if len(state) == 0 {
		state = json.RawMessage(`{}`)
	}
	updatedBy := "attempt:" + attemptID
	_, err := q.db.ExecContext(ctx, `INSERT INTO job_task_state(task_id, state, version, updated_by, last_execution_id, last_attempt_id, updated_at)
		VALUES (?, ?, 1, ?, ?, ?, UTC_TIMESTAMP(3))
		ON DUPLICATE KEY UPDATE
			state = VALUES(state),
			version = version + 1,
			updated_by = VALUES(updated_by),
			last_execution_id = VALUES(last_execution_id),
			last_attempt_id = VALUES(last_attempt_id),
			updated_at = UTC_TIMESTAMP(3)`,
		taskID, string(state), updatedBy, executionID, attemptID)
	if err != nil {
		return 0, err
	}
	var ver uint64
	if err := q.db.QueryRowContext(ctx, `SELECT version FROM job_task_state WHERE task_id=?`, taskID).Scan(&ver); err != nil {
		return 0, err
	}
	return ver, nil
}

// GetAttemptForUpdate 在事务内锁定 Attempt 行并返回数据库侧租约是否已过期。
func (q *Queries) GetAttemptForUpdate(ctx context.Context, attemptID string) (*Attempt, bool, error) {
	row := q.db.QueryRowContext(ctx, `SELECT attempt_id, execution_id, attempt_number, task_id, service_id, instance_id,
		status, lease_token, lease_expires_at, started_at, finished_at, last_log_seq, cancel_state,
		CASE WHEN lease_expires_at IS NOT NULL AND lease_expires_at < UTC_TIMESTAMP(3) THEN 1 ELSE 0 END
		FROM job_execution_attempt WHERE attempt_id=? FOR UPDATE`, attemptID)
	var a Attempt
	var inst, tok sql.NullString
	var exp, st, fn sql.NullTime
	var expired int
	err := row.Scan(&a.AttemptID, &a.ExecutionID, &a.AttemptNumber, &a.TaskID, &a.ServiceID, &inst,
		&a.Status, &tok, &exp, &st, &fn, &a.LastLogSeq, &a.CancelState, &expired)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, ErrNotFound
	}
	if err != nil {
		return nil, false, err
	}
	a.InstanceID, a.LeaseToken = ptrStr(inst), ptrStr(tok)
	a.LeaseExpiresAt, a.StartedAt, a.FinishedAt = ptrTime(exp), ptrTime(st), ptrTime(fn)
	return &a, expired == 1, nil
}

// GetExecutionForUpdate 在事务内锁定 Execution 行。
func (q *Queries) GetExecutionForUpdate(ctx context.Context, executionID string) (*Execution, error) {
	row := q.db.QueryRowContext(ctx, `SELECT id, execution_id, task_id, service_id, task_code, trigger_type, triggered_by, trigger_context, params, idempotency_key,
		status, status_reason, handler_version, handler_major, schema_hash, timeout_seconds, retry_policy, max_attempts, attempt_count,
		current_attempt_id, started_at, finished_at, result, error_code, error_message, created_at, updated_at
		FROM job_execution WHERE execution_id=? FOR UPDATE`, executionID)
	return scanExecution(row)
}

// UpdateTaskStateAdminCAS 管理端覆盖更新任务状态；expectedVersion 非空时做乐观锁校验。
func (s *Store) UpdateTaskStateAdminCAS(ctx context.Context, taskID int64, newState json.RawMessage, expectedVersion *uint64, operatorID string) (*TaskState, *TaskState, error) {
	if len(newState) == 0 {
		newState = json.RawMessage(`{}`)
	}
	var before, after *TaskState
	err := s.InTx(ctx, func(q *Queries) error {
		var defID int64
		if err := q.db.QueryRowContext(ctx, "SELECT id FROM job_definition WHERE id=? FOR SHARE", taskID).Scan(&defID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		cur, exists, err := q.GetTaskStateForUpdate(ctx, taskID)
		if err != nil {
			return err
		}
		before = cur
		if expectedVersion != nil && *expectedVersion != cur.Version {
			return ErrVersionConflict
		}
		updatedBy := "admin:" + operatorID
		if !exists {
			if _, err := q.db.ExecContext(ctx, `INSERT INTO job_task_state(task_id, state, version, updated_by, last_execution_id, last_attempt_id, updated_at)
				VALUES (?, ?, 1, ?, NULL, NULL, UTC_TIMESTAMP(3))`, taskID, string(newState), updatedBy); err != nil {
				return err
			}
		} else {
			if _, err := q.db.ExecContext(ctx, `UPDATE job_task_state SET state=?, version=version+1, updated_by=?, updated_at=UTC_TIMESTAMP(3) WHERE task_id=?`,
				string(newState), updatedBy, taskID); err != nil {
				return err
			}
		}
		after, err = q.GetTaskState(ctx, taskID)
		return err
	})
	return before, after, err
}

// ResetTaskStateAdminCAS 管理端将任务状态重置为 {}，且 version 继续 +1 避免 ABA；expectedVersion 非空时做乐观锁校验。
func (s *Store) ResetTaskStateAdminCAS(ctx context.Context, taskID int64, expectedVersion *uint64, operatorID string) (*TaskState, *TaskState, error) {
	var before, after *TaskState
	err := s.InTx(ctx, func(q *Queries) error {
		var defID int64
		if err := q.db.QueryRowContext(ctx, "SELECT id FROM job_definition WHERE id=? FOR SHARE", taskID).Scan(&defID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		cur, exists, err := q.GetTaskStateForUpdate(ctx, taskID)
		if err != nil {
			return err
		}
		before = cur
		if expectedVersion != nil && *expectedVersion != cur.Version {
			return ErrVersionConflict
		}
		updatedBy := "admin:" + operatorID
		if !exists {
			if _, err := q.db.ExecContext(ctx, `INSERT INTO job_task_state(task_id, state, version, updated_by, last_execution_id, last_attempt_id, updated_at)
				VALUES (?, '{}', 1, ?, NULL, NULL, UTC_TIMESTAMP(3))`, taskID, updatedBy); err != nil {
				return err
			}
		} else {
			if _, err := q.db.ExecContext(ctx, `UPDATE job_task_state SET state='{}', version=version+1, updated_by=?, last_execution_id=NULL, last_attempt_id=NULL, updated_at=UTC_TIMESTAMP(3) WHERE task_id=?`,
				updatedBy, taskID); err != nil {
				return err
			}
		}
		after, err = q.GetTaskState(ctx, taskID)
		return err
	})
	return before, after, err
}
