package execution

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/GlassJob/job-center/internal/application/apperr"
	"github.com/GlassJob/job-center/internal/domain/schedule"
	"github.com/GlassJob/job-center/internal/domain/task"
	"github.com/GlassJob/job-center/internal/infrastructure/idgen"
	"github.com/GlassJob/job-center/internal/infrastructure/security"
	"github.com/GlassJob/job-center/internal/protocol"
	repo "github.com/GlassJob/job-center/internal/repository/mysql"
)

type Sender interface {
	Send(instanceID, typ string, data any) error
}

type Service struct {
	store           *repo.Store
	sender          Sender
	leaseTTL        time.Duration
	log             *slog.Logger
	clientSecrets   func(string) string
	webhookHTTP     *http.Client
	webhookBackoffs []time.Duration
}

func New(store *repo.Store, sender Sender, leaseTTL time.Duration, log *slog.Logger) *Service {
	if leaseTTL <= 0 {
		leaseTTL = 60 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &Service{store: store, sender: sender, leaseTTL: leaseTTL, log: log}
}

type TriggerInput struct {
	TaskID         int64
	Params         json.RawMessage
	Reason         string
	AllowWait      bool
	CallbackURL    string
	IdempotencyKey string
	Operator       security.Operator
	Client         string
}

type SSETicketBrief struct {
	Ticket    string `json:"ticket"`
	ExpiresAt string `json:"expires_at"`
	URL       string `json:"url"`
}

type TriggerResult struct {
	ExecutionID      string          `json:"execution_id"`
	Status           string          `json:"status"`
	AttemptID        string          `json:"attempt_id,omitempty"`
	InstanceID       string          `json:"instance_id,omitempty"`
	IdempotentReplay bool            `json:"idempotent_replay,omitempty"`
	SSETicket        *SSETicketBrief `json:"sse_ticket,omitempty"`
}

func (s *Service) ManualTrigger(ctx context.Context, in TriggerInput) (*TriggerResult, int, error) {
	if in.IdempotencyKey == "" {
		return nil, 400, apperr.New(apperr.KindInvalid, "PARAMS_INVALID", "Idempotency-Key required")
	}
	if existing, err := s.store.Q().GetExecutionByIdempotency(ctx, in.TaskID, in.IdempotencyKey); err == nil {
		out := &TriggerResult{ExecutionID: existing.ExecutionID, Status: existing.Status, IdempotentReplay: true}
		if existing.CurrentAttemptID != nil {
			out.AttemptID = *existing.CurrentAttemptID
		}
		return out, 200, nil
	} else if !errors.Is(err, repo.ErrNotFound) {
		return nil, 503, err
	}
	detail, err := s.store.Q().GetTask(ctx, in.TaskID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, 404, apperr.New(apperr.KindNotFound, "NOT_FOUND", "task not found")
		}
		return nil, 503, err
	}
	if detail.Schedule == nil || !detail.Schedule.ManualEnabled {
		return nil, 400, apperr.New(apperr.KindInvalid, "MANUAL_DISABLED", "manual_enabled=0")
	}
	if len(in.Params) == 0 {
		in.Params = json.RawMessage(`{}`)
	}
	hv := detail.HandlerVersion
	major := 0
	if v, err := parseMajor(hv); err == nil {
		major = v
	}
	retry := detail.Schedule.RetryPolicy
	if len(retry) == 0 {
		retry = json.RawMessage(`{"max_attempts":1}`)
	}
	maxAttempts := 1
	var rp struct {
		MaxAttempts int `json:"max_attempts"`
	}
	_ = json.Unmarshal(retry, &rp)
	if rp.MaxAttempts > 0 {
		maxAttempts = rp.MaxAttempts
	}
	execID := idgen.NewULID()
	e := &repo.Execution{
		ExecutionID: execID, TaskID: detail.ID, ServiceID: detail.ServiceID, TaskCode: detail.TaskCode,
		TriggerType: "MANUAL", TriggeredBy: "admin:" + in.Operator.ID, Params: in.Params,
		IdempotencyKey: &in.IdempotencyKey, Status: "PENDING", HandlerVersion: &hv, HandlerMajor: &major,
		SchemaHash: detail.SchemaHash, TimeoutSeconds: detail.Schedule.TimeoutSeconds, RetryPolicy: retry,
		MaxAttempts: maxAttempts, AttemptCount: 0,
	}
	if err := s.store.Q().InsertExecution(ctx, e); err != nil {
		if repo.IsDuplicate(err) {
			existing, e2 := s.store.Q().GetExecutionByIdempotency(ctx, in.TaskID, in.IdempotencyKey)
			if e2 == nil {
				return &TriggerResult{ExecutionID: existing.ExecutionID, Status: existing.Status, IdempotentReplay: true}, 200, nil
			}
		}
		return nil, 503, err
	}
	_ = s.store.Q().InsertEvent(ctx, execID, "", "EXECUTION_STATE", "", "PENDING", "ADMIN", in.Operator.ID, "", map[string]any{"reason": in.Reason})
	_ = s.store.Q().InsertAudit(ctx, repo.Audit{
		OperatorSource: in.Client, OperatorID: in.Operator.ID, OperatorName: in.Operator.Name, Action: "task.execute",
		Permission: "job.task.execute", ResourceType: "task", ResourceID: fmt.Sprintf("%d", in.TaskID),
		RequestID: in.Operator.RequestID, ClientIP: in.Operator.ClientIP, Result: "SUCCESS",
		After: map[string]any{"execution_id": execID},
	})

	cand, err := s.store.Q().PickCandidate(ctx, detail.ID, major)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			if in.AllowWait {
				return &TriggerResult{ExecutionID: execID, Status: "PENDING"}, 202, nil
			}
			_ = s.finalizeExecutionAndNotify(ctx, execID, "FAILED", "NO_AVAILABLE_INSTANCE", time.Now().UTC(), nil, "NO_AVAILABLE_INSTANCE", "no online instance")
			return nil, 400, apperr.New(apperr.KindInvalid, "NO_AVAILABLE_INSTANCE", "no online compatible instance")
		}
		return nil, 503, err
	}
	attempt, err := s.dispatch(ctx, e, detail, cand)
	if err != nil {
		return nil, 503, err
	}
	return &TriggerResult{ExecutionID: execID, Status: "PENDING", AttemptID: attempt.AttemptID, InstanceID: cand.InstanceID}, 202, nil
}

func (s *Service) dispatch(ctx context.Context, e *repo.Execution, detail *repo.TaskDetail, cand *repo.CandidateInstance) (*repo.Attempt, error) {
	attemptID := idgen.NewULID()
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	leaseExp := now.Add(s.leaseTTL)
	deadline := now.Add(time.Duration(e.TimeoutSeconds) * time.Second)
	inst := cand.InstanceID
	a := &repo.Attempt{
		AttemptID: attemptID, ExecutionID: e.ExecutionID, AttemptNumber: e.AttemptCount + 1,
		TaskID: e.TaskID, ServiceID: e.ServiceID, InstanceID: &inst, Hostname: strPtr(cand.Hostname),
		GitCommit: strPtr(cand.GitCommit), AppVersion: strPtr(cand.AppVersion), HandlerVersion: strPtr(cand.HandlerVersion),
		LeaseToken: &token, LeaseExpiresAt: &leaseExp, Status: "DISPATCHING", DispatchCount: 1, DispatchedAt: &now, DeadlineAt: &deadline,
		CancelState: "NONE",
	}
	if err := s.store.Q().InsertAttempt(ctx, a); err != nil {
		return nil, err
	}
	if err := s.store.Q().SetExecutionCurrentAttempt(ctx, e.ExecutionID, attemptID, "PENDING", a.AttemptNumber); err != nil {
		return nil, err
	}
	_ = s.store.Q().InsertEvent(ctx, e.ExecutionID, attemptID, "DISPATCHED", "CREATED", "DISPATCHING", "SYSTEM", "", cand.InstanceID, nil)

	params := e.Params
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	ts, err := s.store.Q().GetTaskState(ctx, e.TaskID)
	if err != nil {
		return nil, err
	}
	statePayload := ts.State
	if len(statePayload) == 0 {
		statePayload = json.RawMessage(`{}`)
	}
	msg := protocol.TaskExecute{
		ExecutionID: e.ExecutionID, AttemptID: attemptID, AttemptNumber: a.AttemptNumber,
		LeaseToken: token, LeaseExpiresAt: protocol.FormatTime(leaseExp), TaskCode: e.TaskCode, TaskName: detail.TaskName,
		Params: params, State: statePayload, StateVersion: ts.Version,
		TimeoutSeconds: e.TimeoutSeconds, DeadlineAt: protocol.FormatTime(deadline),
		TriggerType: e.TriggerType, HandlerVersion: cand.HandlerVersion,
	}
	if err := s.sender.Send(cand.InstanceID, protocol.TypeTaskExecute, msg); err != nil {
		s.log.Warn("dispatch send failed", "instance_id", cand.InstanceID, "err", err)
		// leave DISPATCHING; executor reconnect / retry path in later work
	}
	return a, nil
}

func (s *Service) OnExecuteAck(ctx context.Context, sessInstance string, ack protocol.TaskExecuteAck) (*protocol.TaskResultAck, error) {
	a, err := s.loadAndCheckLease(ctx, ack.AttemptID, ack.LeaseToken, sessInstance)
	if err != nil {
		return reject(ack.AttemptID, err), nil
	}
	if !ack.Accepted {
		_ = s.store.Q().FinalizeAttempt(ctx, a.AttemptID, "FAILED", "REJECTED_BY_EXECUTOR", time.Now().UTC(), nil, "REJECTED", "executor rejected", "")
		_ = s.finalizeExecutionAndNotify(ctx, a.ExecutionID, "FAILED", "REJECTED_BY_EXECUTOR", time.Now().UTC(), nil, "REJECTED", "executor rejected")
		return okAck(ack.AttemptID), nil
	}
	_ = s.store.Q().MarkAttemptAcked(ctx, a.AttemptID)
	_ = s.store.Q().InsertEvent(ctx, a.ExecutionID, a.AttemptID, "ACKED", a.Status, "ACKNOWLEDGED", "EXECUTOR", sessInstance, sessInstance, nil)
	return okAck(ack.AttemptID), nil
}

func (s *Service) OnStarted(ctx context.Context, sessInstance string, msg protocol.TaskStarted) (*protocol.TaskResultAck, error) {
	a, err := s.loadAndCheckLease(ctx, msg.AttemptID, msg.LeaseToken, sessInstance)
	if err != nil {
		return reject(msg.AttemptID, err), nil
	}
	started, err := parseTime(msg.StartedAt)
	if err != nil {
		started = time.Now().UTC()
	}
	_ = s.store.Q().MarkAttemptStarted(ctx, a.AttemptID, started)
	_ = s.store.Q().MarkExecutionStarted(ctx, a.ExecutionID, started)
	_ = s.store.Q().InsertEvent(ctx, a.ExecutionID, a.AttemptID, "ATTEMPT_STATE", a.Status, "RUNNING", "EXECUTOR", sessInstance, sessInstance, nil)
	return okAck(msg.AttemptID), nil
}

func (s *Service) OnProgress(ctx context.Context, sessInstance string, msg protocol.TaskProgress) (*protocol.TaskResultAck, error) {
	if _, err := s.loadAndCheckLease(ctx, msg.AttemptID, msg.LeaseToken, sessInstance); err != nil {
		return reject(msg.AttemptID, err), nil
	}
	_ = s.store.Q().UpdateProgress(ctx, msg.AttemptID, msg.Percent, msg.Message)
	return okAck(msg.AttemptID), nil
}

func (s *Service) OnLog(ctx context.Context, sessInstance string, msg protocol.TaskLog) (*protocol.TaskResultAck, error) {
	a, err := s.loadAndCheckLease(ctx, msg.AttemptID, msg.LeaseToken, sessInstance)
	if err != nil {
		return reject(msg.AttemptID, err), nil
	}
	entries := make([]struct {
		Seq int64; Level, Stream, Message string; Fields json.RawMessage; LoggedAt time.Time
	}, 0, len(msg.Entries))
	var maxSeq int64
	for _, e := range msg.Entries {
		ts, err := parseTime(e.Ts)
		if err != nil {
			ts = time.Now().UTC()
		}
		entries = append(entries, struct {
			Seq int64; Level, Stream, Message string; Fields json.RawMessage; LoggedAt time.Time
		}{e.Seq, e.Level, e.Stream, e.Message, e.Fields, ts})
		if e.Seq > maxSeq {
			maxSeq = e.Seq
		}
	}
	n, err := s.store.Q().InsertLogs(ctx, a.ExecutionID, a.AttemptID, entries)
	if err != nil {
		ra := &protocol.TaskResultAck{Status: "RETRY_LATER", AttemptID: msg.AttemptID, Code: protocol.ErrStorageUnavailable, ErrorCode: protocol.ErrStorageUnavailable, Message: err.Error()}
		return ra, nil
	}
	ack := okAck(msg.AttemptID)
	if n > 0 {
		ack.AckedThroughSeq = &n
	} else if maxSeq > 0 {
		ack.AckedThroughSeq = &maxSeq
	}
	return ack, nil
}

// OnCheckpoint 处理中途游标持久化（task.checkpoint）：
// 事务内锁 job_execution_attempt，校验 lease_token、实例归属、未过期、活跃态（ACKNOWLEDGED|RUNNING）、
// 仍为 execution.current_attempt_id 且 cancel_state == 'NONE'，通过后推进 job_task_state(version+1) 并写 CHECKPOINT 事件；
// 否则写 STALE_RESULT_REJECTED 并回 REJECTED(LEASE_INVALID)。
func (s *Service) OnCheckpoint(ctx context.Context, sessInstance string, msg protocol.TaskCheckpoint) (*protocol.TaskResultAck, error) {
	cleanState, err := task.ValidateJSONObject(msg.State, task.MaxStateBytes)
	if err != nil {
		return reject(msg.AttemptID, apperr.New(apperr.KindInvalid, protocol.ErrProtocolInvalidMessage, "state: "+err.Error())), nil
	}
	var ack *protocol.TaskResultAck
	txErr := s.store.InTx(ctx, func(q *repo.Queries) error {
		a, leaseExpired, err := q.GetAttemptForUpdate(ctx, msg.AttemptID)
		if err != nil {
			if errors.Is(err, repo.ErrNotFound) {
				ack = reject(msg.AttemptID, apperr.New(apperr.KindNotFound, protocol.ErrAttemptNotFound, "attempt not found"))
				return nil
			}
			return err
		}
		e, err := q.GetExecutionForUpdate(ctx, a.ExecutionID)
		if err != nil {
			return err
		}
		var rejectReason string
		switch {
		case a.InstanceID == nil || *a.InstanceID != sessInstance:
			rejectReason = "instance mismatch"
		case a.LeaseToken == nil || *a.LeaseToken != msg.LeaseToken:
			rejectReason = "lease_token mismatch"
		case leaseExpired:
			rejectReason = "lease expired"
		case a.Status != "ACKNOWLEDGED" && a.Status != "RUNNING":
			rejectReason = "attempt not active: " + a.Status
		case e.CurrentAttemptID == nil || *e.CurrentAttemptID != msg.AttemptID:
			rejectReason = "stale attempt"
		case a.CancelState != "" && a.CancelState != "NONE":
			rejectReason = "cancel_state=" + a.CancelState
		}
		if rejectReason != "" {
			_ = q.InsertEvent(ctx, a.ExecutionID, a.AttemptID, "STALE_RESULT_REJECTED", a.Status, a.Status,
				"EXECUTOR", sessInstance, sessInstance, map[string]any{"reason": rejectReason, "source": "task.checkpoint"})
			ack = reject(msg.AttemptID, apperr.New(apperr.KindInvalid, protocol.ErrLeaseInvalid, rejectReason))
			return nil
		}
		newVer, err := q.UpsertTaskStateByAttempt(ctx, e.TaskID, cleanState, e.ExecutionID, a.AttemptID)
		if err != nil {
			return err
		}
		if err := q.InsertEvent(ctx, e.ExecutionID, a.AttemptID, "CHECKPOINT", a.Status, a.Status,
			"EXECUTOR", sessInstance, sessInstance, map[string]any{"state_version": newVer}); err != nil {
			return err
		}
		_, _ = q.RenewLease(ctx, a.AttemptID, msg.LeaseToken, time.Now().UTC().Add(s.leaseTTL))
		ack = okAck(msg.AttemptID)
		return nil
	})
	if txErr != nil {
		return &protocol.TaskResultAck{
			Status: "RETRY_LATER", AttemptID: msg.AttemptID,
			Code: protocol.ErrStorageUnavailable, ErrorCode: protocol.ErrStorageUnavailable, Message: txErr.Error(),
		}, nil
	}
	return ack, nil
}

func (s *Service) OnCompleted(ctx context.Context, sessInstance string, msg protocol.TaskCompleted) (*protocol.TaskResultAck, error) {
	trimmedNext := bytes.TrimSpace(msg.NextState)
	hasNextState := len(trimmedNext) > 0 && !bytes.Equal(trimmedNext, []byte("null"))
	var cleanNextState json.RawMessage
	if hasNextState {
		var err error
		cleanNextState, err = task.ValidateJSONObject(trimmedNext, task.MaxStateBytes)
		if err != nil {
			return reject(msg.AttemptID, apperr.New(apperr.KindInvalid, protocol.ErrProtocolInvalidMessage, "next_state: "+err.Error())), nil
		}
	}
	finished, _ := parseTime(msg.FinishedAt)
	if finished.IsZero() {
		finished = time.Now().UTC()
	}

	var (
		ack           *protocol.TaskResultAck
		execID        string
		taskID        int64
		execFinalized bool
		replayed      bool
	)
	txErr := s.store.InTx(ctx, func(q *repo.Queries) error {
		a, leaseExpired, err := q.GetAttemptForUpdate(ctx, msg.AttemptID)
		if err != nil {
			if errors.Is(err, repo.ErrNotFound) {
				ack = reject(msg.AttemptID, apperr.New(apperr.KindNotFound, protocol.ErrAttemptNotFound, "attempt not found"))
				return nil
			}
			return err
		}
		if a.LeaseToken == nil || *a.LeaseToken != msg.LeaseToken {
			_ = q.InsertEvent(ctx, a.ExecutionID, a.AttemptID, "STALE_RESULT_REJECTED", a.Status, a.Status,
				"EXECUTOR", sessInstance, sessInstance, map[string]any{"reason": "lease_token mismatch"})
			ack = reject(msg.AttemptID, apperr.New(apperr.KindInvalid, protocol.ErrLeaseInvalid, "lease_token mismatch"))
			return nil
		}
		if a.InstanceID == nil || *a.InstanceID != sessInstance {
			_ = q.InsertEvent(ctx, a.ExecutionID, a.AttemptID, "STALE_RESULT_REJECTED", a.Status, a.Status,
				"EXECUTOR", sessInstance, sessInstance, map[string]any{"reason": "instance mismatch"})
			ack = reject(msg.AttemptID, apperr.New(apperr.KindInvalid, protocol.ErrLeaseInvalid, "instance mismatch"))
			return nil
		}
		// 重复重放幂等：已终态 SUCCEEDED 的同 attempt_id 直接回 OK，不重复累加 job_task_state.version
		if a.Status == "SUCCEEDED" {
			replayed = true
			ack = okAck(msg.AttemptID)
			return nil
		}
		e, err := q.GetExecutionForUpdate(ctx, a.ExecutionID)
		if err != nil {
			return err
		}
		if e.CurrentAttemptID == nil || *e.CurrentAttemptID != msg.AttemptID {
			_ = q.InsertEvent(ctx, a.ExecutionID, a.AttemptID, "STALE_RESULT_REJECTED", a.Status, a.Status,
				"EXECUTOR", sessInstance, sessInstance, map[string]any{"reason": "stale attempt"})
			ack = reject(msg.AttemptID, apperr.New(apperr.KindInvalid, protocol.ErrLeaseInvalid, "stale attempt"))
			return nil
		}
		if leaseExpired || a.Status == "LOST" || a.Status == "FAILED" || a.Status == "CANCELLED" || a.Status == "TIMED_OUT" {
			_ = q.InsertEvent(ctx, a.ExecutionID, a.AttemptID, "STALE_RESULT_REJECTED", a.Status, a.Status,
				"EXECUTOR", sessInstance, sessInstance, map[string]any{"reason": "lease expired or terminal: " + a.Status})
			ack = reject(msg.AttemptID, apperr.New(apperr.KindInvalid, protocol.ErrLeaseInvalid, "lease expired or attempt no longer active"))
			return nil
		}

		status := "SUCCEEDED"
		if err := q.FinalizeAttempt(ctx, a.AttemptID, status, "", finished, msg.Result, "", "", ""); err != nil {
			return err
		}
		ok, err := q.FinalizeExecution(ctx, a.ExecutionID, status, "", finished, msg.Result, "", "")
		if err != nil {
			return err
		}
		execFinalized = ok
		execID = a.ExecutionID
		taskID = e.TaskID

		var evPayload any
		if hasNextState {
			newVer, err := q.UpsertTaskStateByAttempt(ctx, e.TaskID, cleanNextState, e.ExecutionID, a.AttemptID)
			if err != nil {
				return err
			}
			evPayload = map[string]any{"state_version": newVer}
		}
		if err := q.InsertEvent(ctx, a.ExecutionID, a.AttemptID, "ATTEMPT_STATE", a.Status, status, "EXECUTOR", sessInstance, sessInstance, evPayload); err != nil {
			return err
		}
		ack = okAck(msg.AttemptID)
		return nil
	})
	if txErr != nil {
		return &protocol.TaskResultAck{
			Status: "RETRY_LATER", AttemptID: msg.AttemptID,
			Code: protocol.ErrStorageUnavailable, ErrorCode: protocol.ErrStorageUnavailable, Message: txErr.Error(),
		}, nil
	}
	if ack != nil && ack.Status == "OK" && !replayed {
		if execFinalized {
			s.dispatchTerminalWebhookAsync(execID)
		}
		s.wakeAfterTerminal(ctx, execID, taskID)
	}
	return ack, nil
}

func (s *Service) OnFailed(ctx context.Context, sessInstance string, msg protocol.TaskFailed) (*protocol.TaskResultAck, error) {
	a, err := s.loadAndCheckLease(ctx, msg.AttemptID, msg.LeaseToken, sessInstance)
	if err != nil {
		return reject(msg.AttemptID, err), nil
	}
	finished, _ := parseTime(msg.FinishedAt)
	if finished.IsZero() {
		finished = time.Now().UTC()
	}
	status := "FAILED"
	if msg.Reason == "TIMEOUT" {
		status = "TIMED_OUT"
	}
	cancelState := ""
	if a.CancelState != "NONE" && a.CancelState != "STOPPED" {
		cancelState = "CANCEL_UNCONFIRMED"
	}
	_ = s.store.Q().FinalizeAttempt(ctx, a.AttemptID, status, msg.Reason, finished, nil, msg.Error.Code, msg.Error.Message, cancelState)
	_ = s.store.Q().InsertEvent(ctx, a.ExecutionID, a.AttemptID, "ATTEMPT_STATE", a.Status, status, "EXECUTOR", sessInstance, sessInstance, nil)

	e, _ := s.store.Q().GetExecution(ctx, a.ExecutionID)
	if e == nil {
		return okAck(msg.AttemptID), nil
	}
	// cancel_state≠NONE：不重试
	if cancelState == "CANCEL_UNCONFIRMED" {
		_ = s.finalizeExecutionAndNotify(ctx, a.ExecutionID, status, "CANCEL_NOT_HONORED", finished, nil, msg.Error.Code, msg.Error.Message)
		s.wakeAfterTerminal(ctx, a.ExecutionID, a.TaskID)
		return okAck(msg.AttemptID), nil
	}
	rp := schedule.ParseRetryPolicy(e.RetryPolicy)
	should := false
	if status == "TIMED_OUT" {
		should = rp.ShouldRetryTimeout(e.AttemptCount)
	} else {
		should = rp.ShouldRetryFailed(e.AttemptCount, msg.Reason, msg.Error.Code, nil)
	}
	if should {
		delay := rp.Delay(e.AttemptCount)
		_ = s.store.Q().ScheduleRetry(ctx, e.ExecutionID, time.Now().UTC().Add(delay), "RETRY_WAIT")
		_ = s.store.Q().InsertEvent(ctx, e.ExecutionID, a.AttemptID, "EXECUTION_STATE", e.Status, "PENDING", "SYSTEM", "", "", map[string]any{"reason": "RETRY", "delay_ms": delay.Milliseconds()})
		return okAck(msg.AttemptID), nil
	}
	_ = s.finalizeExecutionAndNotify(ctx, a.ExecutionID, status, msg.Reason, finished, nil, msg.Error.Code, msg.Error.Message)
	s.wakeAfterTerminal(ctx, a.ExecutionID, a.TaskID)
	return okAck(msg.AttemptID), nil
}

func (s *Service) OnCancelled(ctx context.Context, sessInstance string, msg protocol.TaskCancelled) (*protocol.TaskResultAck, error) {
	a, err := s.loadAndCheckLease(ctx, msg.AttemptID, msg.LeaseToken, sessInstance)
	if err != nil {
		return reject(msg.AttemptID, err), nil
	}
	finished, _ := parseTime(msg.FinishedAt)
	if finished.IsZero() {
		finished = time.Now().UTC()
	}
	_ = s.store.Q().FinalizeAttempt(ctx, a.AttemptID, "CANCELLED", msg.StoppedBy, finished, nil, "", "", "STOPPED")
	_ = s.finalizeExecutionAndNotify(ctx, a.ExecutionID, "CANCELLED", msg.StoppedBy, finished, nil, "", "")
	_ = s.store.Q().InsertEvent(ctx, a.ExecutionID, a.AttemptID, "ATTEMPT_STATE", a.Status, "CANCELLED", "EXECUTOR", sessInstance, sessInstance, nil)
	s.wakeAfterTerminal(ctx, a.ExecutionID, a.TaskID)
	return okAck(msg.AttemptID), nil
}

func (s *Service) OnCancelAck(ctx context.Context, sessInstance string, msg protocol.TaskCancelAck) error {
	a, err := s.store.Q().GetAttempt(ctx, msg.AttemptID)
	if err != nil {
		return err
	}
	if a.InstanceID == nil || *a.InstanceID != sessInstance {
		return apperr.New(apperr.KindInvalid, protocol.ErrLeaseInvalid, "instance mismatch")
	}
	switch msg.State {
	case "CANCELLING":
		_ = s.store.Q().SetCancelState(ctx, a.AttemptID, "ACKED")
	case "ALREADY_FINISHED":
		// wait for result
	}
	_ = s.store.Q().InsertEvent(ctx, a.ExecutionID, a.AttemptID, "CANCEL_ACKED", a.CancelState, msg.State, "EXECUTOR", sessInstance, sessInstance, nil)
	return nil
}

type CancelResult struct {
	ExecutionStatus string `json:"execution_status"`
	CancelState     string `json:"cancel_state"`
}

func (s *Service) Cancel(ctx context.Context, executionID, reason string, grace int, op security.Operator) (*CancelResult, int, error) {
	return s.CancelBy(ctx, executionID, reason, grace, "ADMIN", op.ID)
}

func (s *Service) CancelBy(ctx context.Context, executionID, reason string, grace int, actorSource, actorID string) (*CancelResult, int, error) {
	e, err := s.store.Q().GetExecution(ctx, executionID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, 404, apperr.New(apperr.KindNotFound, "NOT_FOUND", "execution not found")
		}
		return nil, 503, err
	}
	if e.Status == "SUCCEEDED" || e.Status == "FAILED" || e.Status == "CANCELLED" || e.Status == "TIMED_OUT" || e.Status == "SKIPPED" {
		return nil, 409, apperr.New(apperr.KindConflict, "ALREADY_FINAL", "execution already final")
	}
	if e.CurrentAttemptID == nil {
		_ = s.finalizeExecutionAndNotify(ctx, executionID, "CANCELLED", reason, time.Now().UTC(), nil, "", "")
		return &CancelResult{ExecutionStatus: "CANCELLED", CancelState: "STOPPED"}, 202, nil
	}
	a, err := s.store.Q().GetAttempt(ctx, *e.CurrentAttemptID)
	if err != nil {
		return nil, 503, err
	}
	prefix := "admin:"
	if actorSource == "API" {
		prefix = "api:"
	}
	_ = s.store.Q().MarkExecutionCancelRequested(ctx, executionID, prefix+actorID)
	_ = s.store.Q().SetCancelState(ctx, a.AttemptID, "REQUESTED")
	state := "REQUESTED"
	if a.InstanceID != nil && a.LeaseToken != nil {
		if grace <= 0 {
			grace = 30
		}
		msg := protocol.TaskCancel{
			ExecutionID: executionID, AttemptID: a.AttemptID, LeaseToken: *a.LeaseToken,
			Reason: reason, GraceSeconds: grace,
		}
		if err := s.sender.Send(*a.InstanceID, protocol.TypeTaskCancel, msg); err == nil {
			_ = s.store.Q().SetCancelState(ctx, a.AttemptID, "SENT")
			state = "SENT"
		}
	}
	_ = s.store.Q().InsertEvent(ctx, executionID, a.AttemptID, "CANCEL_REQUESTED", "", state, actorSource, actorID, "", map[string]any{"reason": reason})
	return &CancelResult{ExecutionStatus: e.Status, CancelState: state}, 202, nil
}

func (s *Service) GetDetail(ctx context.Context, executionID string) (*repo.Execution, []*repo.Attempt, error) {
	e, err := s.store.Q().GetExecution(ctx, executionID)
	if err != nil {
		return nil, nil, err
	}
	as, err := s.store.Q().ListAttempts(ctx, executionID)
	return e, as, err
}

func (s *Service) loadAndCheckLease(ctx context.Context, attemptID, token, sessInstance string) (*repo.Attempt, error) {
	a, err := s.store.Q().GetAttempt(ctx, attemptID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, apperr.New(apperr.KindNotFound, protocol.ErrAttemptNotFound, "attempt not found")
		}
		return nil, err
	}
	if a.LeaseToken == nil || *a.LeaseToken != token {
		_ = s.store.Q().InsertEvent(ctx, a.ExecutionID, attemptID, "STALE_RESULT_REJECTED", a.Status, a.Status, "EXECUTOR", sessInstance, sessInstance, map[string]any{"reason": "lease_token mismatch"})
		return nil, apperr.New(apperr.KindInvalid, protocol.ErrLeaseInvalid, "lease_token mismatch")
	}
	if a.InstanceID == nil || *a.InstanceID != sessInstance {
		return nil, apperr.New(apperr.KindInvalid, protocol.ErrLeaseInvalid, "instance mismatch")
	}
	e, err := s.store.Q().GetExecution(ctx, a.ExecutionID)
	if err != nil {
		return nil, err
	}
	if e.CurrentAttemptID == nil || *e.CurrentAttemptID != attemptID {
		_ = s.store.Q().InsertEvent(ctx, a.ExecutionID, attemptID, "STALE_RESULT_REJECTED", a.Status, a.Status, "EXECUTOR", sessInstance, sessInstance, map[string]any{"reason": "stale attempt"})
		return nil, apperr.New(apperr.KindInvalid, protocol.ErrLeaseInvalid, "stale attempt")
	}
	// 续租（心跳/进度/结果类消息）
	_, _ = s.store.Q().RenewLease(ctx, attemptID, token, time.Now().UTC().Add(s.leaseTTL))
	return a, nil
}

func (s *Service) wakeAfterTerminal(ctx context.Context, executionID string, taskID int64) {
	slot, _ := s.store.Q().GetExecutionSlot(ctx, executionID)
	slotKey := ""
	if slot != nil {
		slotKey = *slot
	}
	_ = s.store.InTx(ctx, func(q *repo.Queries) error {
		return q.ReleaseSlotAndWake(ctx, executionID, taskID, slotKey)
	})
}

func okAck(attemptID string) *protocol.TaskResultAck {
	return &protocol.TaskResultAck{Status: "OK", AttemptID: attemptID}
}

func reject(attemptID string, err error) *protocol.TaskResultAck {
	ae := apperr.As(err)
	code := protocol.ErrLeaseInvalid
	if ae != nil && ae.Code != "" {
		code = ae.Code
	}
	msg := err.Error()
	if ae != nil {
		msg = ae.Message
	}
	return &protocol.TaskResultAck{Status: "REJECTED", AttemptID: attemptID, Code: code, ErrorCode: code, Message: msg}
}

func randomToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}
	return time.Parse(time.RFC3339, s)
}

func parseMajor(v string) (int, error) {
	var major int
	_, err := fmt.Sscanf(v, "%d", &major)
	return major, err
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *Service) APITrigger(ctx context.Context, in TriggerInput) (*TriggerResult, int, error) {
	if in.IdempotencyKey == "" {
		return nil, 400, apperr.New(apperr.KindInvalid, "PARAMS_INVALID", "Idempotency-Key required")
	}
	if existing, err := s.store.Q().GetExecutionByIdempotency(ctx, in.TaskID, in.IdempotencyKey); err == nil {
		out := &TriggerResult{ExecutionID: existing.ExecutionID, Status: existing.Status, IdempotentReplay: true}
		if existing.CurrentAttemptID != nil {
			out.AttemptID = *existing.CurrentAttemptID
		}
		return out, 200, nil
	} else if !errors.Is(err, repo.ErrNotFound) {
		return nil, 503, err
	}
	detail, err := s.store.Q().GetTask(ctx, in.TaskID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, 404, apperr.New(apperr.KindNotFound, "NOT_FOUND", "task not found")
		}
		return nil, 503, err
	}
	if detail.Status != "ACTIVE" {
		return nil, 400, apperr.New(apperr.KindInvalid, "TASK_DISABLED", "task is not active")
	}
	if len(in.Params) == 0 {
		in.Params = json.RawMessage(`{}`)
	}
	hv := detail.HandlerVersion
	major := 0
	if v, err := parseMajor(hv); err == nil {
		major = v
	}
	retry := json.RawMessage(`{"max_attempts":1}`)
	timeoutSec := 3600
	if detail.Schedule != nil {
		if len(detail.Schedule.RetryPolicy) > 0 {
			retry = detail.Schedule.RetryPolicy
		}
		if detail.Schedule.TimeoutSeconds > 0 {
			timeoutSec = detail.Schedule.TimeoutSeconds
		}
	}
	maxAttempts := 1
	var rp struct {
		MaxAttempts int `json:"max_attempts"`
	}
	_ = json.Unmarshal(retry, &rp)
	if rp.MaxAttempts > 0 {
		maxAttempts = rp.MaxAttempts
	}
	var trigCtx json.RawMessage
	if in.Client != "" || strings.TrimSpace(in.CallbackURL) != "" || in.Reason != "" {
		b, _ := json.Marshal(triggerContextMeta{
			ClientID:    in.Client,
			CallbackURL: strings.TrimSpace(in.CallbackURL),
			Reason:      in.Reason,
		})
		trigCtx = b
	}
	execID := idgen.NewULID()
	e := &repo.Execution{
		ExecutionID: execID, TaskID: detail.ID, ServiceID: detail.ServiceID, TaskCode: detail.TaskCode,
		TriggerType: "API", TriggeredBy: "api:" + in.Client, TriggerContext: trigCtx, Params: in.Params,
		IdempotencyKey: &in.IdempotencyKey, Status: "PENDING", HandlerVersion: &hv, HandlerMajor: &major,
		SchemaHash: detail.SchemaHash, TimeoutSeconds: timeoutSec, RetryPolicy: retry,
		MaxAttempts: maxAttempts, AttemptCount: 0,
	}
	if err := s.store.Q().InsertExecution(ctx, e); err != nil {
		if repo.IsDuplicate(err) {
			existing, e2 := s.store.Q().GetExecutionByIdempotency(ctx, in.TaskID, in.IdempotencyKey)
			if e2 == nil {
				return &TriggerResult{ExecutionID: existing.ExecutionID, Status: existing.Status, IdempotentReplay: true}, 200, nil
			}
		}
		return nil, 503, err
	}
	_ = s.store.Q().InsertEvent(ctx, execID, "", "EXECUTION_STATE", "", "PENDING", "API", in.Client, "", nil)
	_ = s.store.Q().InsertAudit(ctx, repo.Audit{
		OperatorSource: "API", OperatorID: in.Client, Action: "task.trigger_api",
		Permission: "api.trigger", ResourceType: "task", ResourceID: fmt.Sprintf("%d", in.TaskID),
		Result: "SUCCESS", After: map[string]any{"execution_id": execID},
	})
	cand, err := s.store.Q().PickCandidate(ctx, detail.ID, major)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			if in.AllowWait {
				return &TriggerResult{ExecutionID: execID, Status: "PENDING"}, 202, nil
			}
			_ = s.finalizeExecutionAndNotify(ctx, execID, "FAILED", "NO_AVAILABLE_INSTANCE", time.Now().UTC(), nil, "NO_AVAILABLE_INSTANCE", "no online instance")
			return nil, 400, apperr.New(apperr.KindInvalid, "NO_AVAILABLE_INSTANCE", "no online compatible instance")
		}
		return nil, 503, err
	}
	attempt, err := s.dispatch(ctx, e, detail, cand)
	if err != nil {
		return nil, 503, err
	}
	return &TriggerResult{ExecutionID: execID, Status: "PENDING", AttemptID: attempt.AttemptID, InstanceID: cand.InstanceID}, 202, nil
}
