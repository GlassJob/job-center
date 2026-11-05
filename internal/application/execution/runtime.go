package execution

import (
	"context"
	"errors"
	"time"

	"github.com/GlassJob/job-center/internal/domain/schedule"
	"github.com/GlassJob/job-center/internal/protocol"
	repo "github.com/GlassJob/job-center/internal/repository/mysql"
)

// RuntimeOptions 调度器周边：租约扫描、超时、派发、重启宽限。
type RuntimeOptions struct {
	NodeID           string
	SweepInterval    time.Duration
	LeaseTTL         time.Duration
	LostGrace        time.Duration
	TimeoutGrace     time.Duration
	DispatchInterval time.Duration
	// StartedAt Job Center 进程启动时间；首个 LeaseTTL 内暂停 LOST 判定。
	StartedAt time.Time
}

// Runtime 后台循环：lease-sweeper + timeout + dispatcher。
type Runtime struct {
	svc  *Service
	opt  RuntimeOptions
}

func NewRuntime(svc *Service, opt RuntimeOptions) *Runtime {
	if opt.SweepInterval <= 0 {
		opt.SweepInterval = 5 * time.Second
	}
	if opt.LeaseTTL <= 0 {
		opt.LeaseTTL = 60 * time.Second
	}
	if opt.LostGrace <= 0 {
		opt.LostGrace = 60 * time.Second
	}
	if opt.TimeoutGrace <= 0 {
		opt.TimeoutGrace = 60 * time.Second
	}
	if opt.DispatchInterval <= 0 {
		opt.DispatchInterval = time.Second
	}
	if opt.StartedAt.IsZero() {
		opt.StartedAt = time.Now().UTC()
	}
	return &Runtime{svc: svc, opt: opt}
}

func (r *Runtime) Run(ctx context.Context) {
	_ = r.svc.store.Q().EnsureNodeLeaseRow(ctx, "lease-sweeper", r.opt.NodeID)
	sweep := time.NewTicker(r.opt.SweepInterval)
	dispatch := time.NewTicker(r.opt.DispatchInterval)
	defer sweep.Stop()
	defer dispatch.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sweep.C:
			if err := r.Sweep(ctx); err != nil && ctx.Err() == nil {
				r.svc.log.Error("lease/timeout sweep failed", "err", err)
			}
		case <-dispatch.C:
			if err := r.DispatchOnce(ctx); err != nil && ctx.Err() == nil {
				r.svc.log.Error("dispatch failed", "err", err)
			}
		}
	}
}

func (r *Runtime) Sweep(ctx context.Context) error {
	held, err := r.svc.store.Q().HoldNodeLease(ctx, "lease-sweeper", r.opt.NodeID, 15*time.Second)
	if err != nil {
		return err
	}
	if !held {
		return nil
	}
	if err := r.expireLeases(ctx); err != nil {
		return err
	}
	if err := r.recoverLost(ctx); err != nil {
		return err
	}
	return r.timeoutAttempts(ctx)
}

func (r *Runtime) leaseLostPaused() bool {
	return time.Since(r.opt.StartedAt) < r.opt.LeaseTTL
}

func (r *Runtime) expireLeases(ctx context.Context) error {
	if r.leaseLostPaused() {
		return nil
	}
	list, err := r.svc.store.Q().ListExpiredLeases(ctx, 100)
	if err != nil {
		return err
	}
	for _, a := range list {
		ok, err := r.svc.store.Q().MarkAttemptLost(ctx, a.AttemptID, "LEASE_EXPIRED")
		if err != nil || !ok {
			continue
		}
		_ = r.svc.store.Q().InsertEvent(ctx, a.ExecutionID, a.AttemptID, "ATTEMPT_STATE", a.Status, "LOST", "SYSTEM", "lease-sweeper", "", map[string]any{"reason": "LEASE_EXPIRED"})
		if a.InstanceID != nil && a.LeaseToken != nil {
			_ = r.svc.sender.Send(*a.InstanceID, protocol.TypeTaskCancel, protocol.TaskCancel{
				ExecutionID: a.ExecutionID, AttemptID: a.AttemptID, LeaseToken: *a.LeaseToken,
				Reason: "LEASE_REVOKED", GraceSeconds: 5,
			})
		}
		// cancel_state≠NONE → 不重试，直接 FAILED
		if a.CancelState != "" && a.CancelState != "NONE" {
			_ = r.svc.store.Q().FinalizeAttempt(ctx, a.AttemptID, "LOST", "CANCEL_NOT_HONORED", time.Now().UTC(), nil, "CANCEL_NOT_HONORED", "cancel not confirmed", "CANCEL_UNCONFIRMED")
			_ = r.svc.store.Q().FinalizeExecution(ctx, a.ExecutionID, "FAILED", "CANCEL_NOT_HONORED", time.Now().UTC(), nil, "CANCEL_NOT_HONORED", "cancel not confirmed")
			r.afterTerminal(ctx, a.ExecutionID, a.TaskID)
		}
	}
	return nil
}

func (r *Runtime) recoverLost(ctx context.Context) error {
	list, err := r.svc.store.Q().ListLostReadyForRetry(ctx, r.opt.LostGrace, 100)
	if err != nil {
		return err
	}
	for _, a := range list {
		e, err := r.svc.store.Q().GetExecution(ctx, a.ExecutionID)
		if err != nil {
			continue
		}
		if a.CancelState != "" && a.CancelState != "NONE" {
			_ = r.svc.store.Q().FinalizeExecution(ctx, a.ExecutionID, "FAILED", "CANCEL_NOT_HONORED", time.Now().UTC(), nil, "CANCEL_NOT_HONORED", "cancel not confirmed")
			r.afterTerminal(ctx, a.ExecutionID, a.TaskID)
			continue
		}
		rp := schedule.ParseRetryPolicy(e.RetryPolicy)
		if rp.ShouldRetryLost(e.AttemptCount) {
			delay := rp.Delay(e.AttemptCount)
			_ = r.svc.store.Q().ScheduleRetry(ctx, e.ExecutionID, time.Now().UTC().Add(delay), "RETRY_WAIT")
			_ = r.svc.store.Q().InsertEvent(ctx, e.ExecutionID, a.AttemptID, "EXECUTION_STATE", e.Status, "PENDING", "SYSTEM", "lease-sweeper", "", map[string]any{"reason": "RETRY_ON_LOST", "delay_ms": delay.Milliseconds()})
		} else {
			_ = r.svc.store.Q().FinalizeExecution(ctx, e.ExecutionID, "FAILED", "LOST", time.Now().UTC(), nil, "LOST", "lease lost, retries exhausted")
			r.afterTerminal(ctx, e.ExecutionID, e.TaskID)
		}
	}
	return nil
}

func (r *Runtime) timeoutAttempts(ctx context.Context) error {
	list, err := r.svc.store.Q().ListPastDeadline(ctx, 100)
	if err != nil {
		return err
	}
	for _, a := range list {
		// 发送 cancel(TIMEOUT)；若已在取消路径或宽限期外 → TIMED_OUT
		if a.CancelState == "NONE" || a.CancelState == "" {
			_ = r.svc.store.Q().SetCancelState(ctx, a.AttemptID, "REQUESTED")
			if a.InstanceID != nil && a.LeaseToken != nil {
				if err := r.svc.sender.Send(*a.InstanceID, protocol.TypeTaskCancel, protocol.TaskCancel{
					ExecutionID: a.ExecutionID, AttemptID: a.AttemptID, LeaseToken: *a.LeaseToken,
					Reason: "TIMEOUT", GraceSeconds: int(r.opt.TimeoutGrace.Seconds()),
				}); err == nil {
					_ = r.svc.store.Q().SetCancelState(ctx, a.AttemptID, "SENT")
				}
			}
			_ = r.svc.store.Q().InsertEvent(ctx, a.ExecutionID, a.AttemptID, "TIMEOUT_REQUESTED", a.Status, a.Status, "SYSTEM", "timeout-sweeper", "", nil)
			continue
		}
		// 已请求取消且超过宽限：TIMED_OUT_UNCONFIRMED
		deadline := a.DeadlineAt
		if deadline == nil {
			continue
		}
		if time.Now().UTC().Sub(deadline.UTC()) < r.opt.TimeoutGrace {
			continue
		}
		e, err := r.svc.store.Q().GetExecution(ctx, a.ExecutionID)
		if err != nil {
			continue
		}
		_ = r.svc.store.Q().FinalizeAttempt(ctx, a.AttemptID, "TIMED_OUT", "TIMEOUT_UNCONFIRMED", time.Now().UTC(), nil, "TIMEOUT", "deadline exceeded", "CANCEL_UNCONFIRMED")
		rp := schedule.ParseRetryPolicy(e.RetryPolicy)
		if a.CancelState != "NONE" && a.CancelState != "STOPPED" && a.CancelState != "REQUESTED" && a.CancelState != "SENT" && a.CancelState != "ACKED" {
			// keep
		}
		if rp.ShouldRetryTimeout(e.AttemptCount) && (a.CancelState == "NONE" || a.CancelState == "" || a.CancelState == "REQUESTED" || a.CancelState == "SENT" || a.CancelState == "ACKED") {
			// docs: cancel_state≠NONE 一律不重试 — timeout cancel 也算 ≠NONE，故不重试
			_ = r.svc.store.Q().FinalizeExecution(ctx, e.ExecutionID, "TIMED_OUT", "TIMEOUT_UNCONFIRMED", time.Now().UTC(), nil, "TIMEOUT", "deadline exceeded")
			r.afterTerminal(ctx, e.ExecutionID, e.TaskID)
			continue
		}
		_ = r.svc.store.Q().FinalizeExecution(ctx, e.ExecutionID, "TIMED_OUT", "TIMEOUT_UNCONFIRMED", time.Now().UTC(), nil, "TIMEOUT", "deadline exceeded")
		r.afterTerminal(ctx, e.ExecutionID, e.TaskID)
	}
	return nil
}

func (r *Runtime) afterTerminal(ctx context.Context, executionID string, taskID int64) {
	slot, _ := r.svc.store.Q().GetExecutionSlot(ctx, executionID)
	slotKey := ""
	if slot != nil {
		slotKey = *slot
	}
	_ = r.svc.store.InTx(ctx, func(q *repo.Queries) error {
		return q.ReleaseSlotAndWake(ctx, executionID, taskID, slotKey)
	})
}

// DispatchOnce 领取 PENDING 并派发。
func (r *Runtime) DispatchOnce(ctx context.Context) error {
	list, err := r.svc.store.Q().ListPendingDispatch(ctx, 20)
	if err != nil {
		return err
	}
	for _, e := range list {
		if e.CurrentAttemptID != nil && e.StatusReason != nil && *e.StatusReason != "RETRY_WAIT" {
			continue
		}
		detail, err := r.svc.store.Q().GetTask(ctx, e.TaskID)
		if err != nil {
			continue
		}
		major := 0
		if e.HandlerMajor != nil {
			major = *e.HandlerMajor
		}
		cand, err := r.svc.store.Q().PickCandidate(ctx, e.TaskID, major)
		if err != nil {
			if errors.Is(err, repo.ErrNotFound) {
				// bump next_attempt_at
				_ = r.svc.store.Q().ScheduleRetry(ctx, e.ExecutionID, time.Now().UTC().Add(5*time.Second), "NO_AVAILABLE_INSTANCE")
			}
			continue
		}
		if _, err := r.svc.dispatch(ctx, e, detail, cand); err != nil {
			r.svc.log.Warn("dispatch error", "execution_id", e.ExecutionID, "err", err)
		}
	}
	return nil
}

// RenewAttemptLease 供 WS 路径续租。
func (s *Service) RenewAttemptLease(ctx context.Context, attemptID, token string) {
	_ = s // keep method on Service
	extend := time.Now().UTC().Add(s.leaseTTL)
	_, _ = s.store.Q().RenewLease(ctx, attemptID, token, extend)
}
