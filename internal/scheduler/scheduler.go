package scheduler

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/GlassJob/job-center/internal/config"
	"github.com/GlassJob/job-center/internal/model"
	"github.com/GlassJob/job-center/internal/pkg/idgen"
	"github.com/GlassJob/job-center/internal/repository/mysql"
	"github.com/GlassJob/job-center/internal/service"
	"github.com/robfig/cron/v3"
)

var cronParser = cron.NewParser(
	cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// claimScheduleState 同 schedule 在本批 claim 内的 misfire 计数。
type claimScheduleState struct {
	sch          *model.Schedule
	misfireFired int
}

type Scheduler struct {
	store      *mysql.Store
	executions *service.Executions
	cfg        config.SchedulerConfig
	nodeID     string
	log        *slog.Logger
	wake       chan struct{}
	claim      loopTracker
	generate   loopTracker
	queued     loopTracker
}

func New(store *mysql.Store, executions *service.Executions, cfg config.SchedulerConfig, nodeID string, log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	s := &Scheduler{
		store:      store,
		executions: executions,
		cfg:        cfg,
		nodeID:     nodeID,
		log:        log.With("component", "scheduler"),
		wake:       make(chan struct{}, 1),
	}
	init := PhaseIdle
	if !cfg.Enabled {
		init = PhaseDisabled
	}
	s.claim.set(init, "", "")
	s.generate.set(init, "", "")
	s.queued.set(init, "", "")
	return s
}

// Status 只读工作状态（Admin 用）。
func (s *Scheduler) Status() Status {
	return Status{
		Enabled:  s.cfg.Enabled,
		NodeID:   s.nodeID,
		Claim:    s.claim.snapshot(),
		Generate: s.generate.snapshot(),
		Queued:   s.queued.snapshot(),
	}
}

// WakeClaim 排班变更后唤醒 Claim（重新算 next fire）。实现 service.HorizonGenerator。
func (s *Scheduler) WakeClaim() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Start 启动 Generate / Claim / QueuedScanner 三循环，直到 ctx 取消。
func (s *Scheduler) Start(ctx context.Context) {
	if !s.cfg.Enabled {
		s.log.Info("scheduler disabled")
		s.claim.set(PhaseDisabled, "", "")
		s.generate.set(PhaseDisabled, "", "")
		s.queued.set(PhaseDisabled, "", "")
		return
	}
	s.log.Info("scheduler started",
		"generate_interval", s.cfg.GenerateIntervalDuration().String(),
		"claim_lead", s.cfg.ClaimLeadDuration().String(),
		"queue_interval", s.cfg.QueueIntervalDuration().String(),
		"horizon", s.cfg.HorizonDuration().String(),
	)
	go s.loop(ctx, &s.generate, "generate", s.cfg.GenerateIntervalDuration(), s.runGenerate)
	go s.claimLoop(ctx)
	go s.loop(ctx, &s.queued, "queued", s.cfg.QueueIntervalDuration(), s.runQueued)
	<-ctx.Done()
	s.claim.set(PhaseStopped, "", "")
	s.generate.set(PhaseStopped, "", "")
	s.queued.set(PhaseStopped, "", "")
	s.log.Info("scheduler stopped")
}

func (s *Scheduler) loop(ctx context.Context, tr *loopTracker, name string, every time.Duration, fn func(context.Context) string) {
	if every <= 0 {
		every = time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	tr.set(PhaseRunning, name, "")
	result := fn(ctx)
	tr.set(PhaseIdle, "waiting next tick", result)
	for {
		select {
		case <-ctx.Done():
			tr.set(PhaseStopped, "", "")
			return
		case <-t.C:
			tr.set(PhaseRunning, name, "")
			result := fn(ctx)
			tr.set(PhaseIdle, "waiting next tick", result)
		}
	}
}

func (s *Scheduler) claimLoop(ctx context.Context) {
	lead := s.cfg.ClaimLeadDuration()
	if lead < 0 {
		lead = time.Second
	}

	for {
		if ctx.Err() != nil {
			s.claim.set(PhaseStopped, "", "")
			return
		}
		s.claim.set(PhaseRunning, "claiming due triggers", "")
		n := s.runClaim(ctx)
		if n > 0 {
			s.claim.set(PhaseRunning, "draining due triggers", "claimed="+strconv.Itoa(n))
			continue // 排空 due
		}

		// 有 PENDING：睡到 fire_at - lead；无 PENDING：只等 WakeClaim（Generate/排班变更），不再轮询扫库。
		next, err := s.store.EarliestPendingFireAt(ctx)
		if err != nil {
			s.log.Error("earliest pending fire", "err", err)
			s.claim.set(PhaseSleeping, "earliest_fire_error wait=wake", "claimed=0")
			select {
			case <-ctx.Done():
				s.claim.set(PhaseStopped, "", "")
				return
			case <-s.wake:
				s.claim.set(PhaseIdle, "woken by reschedule", "")
			}
			continue
		}
		if next == nil {
			s.claim.set(PhaseSleeping, "no_pending_triggers wait=wake", "claimed=0")
			select {
			case <-ctx.Done():
				s.claim.set(PhaseStopped, "", "")
				return
			case <-s.wake:
				s.claim.set(PhaseIdle, "woken by reschedule", "")
			}
			continue
		}

		now := time.Now().UTC()
		until := next.Sub(now) - lead
		if until <= 0 {
			continue
		}
		s.claim.set(PhaseSleeping, "sleep_until_next_fire wait="+until.String(), "claimed=0")
		timer := time.NewTimer(until)
		select {
		case <-ctx.Done():
			timer.Stop()
			s.claim.set(PhaseStopped, "", "")
			return
		case <-s.wake:
			timer.Stop()
			s.claim.set(PhaseIdle, "woken by reschedule", "")
		case <-timer.C:
			s.claim.set(PhaseIdle, "sleep elapsed", "")
		}
	}
}

// GenerateForSchedule 立即为单个 schedule 预生成到 horizon（Admin Create/Patch/Regenerate 调用）。
// 重生策略：先删 now 之后的 PENDING（及占位行），清空水位，再插入。
func (s *Scheduler) GenerateForSchedule(ctx context.Context, scheduleID string) error {
	defer s.WakeClaim()
	if !s.cfg.Enabled {
		return nil
	}
	sch, err := s.store.GetScheduleByID(ctx, scheduleID)
	if err != nil {
		return err
	}
	return s.regenerateOne(ctx, sch)
}

// RegenerateItemError 全平台重生中单条失败。
type RegenerateItemError struct {
	ScheduleID string `json:"schedule_id"`
	Message    string `json:"message"`
}

// RegenerateAllResult 全平台先删再插结果。
type RegenerateAllResult struct {
	Total     int                   `json:"total"`
	Succeeded int                   `json:"succeeded"`
	Failed    int                   `json:"failed"`
	Errors    []RegenerateItemError `json:"errors"`
}

// RegenerateAll 对全部 enabled+cron 的 schedules 逐个先删再插（同步）。
func (s *Scheduler) RegenerateAll(ctx context.Context) RegenerateAllResult {
	out := RegenerateAllResult{Errors: []RegenerateItemError{}}
	if !s.cfg.Enabled {
		return out
	}
	list, err := s.store.ListEnabledSchedulesWithCron(ctx)
	if err != nil {
		out.Errors = append(out.Errors, RegenerateItemError{ScheduleID: "", Message: err.Error()})
		out.Failed = 1
		return out
	}
	out.Total = len(list)
	for i := range list {
		if ctx.Err() != nil {
			out.Errors = append(out.Errors, RegenerateItemError{
				ScheduleID: list[i].ID,
				Message:    ctx.Err().Error(),
			})
			out.Failed++
			continue
		}
		if err := s.regenerateOne(ctx, &list[i]); err != nil {
			out.Failed++
			out.Errors = append(out.Errors, RegenerateItemError{
				ScheduleID: list[i].ID,
				Message:    err.Error(),
			})
			continue
		}
		out.Succeeded++
	}
	s.WakeClaim()
	return out
}

func (s *Scheduler) regenerateOne(ctx context.Context, sch *model.Schedule) error {
	if sch == nil {
		return nil
	}
	if !sch.Enabled || strings.TrimSpace(sch.CronExpression) == "" {
		return nil
	}
	now := time.Now().UTC()
	if _, err := s.store.DeleteFuturePendingTriggers(ctx, sch.ID, now); err != nil {
		return err
	}
	if err := s.store.ClearScheduleHorizon(ctx, sch.ID); err != nil {
		return err
	}
	sch.HorizonAt = nil
	return s.generateOne(ctx, sch, now, now.Add(s.cfg.HorizonDuration()))
}

func (s *Scheduler) runGenerate(ctx context.Context) string {
	list, err := s.store.ListEnabledSchedulesWithCron(ctx)
	if err != nil {
		s.log.Error("generate list schedules", "err", err)
		return "error"
	}
	now := time.Now().UTC()
	horizon := s.cfg.HorizonDuration()
	target := now.Add(horizon)
	for i := range list {
		if err := s.generateOne(ctx, &list[i], now, target); err != nil {
			s.log.Warn("generate schedule", "schedule_id", list[i].ID, "err", err)
		}
	}
	s.WakeClaim()
	return "schedules=" + strconv.Itoa(len(list))
}

func (s *Scheduler) generateOne(ctx context.Context, sch *model.Schedule, now, target time.Time) error {
	expr := strings.TrimSpace(sch.CronExpression)
	if expr == "" {
		return nil
	}
	sched, err := cronParser.Parse(expr)
	if err != nil {
		return err
	}
	loc, err := time.LoadLocation(sch.Timezone)
	if err != nil || loc == nil {
		loc = time.UTC
	}

	start := now
	if sch.HorizonAt != nil && sch.HorizonAt.After(start) {
		start = *sch.HorizonAt
	}
	if !start.Before(target) {
		return nil
	}

	const batchSize = 5000
	cursor := start.In(loc)
	for {
		triggers := make([]model.ScheduleTrigger, 0, batchSize)
		reachedEnd := false
		for len(triggers) < batchSize {
			next := sched.Next(cursor)
			if next.IsZero() || next.After(target) {
				reachedEnd = true
				break
			}
			fire := next.UTC()
			triggers = append(triggers, model.ScheduleTrigger{
				ID:         idgen.New(),
				ScheduleID: sch.ID,
				JobID:      sch.JobID,
				FireAt:     fire,
				Status:     model.TriggerPending,
			})
			cursor = next
		}
		if err := s.store.InsertTriggersIgnore(ctx, triggers); err != nil {
			return err
		}
		nextFire, err := s.store.NextPendingFireAt(ctx, sch.ID, now)
		if err != nil {
			return err
		}
		if reachedEnd {
			return s.store.UpdateScheduleHorizon(ctx, sch.ID, target, nextFire)
		}
		// 批次未盖满 horizon：水位先推到本批最后一点，下轮/日扫继续补。
		last := triggers[len(triggers)-1].FireAt
		if err := s.store.UpdateScheduleHorizon(ctx, sch.ID, last, nextFire); err != nil {
			return err
		}
		sch.HorizonAt = &last
	}
}

func (s *Scheduler) runClaim(ctx context.Context) int {
	now := time.Now().UTC()
	batch := s.cfg.ClaimBatch
	if batch < 1 {
		batch = 50
	}
	claimed, err := s.store.ClaimDueTriggers(ctx, s.nodeID, now, batch, s.cfg.ClaimTTLDuration())
	if err != nil {
		s.log.Error("claim due triggers", "err", err)
		return 0
	}
	if len(claimed) == 0 {
		return 0
	}

	// misfire 计数：FIRE_ONCE 仅补 1 次；CATCH_UP 按 catch_up_max 截断本批逾期火。
	states := map[string]*claimScheduleState{}

	for i := range claimed {
		tr := &claimed[i]
		st, ok := states[tr.ScheduleID]
		if !ok {
			sch, err := s.store.GetScheduleByID(ctx, tr.ScheduleID)
			if err != nil {
				_ = s.store.MarkTriggerSkipped(ctx, tr.ID, "SCHEDULE_NOT_FOUND")
				continue
			}
			st = &claimScheduleState{sch: sch}
			states[tr.ScheduleID] = st
		}
		s.handleClaimed(ctx, tr, st, now)
	}
	return len(claimed)
}

func (s *Scheduler) handleClaimed(ctx context.Context, tr *model.ScheduleTrigger, st *claimScheduleState, now time.Time) {
	sch := st.sch
	if !sch.Enabled {
		_ = s.store.MarkTriggerSkipped(ctx, tr.ID, "SCHEDULE_DISABLED")
		return
	}

	threshold := time.Duration(sch.MisfireThresholdSeconds) * time.Second
	if threshold <= 0 {
		threshold = 60 * time.Second
	}
	overdue := now.Sub(tr.FireAt) > threshold

	if overdue {
		switch sch.MisfirePolicy {
		case model.MisfireSkip, "":
			_ = s.store.MarkTriggerSkipped(ctx, tr.ID, "MISFIRE_SKIP")
			return
		case model.MisfireFireOnce:
			if st.misfireFired >= 1 {
				_ = s.store.MarkTriggerSkipped(ctx, tr.ID, "FIRE_ONCE_DONE")
				return
			}
		case model.MisfireCatchUp:
			maxFire := int(sch.CatchUpMax)
			if maxFire < 1 {
				maxFire = 10
			}
			if st.misfireFired >= maxFire {
				_ = s.store.MarkTriggerSkipped(ctx, tr.ID, "CATCH_UP_TRUNCATED")
				return
			}
		}
	}

	res, err := s.executions.CronTrigger(ctx, tr.ScheduleID, tr.FireAt, s.nodeID)
	if err != nil {
		s.log.Warn("cron trigger", "trigger_id", tr.ID, "schedule_id", tr.ScheduleID, "err", err)
		_ = s.store.MarkTriggerSkipped(ctx, tr.ID, "CRON_TRIGGER_ERROR")
		return
	}
	if res.Skipped {
		_ = s.store.MarkTriggerSkipped(ctx, tr.ID, res.SkipReason)
		return
	}
	if err := s.store.MarkTriggerFired(ctx, tr.ID, res.ExecutionID); err != nil {
		s.log.Warn("mark trigger fired", "trigger_id", tr.ID, "err", err)
		return
	}
	if overdue {
		st.misfireFired++
	}
	nextFire, _ := s.store.NextPendingFireAt(ctx, sch.ID, now)
	_ = s.store.TouchScheduleLastFire(ctx, sch.ID, tr.FireAt, nextFire)
}

func (s *Scheduler) runQueued(ctx context.Context) string {
	batch := s.cfg.QueueBatch
	if batch < 1 {
		batch = 50
	}
	recovered, err := s.executions.RecoverAbandoned(ctx, batch)
	if err != nil {
		s.log.Error("recover abandoned", "err", err)
		return "error"
	}
	n, err := s.executions.DispatchQueued(ctx, batch)
	if err != nil {
		s.log.Error("dispatch queued", "err", err)
		return "error"
	}
	if recovered > 0 || n > 0 {
		s.log.Info("queued dispatched", "recovered", recovered, "count", n)
	}
	return "recovered=" + strconv.Itoa(recovered) + " dispatched=" + strconv.Itoa(n)
}
