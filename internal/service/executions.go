package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/GlassJob/job-center/internal/model"
	"github.com/GlassJob/job-center/internal/pkg/idgen"
	"github.com/GlassJob/job-center/internal/repository/mysql"
	"github.com/GlassJob/job-center/pkg/protocol"
)

// TaskDispatcher 向在线实例下发中心帧（由 gateway.Hub 实现）。
type TaskDispatcher interface {
	Send(instanceID, typ string, data any) error
}

type Executions struct {
	store      *mysql.Store
	dispatcher TaskDispatcher
	leaseTTL   time.Duration
}

func NewExecutions(store *mysql.Store, dispatcher TaskDispatcher, leaseTTL time.Duration) *Executions {
	if leaseTTL <= 0 {
		leaseTTL = 30 * time.Second
	}
	return &Executions{store: store, dispatcher: dispatcher, leaseTTL: leaseTTL}
}

type TriggerExecutionInput struct {
	Params         json.RawMessage `json:"params"`
	IdempotencyKey string          `json:"idempotency_key"`
	InstanceID     string          `json:"instance_id"`
}

type TriggerExecutionResult struct {
	ExecutionID string `json:"execution_id"`
	Status      string `json:"status"`
	AttemptID   string `json:"attempt_id,omitempty"`
	InstanceID  string `json:"instance_id,omitempty"`
	Message     string `json:"message,omitempty"`
	Replay      bool   `json:"idempotent_replay,omitempty"`
}

func (s *Executions) ManualTrigger(ctx context.Context, serviceID, jobID, triggeredBy string, in TriggerExecutionInput) (*TriggerExecutionResult, error) {
	job, err := s.store.GetJob(ctx, serviceID, jobID)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if job.Status != model.JobStatusActive || !job.Enabled {
		return nil, fmt.Errorf("%w: job not active", ErrInvalidInput)
	}

	if key := strings.TrimSpace(in.IdempotencyKey); key != "" {
		if existing, err := s.store.GetExecutionByJobIdempotency(ctx, job.ID, key); err == nil {
			out := &TriggerExecutionResult{ExecutionID: existing.ID, Status: existing.Status, Replay: true}
			if existing.CurrentAttemptID != nil {
				out.AttemptID = *existing.CurrentAttemptID
			}
			return out, nil
		} else if !errors.Is(err, mysql.ErrNotFound) {
			return nil, err
		}
	}

	params := in.Params
	if len(params) == 0 || string(params) == "null" {
		params = json.RawMessage(`{}`)
	}
	timeout := job.DefaultTimeoutSeconds
	if timeout == 0 {
		timeout = 300
	}
	var major *uint
	if job.HandlerVersion != "" {
		if m, ok := parseMajor(job.HandlerVersion); ok {
			major = &m
		}
	}
	now := time.Now().UTC()
	var idem *string
	if k := strings.TrimSpace(in.IdempotencyKey); k != "" {
		idem = &k
	}
	exec := &model.Execution{
		ID:             idgen.New(),
		ServiceID:      serviceID,
		JobID:          job.ID,
		JobCode:        job.Code,
		TriggerType:    model.TriggerManual,
		TriggeredBy:    triggeredBy,
		Params:         params,
		PolicySnapshot: map[string]any{"max_attempts": 1, "timeout_seconds": timeout},
		IdempotencyKey: idem,
		Status:         model.ExecStatusPending,
		HandlerVersion: job.HandlerVersion,
		HandlerMajor:   major,
		SchemaHash:     job.SchemaHash,
		TimeoutSeconds: timeout,
		MaxAttempts:    1,
		ScheduledAt:    &now,
	}
	if err := s.store.CreateExecution(ctx, exec); err != nil {
		if isDup(err) && idem != nil {
			existing, e2 := s.store.GetExecutionByJobIdempotency(ctx, job.ID, *idem)
			if e2 == nil {
				return &TriggerExecutionResult{ExecutionID: existing.ID, Status: existing.Status, Replay: true}, nil
			}
		}
		return nil, err
	}

	inst, err := s.store.PickOnlineInstanceForJob(ctx, serviceID, job.ID, strings.TrimSpace(in.InstanceID))
	if errors.Is(err, mysql.ErrNotFound) {
		qAt := now
		_ = s.store.UpdateExecutionFields(ctx, exec.ID, map[string]any{
			"status":    model.ExecStatusQueued,
			"queued_at": qAt,
		})
		return &TriggerExecutionResult{
			ExecutionID: exec.ID,
			Status:      model.ExecStatusQueued,
			Message:     "no online instance with capacity; queued",
		}, nil
	}
	if err != nil {
		return nil, err
	}

	attempt, err := s.dispatch(ctx, exec, job, inst)
	if err != nil {
		return nil, err
	}
	return &TriggerExecutionResult{
		ExecutionID: exec.ID,
		Status:      model.ExecStatusDispatching,
		AttemptID:   attempt.ID,
		InstanceID:  inst.ID,
	}, nil
}

func (s *Executions) Get(ctx context.Context, serviceID, jobID, executionID string) (*model.ExecutionView, error) {
	e, err := s.store.GetExecution(ctx, executionID)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if e.ServiceID != serviceID || e.JobID != jobID {
		return nil, ErrNotFound
	}
	var att *model.Attempt
	if e.CurrentAttemptID != nil && *e.CurrentAttemptID != "" {
		att, _ = s.store.GetAttempt(ctx, *e.CurrentAttemptID)
	}
	v := e.View(att)
	return &v, nil
}

type CancelExecutionInput struct {
	Reason string `json:"reason"`
}

type CancelExecutionResult struct {
	ExecutionID string `json:"execution_id"`
	Status      string `json:"status"`
	AttemptID   string `json:"attempt_id,omitempty"`
	CancelState string `json:"cancel_state,omitempty"`
	Message     string `json:"message,omitempty"`
	Idempotent  bool   `json:"idempotent,omitempty"`
}

func (s *Executions) Cancel(ctx context.Context, serviceID, jobID, executionID, requestedBy string, in CancelExecutionInput) (*CancelExecutionResult, error) {
	e, err := s.store.GetExecution(ctx, executionID)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if e.ServiceID != serviceID || e.JobID != jobID {
		return nil, ErrNotFound
	}

	var att *model.Attempt
	if e.CurrentAttemptID != nil && *e.CurrentAttemptID != "" {
		att, _ = s.store.GetAttempt(ctx, *e.CurrentAttemptID)
	}

	switch e.Status {
	case model.ExecStatusSucceeded, model.ExecStatusFailed, model.ExecStatusCancelled,
		model.ExecStatusTimedOut, model.ExecStatusSkipped:
		return nil, fmt.Errorf("%w: execution already finished (%s)", ErrConflict, e.Status)
	}

	if e.CancelRequestedAt != nil || (att != nil && att.CancelState != "" && att.CancelState != model.CancelNone) {
		out := &CancelExecutionResult{
			ExecutionID: e.ID,
			Status:      e.Status,
			Idempotent:  true,
			Message:     "cancel already requested",
		}
		if att != nil {
			out.AttemptID = att.ID
			out.CancelState = att.CancelState
		}
		return out, nil
	}

	now := time.Now().UTC()
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		reason = "ADMIN_CANCEL"
	}
	by := strings.TrimSpace(requestedBy)
	if by == "" {
		by = "admin"
	}

	// 未派发或无实例：直接取消。
	if e.Status == model.ExecStatusQueued || e.Status == model.ExecStatusPending ||
		att == nil || att.InstanceID == nil || *att.InstanceID == "" || att.LeaseToken == "" {
		fields := map[string]any{
			"status":              model.ExecStatusCancelled,
			"finished_at":         now,
			"status_reason":       reason,
			"cancel_requested_at": now,
			"cancel_requested_by": by,
		}
		if err := s.store.UpdateExecutionFields(ctx, e.ID, fields); err != nil {
			return nil, err
		}
		if att != nil {
			_ = s.store.UpdateAttemptFields(ctx, att.ID, map[string]any{
				"status":              model.AttemptCancelled,
				"finished_at":         now,
				"status_reason":       reason,
				"cancel_state":        model.CancelStopped,
				"cancel_requested_at": now,
			})
		}
		out := &CancelExecutionResult{
			ExecutionID: e.ID,
			Status:      model.ExecStatusCancelled,
			Message:     "cancelled without dispatch",
			CancelState: model.CancelStopped,
		}
		if att != nil {
			out.AttemptID = att.ID
		}
		return out, nil
	}

	_ = s.store.UpdateExecutionFields(ctx, e.ID, map[string]any{
		"cancel_requested_at": now,
		"cancel_requested_by": by,
		"status_reason":       reason,
	})
	_ = s.store.UpdateAttemptFields(ctx, att.ID, map[string]any{
		"cancel_state":        model.CancelRequested,
		"cancel_requested_at": now,
	})

	msg := protocol.TaskCancel{
		ExecutionID: e.ID,
		AttemptID:   att.ID,
		LeaseToken:  att.LeaseToken,
		Reason:      reason,
	}
	sent := model.CancelRequested
	if s.dispatcher != nil {
		if err := s.dispatcher.Send(*att.InstanceID, protocol.TypeTaskCancel, msg); err != nil {
			_ = s.store.UpdateAttemptFields(ctx, att.ID, map[string]any{
				"status_reason": "CANCEL_SEND_FAILED",
			})
		} else {
			sent = model.CancelSent
			_ = s.store.UpdateAttemptFields(ctx, att.ID, map[string]any{
				"cancel_state": model.CancelSent,
			})
		}
	}

	return &CancelExecutionResult{
		ExecutionID: e.ID,
		Status:      e.Status,
		AttemptID:   att.ID,
		CancelState: sent,
		Message:     "cancel dispatched",
	}, nil
}

func (s *Executions) OnCancelAck(ctx context.Context, instanceID string, msg protocol.TaskCancelAck) *protocol.TaskResultAck {
	a, err := s.loadAndCheckLease(ctx, msg.AttemptID, msg.LeaseToken, instanceID)
	if err != nil {
		return rejectAck(msg.AttemptID, err)
	}
	now := time.Now().UTC()
	_ = s.store.UpdateAttemptFields(ctx, a.ID, map[string]any{
		"cancel_state":    model.CancelAcked,
		"cancel_acked_at": now,
	})
	return okAck(msg.AttemptID)
}

func (s *Executions) OnCancelled(ctx context.Context, instanceID string, msg protocol.TaskCancelled) *protocol.TaskResultAck {
	a, err := s.loadAndCheckLease(ctx, msg.AttemptID, msg.LeaseToken, instanceID)
	if err != nil {
		return rejectAck(msg.AttemptID, err)
	}
	finished := parseProtoTime(msg.FinishedAt)
	wasRunning := a.Status == model.AttemptRunning
	fields := map[string]any{
		"status":        model.AttemptCancelled,
		"finished_at":   finished,
		"cancel_state":  model.CancelStopped,
		"status_reason": msg.StoppedBy,
	}
	if a.StartedAt != nil {
		ms := uint64(finished.Sub(*a.StartedAt).Milliseconds())
		fields["duration_ms"] = ms
	}
	_ = s.store.UpdateAttemptFields(ctx, a.ID, fields)
	_ = s.store.UpdateExecutionFields(ctx, a.ExecutionID, map[string]any{
		"status":        model.ExecStatusCancelled,
		"finished_at":   finished,
		"status_reason": cancelReason(msg.StoppedBy, "CANCELLED"),
	})
	if wasRunning && a.InstanceID != nil {
		_ = s.store.AdjustInstanceRunning(ctx, *a.InstanceID, -1)
	}
	return okAck(msg.AttemptID)
}

func cancelReason(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func (s *Executions) dispatch(ctx context.Context, exec *model.Execution, job *model.Job, inst *model.Instance) (*model.Attempt, error) {
	token, err := randomLeaseToken()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	leaseExp := now.Add(s.leaseTTL)
	deadline := now.Add(time.Duration(exec.TimeoutSeconds) * time.Second)
	attemptNo := exec.AttemptCount + 1
	if attemptNo == 0 {
		attemptNo = 1
	}
	instID := inst.ID
	a := &model.Attempt{
		ID:             idgen.New(),
		ExecutionID:    exec.ID,
		AttemptNo:      attemptNo,
		JobID:          job.ID,
		ServiceID:      exec.ServiceID,
		InstanceID:     &instID,
		InstanceKey:    inst.InstanceKey,
		Hostname:       inst.Hostname,
		GitCommit:      inst.GitCommit,
		AppVersion:     inst.AppVersion,
		HandlerVersion: job.HandlerVersion,
		LeaseToken:     token,
		LeaseExpiresAt: &leaseExp,
		Status:         model.AttemptDispatching,
		DispatchCount:  1,
		DispatchedAt:   &now,
		DeadlineAt:     &deadline,
		CancelState:    model.CancelNone,
	}
	if err := s.store.CreateAttempt(ctx, a); err != nil {
		return nil, err
	}
	if err := s.store.SetExecutionDispatching(ctx, exec.ID, a.ID, attemptNo); err != nil {
		return nil, err
	}
	params := exec.Params
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	msg := protocol.TaskExecute{
		ExecutionID:    exec.ID,
		AttemptID:      a.ID,
		AttemptNumber:  int(attemptNo),
		LeaseToken:     token,
		LeaseExpiresAt: protocol.FormatTime(leaseExp),
		JobCode:        job.Code,
		JobName:        job.Name,
		Params:         params,
		State:          json.RawMessage(`{}`),
		StateVersion:   0,
		TimeoutSeconds: int(exec.TimeoutSeconds),
		DeadlineAt:     protocol.FormatTime(deadline),
		TriggerType:    exec.TriggerType,
		HandlerVersion: job.HandlerVersion,
	}
	if s.dispatcher != nil {
		if err := s.dispatcher.Send(inst.ID, protocol.TypeTaskExecute, msg); err != nil {
			_ = s.store.UpdateAttemptFields(ctx, a.ID, map[string]any{
				"status_reason": "DISPATCH_SEND_FAILED",
			})
			// 仍返回成功创建；连接稍后恢复时可后置重派
		}
	}
	return a, nil
}

func (s *Executions) OnExecuteAck(ctx context.Context, instanceID string, ack protocol.TaskExecuteAck) *protocol.TaskResultAck {
	a, err := s.loadAndCheckLease(ctx, ack.AttemptID, ack.LeaseToken, instanceID)
	if err != nil {
		return rejectAck(ack.AttemptID, err)
	}
	now := time.Now().UTC()
	if !ack.Accepted {
		_ = s.finalizeFailed(ctx, a, now, "REJECTED_BY_EXECUTOR", "REJECTED", "executor rejected", nil)
		return okAck(ack.AttemptID)
	}
	_ = s.store.UpdateAttemptFields(ctx, a.ID, map[string]any{
		"status":   model.AttemptAcknowledged,
		"acked_at": now,
	})
	return okAck(ack.AttemptID)
}

func (s *Executions) OnStarted(ctx context.Context, instanceID string, msg protocol.TaskStarted) *protocol.TaskResultAck {
	a, err := s.loadAndCheckLease(ctx, msg.AttemptID, msg.LeaseToken, instanceID)
	if err != nil {
		return rejectAck(msg.AttemptID, err)
	}
	started := parseProtoTime(msg.StartedAt)
	_ = s.store.UpdateAttemptFields(ctx, a.ID, map[string]any{
		"status":     model.AttemptRunning,
		"started_at": started,
	})
	_ = s.store.UpdateExecutionFields(ctx, a.ExecutionID, map[string]any{
		"status":     model.ExecStatusRunning,
		"started_at": started,
	})
	if a.InstanceID != nil {
		_ = s.store.AdjustInstanceRunning(ctx, *a.InstanceID, 1)
	}
	return okAck(msg.AttemptID)
}

func (s *Executions) OnProgress(ctx context.Context, instanceID string, msg protocol.TaskProgress) *protocol.TaskResultAck {
	if _, err := s.loadAndCheckLease(ctx, msg.AttemptID, msg.LeaseToken, instanceID); err != nil {
		return rejectAck(msg.AttemptID, err)
	}
	now := time.Now().UTC()
	fields := map[string]any{"progress_updated_at": now, "progress_message": msg.Message}
	if msg.Percent != nil {
		fields["progress_percent"] = *msg.Percent
	}
	_ = s.store.UpdateAttemptFields(ctx, msg.AttemptID, fields)
	return okAck(msg.AttemptID)
}

func (s *Executions) OnCompleted(ctx context.Context, instanceID string, msg protocol.TaskCompleted) *protocol.TaskResultAck {
	a, err := s.loadAndCheckLease(ctx, msg.AttemptID, msg.LeaseToken, instanceID)
	if err != nil {
		return rejectAck(msg.AttemptID, err)
	}
	finished := parseProtoTime(msg.FinishedAt)
	result := msg.Result
	if len(result) == 0 {
		result = json.RawMessage(`{}`)
	}
	fields := map[string]any{
		"status":      model.AttemptSucceeded,
		"finished_at": finished,
		"result":      []byte(result),
	}
	if a.StartedAt != nil {
		ms := uint64(finished.Sub(*a.StartedAt).Milliseconds())
		fields["duration_ms"] = ms
	}
	_ = s.store.UpdateAttemptFields(ctx, a.ID, fields)
	_ = s.store.UpdateExecutionFields(ctx, a.ExecutionID, map[string]any{
		"status":        model.ExecStatusSucceeded,
		"finished_at":   finished,
		"result":        []byte(result),
		"result_summary": truncate(string(result), 500),
		"error_code":    "",
		"error_message": "",
	})
	if a.InstanceID != nil {
		_ = s.store.AdjustInstanceRunning(ctx, *a.InstanceID, -1)
	}
	return okAck(msg.AttemptID)
}

func (s *Executions) OnFailed(ctx context.Context, instanceID string, msg protocol.TaskFailed) *protocol.TaskResultAck {
	a, err := s.loadAndCheckLease(ctx, msg.AttemptID, msg.LeaseToken, instanceID)
	if err != nil {
		return rejectAck(msg.AttemptID, err)
	}
	finished := parseProtoTime(msg.FinishedAt)
	code := msg.Error.Code
	if code == "" {
		code = msg.Reason
	}
	if code == "" {
		code = "FAILED"
	}
	_ = s.finalizeFailed(ctx, a, finished, msg.Reason, code, msg.Error.Message, msg.ExitCode)
	return okAck(msg.AttemptID)
}

func (s *Executions) OnLogOrCheckpoint(ctx context.Context, instanceID string, attemptID, leaseToken string) *protocol.TaskResultAck {
	if _, err := s.loadAndCheckLease(ctx, attemptID, leaseToken, instanceID); err != nil {
		return rejectAck(attemptID, err)
	}
	return okAck(attemptID)
}

func (s *Executions) finalizeFailed(ctx context.Context, a *model.Attempt, finished time.Time, reason, errCode, errMsg string, exitCode *int) error {
	fields := map[string]any{
		"status":        model.AttemptFailed,
		"finished_at":   finished,
		"status_reason": reason,
		"error_code":    errCode,
		"error_message": truncate(errMsg, 2000),
	}
	if exitCode != nil {
		fields["exit_code"] = *exitCode
	}
	if a.StartedAt != nil {
		ms := uint64(finished.Sub(*a.StartedAt).Milliseconds())
		fields["duration_ms"] = ms
	}
	_ = s.store.UpdateAttemptFields(ctx, a.ID, fields)
	_ = s.store.UpdateExecutionFields(ctx, a.ExecutionID, map[string]any{
		"status":         model.ExecStatusFailed,
		"finished_at":    finished,
		"status_reason":  reason,
		"error_code":     errCode,
		"error_message":  truncate(errMsg, 2000),
	})
	if a.InstanceID != nil {
		_ = s.store.AdjustInstanceRunning(ctx, *a.InstanceID, -1)
	}
	return nil
}

func (s *Executions) loadAndCheckLease(ctx context.Context, attemptID, leaseToken, instanceID string) (*model.Attempt, error) {
	a, err := s.store.GetAttempt(ctx, strings.TrimSpace(attemptID))
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, fmt.Errorf("attempt not found")
	}
	if err != nil {
		return nil, err
	}
	if a.LeaseToken == "" || a.LeaseToken != leaseToken {
		return nil, fmt.Errorf("invalid lease")
	}
	if a.InstanceID == nil || *a.InstanceID != instanceID {
		return nil, fmt.Errorf("instance mismatch")
	}
	switch a.Status {
	case model.AttemptSucceeded, model.AttemptFailed, model.AttemptCancelled, model.AttemptTimedOut:
		return nil, fmt.Errorf("attempt already finished")
	}
	return a, nil
}

func okAck(attemptID string) *protocol.TaskResultAck {
	return &protocol.TaskResultAck{Status: "OK", AttemptID: attemptID}
}

func rejectAck(attemptID string, err error) *protocol.TaskResultAck {
	return &protocol.TaskResultAck{
		Status: "REJECTED", AttemptID: attemptID, ErrorCode: "STALE_OR_INVALID", Message: err.Error(),
	}
}

func randomLeaseToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func parseMajor(ver string) (uint, bool) {
	ver = strings.TrimSpace(ver)
	if ver == "" {
		return 0, false
	}
	part := strings.SplitN(ver, ".", 2)[0]
	part = strings.TrimPrefix(part, "v")
	n, err := strconv.ParseUint(part, 10, 32)
	if err != nil {
		return 0, false
	}
	return uint(n), true
}

func parseProtoTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Now().UTC()
	}
	if t, err := time.Parse(protocol.TimeFormat, raw); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC()
	}
	return time.Now().UTC()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func isDup(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate") || strings.Contains(msg, "1062")
}
