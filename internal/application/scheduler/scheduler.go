// Package scheduler 实现 Cron 调度循环：leader 租约、misfire、并发槽、uk_schedule_tick 兜底。
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/GlassJob/job-center/internal/domain/schedule"
	"github.com/GlassJob/job-center/internal/domain/task"
	"github.com/GlassJob/job-center/internal/infrastructure/idgen"
	repo "github.com/GlassJob/job-center/internal/repository/mysql"
)

type Options struct {
	NodeID          string
	TickInterval    time.Duration
	LeaseTTL        time.Duration
	DefaultLocation *time.Location
	BatchSize       int
}

type Scheduler struct {
	store *repo.Store
	opt   Options
	log   *slog.Logger
}

func New(store *repo.Store, opt Options, log *slog.Logger) *Scheduler {
	if opt.TickInterval <= 0 {
		opt.TickInterval = time.Second
	}
	if opt.LeaseTTL <= 0 {
		opt.LeaseTTL = 15 * time.Second
	}
	if opt.BatchSize <= 0 {
		opt.BatchSize = 200
	}
	if opt.DefaultLocation == nil {
		opt.DefaultLocation = time.UTC
	}
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{store: store, opt: opt, log: log}
}

func (s *Scheduler) Run(ctx context.Context) {
	_ = s.store.Q().EnsureNodeLeaseRow(ctx, "scheduler", s.opt.NodeID)
	t := time.NewTicker(s.opt.TickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
				s.log.Error("scheduler tick failed", "err", err)
			}
		}
	}
}

// Tick 单轮调度。
func (s *Scheduler) Tick(ctx context.Context) error {
	held, err := s.store.Q().HoldNodeLease(ctx, "scheduler", s.opt.NodeID, s.opt.LeaseTTL)
	if err != nil {
		return err
	}
	if !held {
		return nil
	}
	due, err := s.store.Q().ListDueSchedules(ctx, s.opt.BatchSize)
	if err != nil {
		return err
	}
	now, err := s.store.Q().DBNow(ctx)
	if err != nil {
		now = time.Now().UTC()
	}
	for _, d := range due {
		if err := s.fireOne(ctx, d, now); err != nil && !errors.Is(err, repo.ErrNotFound) {
			s.log.Warn("fire schedule failed", "schedule_id", d.ScheduleID, "task_id", d.TaskID, "err", err)
		}
	}
	return nil
}

func (s *Scheduler) fireOne(ctx context.Context, d *repo.DueSchedule, now time.Time) error {
	return s.store.InTx(ctx, func(q *repo.Queries) error {
		locked, err := q.LockScheduleForUpdate(ctx, d.ScheduleID)
		if err != nil {
			return err
		}
		cronSched, err := task.ValidateCron(locked.CronExpression)
		if err != nil {
			return err
		}
		loc, err := schedule.ResolveLocation(locked.Timezone, s.opt.DefaultLocation)
		if err != nil {
			loc = s.opt.DefaultLocation
		}
		plan := schedule.ComputeFires(cronSched, locked.NextFireAt, now, loc, locked.MisfirePolicy, locked.MisfireThresholdSeconds, locked.CatchUpMax)

		rp := schedule.ParseRetryPolicy(locked.RetryPolicy)
		major := parseMajor(locked.HandlerVersion)

		if !plan.SkipAuditAt.IsZero() {
			payload := map[string]any{"missed": plan.SkippedCount}
			if err := insertFire(ctx, q, locked, plan.SkipAuditAt, "CRON", "SKIPPED", "MISFIRE_SKIP", nil, nil, rp, major, payload); err != nil {
				return err
			}
		}
		for _, tick := range plan.Fires {
			if err := createWithConcurrency(ctx, q, locked, tick, rp, major); err != nil {
				return err
			}
		}

		base := now
		if !plan.LastTick.IsZero() && plan.LastTick.After(base) {
			base = plan.LastTick
		}
		if plan.LastTick.IsZero() {
			base = locked.NextFireAt
			if !base.After(now) {
				base = now
			}
		}
		next := schedule.NextFire(cronSched, base, loc)
		last := plan.LastTick
		if last.IsZero() {
			last = locked.NextFireAt
		}
		return q.AdvanceSchedule(ctx, locked.ScheduleID, next, last)
	})
}

func createWithConcurrency(ctx context.Context, q *repo.Queries, locked *repo.DueSchedule, tick schedule.FireTick, rp schedule.RetryPolicy, major int) error {
	slot := schedule.SlotKey(locked.ConcurrencyScope, locked.TaskID, locked.ServiceID, "")
	policy := locked.ConcurrencyPolicy
	if policy == "" {
		policy = "SKIP"
	}

	status := "PENDING"
	reason := ""
	var slotPtr *string
	var queuedAt *time.Time

	switch policy {
	case "ALLOW":
		active, err := q.CountActiveExecutions(ctx, locked.TaskID)
		if err != nil {
			return err
		}
		max := locked.MaxConcurrency
		if max < 1 {
			max = 1
		}
		if active >= max {
			status, reason = "SKIPPED", "CONCURRENCY_LIMIT"
		}
	case "QUEUE":
		occupied, err := q.SlotOccupied(ctx, slot)
		if err != nil {
			return err
		}
		if occupied {
			queued, err := q.CountQueuedExecutions(ctx, locked.TaskID)
			if err != nil {
				return err
			}
			if queued >= locked.QueueMax {
				status, reason = "SKIPPED", "QUEUE_FULL"
			} else {
				status, reason = "QUEUED", "CONCURRENCY_QUEUE"
				now := time.Now().UTC()
				queuedAt = &now
			}
		} else {
			slotPtr = &slot
		}
	default: // SKIP
		occupied, err := q.SlotOccupied(ctx, slot)
		if err != nil {
			return err
		}
		if occupied {
			status, reason = "SKIPPED", "CONCURRENCY_SKIP"
		} else {
			slotPtr = &slot
		}
	}

	return insertFire(ctx, q, locked, tick.ScheduledAt, tick.TriggerType, status, reason, slotPtr, queuedAt, rp, major, nil)
}

func insertFire(ctx context.Context, q *repo.Queries, locked *repo.DueSchedule, scheduledAt time.Time, trigger, status, reason string, slot *string, queuedAt *time.Time, rp schedule.RetryPolicy, major int, payload map[string]any) error {
	execID := idgen.NewULID()
	in := repo.CronExecutionInput{
		ExecutionID: execID, ScheduleID: locked.ScheduleID, TaskID: locked.TaskID, ServiceID: locked.ServiceID,
		TaskCode: locked.TaskCode, TriggerType: trigger, ScheduledAt: scheduledAt, Params: json.RawMessage(`{}`),
		Status: status, StatusReason: reason, ConcurrencySlot: slot, HandlerVersion: locked.HandlerVersion,
		HandlerMajor: major, SchemaHash: locked.SchemaHash, TimeoutSeconds: locked.TimeoutSeconds,
		RetryPolicy: locked.RetryPolicy, MaxAttempts: rp.MaxAttempts, QueuedAt: queuedAt,
	}
	if err := q.InsertCronExecution(ctx, in); err != nil {
		if errors.Is(err, repo.ErrDuplicate) {
			// uk_schedule_tick：同计划点已存在 → 静默
			n, _ := q.CountExecutionsByScheduleTick(ctx, locked.ScheduleID, scheduledAt)
			if n > 0 {
				return nil
			}
			// 否则视为 concurrency_slot 冲突 → 无槽 SKIPPED 再插
			if status == "PENDING" || status == "QUEUED" {
				in.ExecutionID = idgen.NewULID()
				in.Status, in.StatusReason, in.ConcurrencySlot, in.QueuedAt = "SKIPPED", "CONCURRENCY_SKIP", nil, nil
				if err2 := q.InsertCronExecution(ctx, in); err2 != nil {
					if errors.Is(err2, repo.ErrDuplicate) {
						return nil
					}
					return err2
				}
				_ = q.InsertEvent(ctx, in.ExecutionID, "", "EXECUTION_STATE", "", in.Status, "SYSTEM", "scheduler", "", payload)
			}
			return nil
		}
		return err
	}
	_ = q.InsertEvent(ctx, execID, "", "EXECUTION_STATE", "", status, "SYSTEM", "scheduler", "", payload)
	return nil
}

func parseMajor(v string) int {
	var major int
	_, _ = fmt.Sscanf(v, "%d", &major)
	return major
}
