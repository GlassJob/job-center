package service

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/GlassJob/job-center/internal/model"
	"github.com/GlassJob/job-center/internal/pkg/idgen"
	"github.com/GlassJob/job-center/internal/repository/mysql"
)

var scheduleCodeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// HorizonGenerator 由 scheduler 实现：对单个 schedule 立即预生成 triggers，并唤醒 Claim。
type HorizonGenerator interface {
	GenerateForSchedule(ctx context.Context, scheduleID string) error
	WakeClaim()
}

type Schedules struct {
	store *mysql.Store
	gen   HorizonGenerator
}

func NewSchedules(store *mysql.Store) *Schedules {
	return &Schedules{store: store}
}

func (s *Schedules) SetHorizonGenerator(g HorizonGenerator) {
	s.gen = g
}

type CreateScheduleInput struct {
	Code                    string          `json:"code"`
	Name                    string          `json:"name"`
	Enabled                 *bool           `json:"enabled"`
	ManualEnabled           *bool           `json:"manual_enabled"`
	CronExpression          *string         `json:"cron_expression"`
	Timezone                *string         `json:"timezone"`
	DefaultParams           *map[string]any `json:"default_params"`
	TimeoutSeconds          *uint           `json:"timeout_seconds"`
	MaxRetries              *uint           `json:"max_retries"`
	RetryBackoff            *map[string]any `json:"retry_backoff"`
	ConcurrencyPolicy       *string         `json:"concurrency_policy"`
	ConcurrencyScope        *string         `json:"concurrency_scope"`
	MaxConcurrency          *uint           `json:"max_concurrency"`
	QueueMax                *uint           `json:"queue_max"`
	MisfirePolicy           *string         `json:"misfire_policy"`
	MisfireThresholdSeconds *uint           `json:"misfire_threshold_seconds"`
	CatchUpMax              *uint           `json:"catch_up_max"`
	DispatchStrategy        *string         `json:"dispatch_strategy"`
}

type PatchScheduleInput struct {
	Version                 *uint           `json:"version"`
	Name                    *string         `json:"name"`
	Enabled                 *bool           `json:"enabled"`
	ManualEnabled           *bool           `json:"manual_enabled"`
	CronExpression          *string         `json:"cron_expression"`
	ClearCronExpression     bool            `json:"clear_cron_expression"`
	Timezone                *string         `json:"timezone"`
	DefaultParams           *map[string]any `json:"default_params"`
	ClearDefaultParams      bool            `json:"clear_default_params"`
	TimeoutSeconds          *uint           `json:"timeout_seconds"`
	MaxRetries              *uint           `json:"max_retries"`
	RetryBackoff            *map[string]any `json:"retry_backoff"`
	ConcurrencyPolicy       *string         `json:"concurrency_policy"`
	ConcurrencyScope        *string         `json:"concurrency_scope"`
	MaxConcurrency          *uint           `json:"max_concurrency"`
	QueueMax                *uint           `json:"queue_max"`
	MisfirePolicy           *string         `json:"misfire_policy"`
	MisfireThresholdSeconds *uint           `json:"misfire_threshold_seconds"`
	CatchUpMax              *uint           `json:"catch_up_max"`
	DispatchStrategy        *string         `json:"dispatch_strategy"`
}

func (s *Schedules) ensureJob(ctx context.Context, serviceID, jobID string) error {
	_, err := s.store.GetJob(ctx, serviceID, jobID)
	if errors.Is(err, mysql.ErrNotFound) {
		return NotFoundf("任务")
	}
	return err
}

func validateJSONObject(name string, v *map[string]any, allowNil bool) error {
	if v == nil {
		if allowNil {
			return nil
		}
		return Invalidf("%s", name)
	}
	if _, err := json.Marshal(*v); err != nil {
		return Invalidf("%s", name)
	}
	return nil
}

func validateScheduleEnums(policy, scope, misfire, dispatch string) error {
	switch policy {
	case "", model.ConcurrencyAllow, model.ConcurrencyForbid, model.ConcurrencyQueue:
	default:
		return Invalidf("并发策略")
	}
	switch scope {
	case "", model.ConcurrencyScopeJob, model.ConcurrencyScopeSchedule, model.ConcurrencyScopeIdempotencyKey:
	default:
		return Invalidf("并发范围")
	}
	switch misfire {
	case "", model.MisfireSkip, model.MisfireFireOnce, model.MisfireCatchUp:
	default:
		return Invalidf("错过策略")
	}
	switch dispatch {
	case "", model.DispatchLeastLoad, model.DispatchRoundRobin, model.DispatchRandom:
	default:
		return Invalidf("派发策略")
	}
	return nil
}

func (s *Schedules) List(ctx context.Context, serviceID, jobID, keyword, enabledRaw string, page, limit int) ([]model.ScheduleView, int64, error) {
	if err := s.ensureJob(ctx, serviceID, jobID); err != nil {
		return nil, 0, err
	}
	var enabled *bool
	if strings.TrimSpace(enabledRaw) != "" {
		v, err := strconv.ParseBool(strings.TrimSpace(enabledRaw))
		if err != nil {
			return nil, 0, Invalidf("enabled 参数")
		}
		enabled = &v
	}
	list, total, err := s.store.ListSchedules(ctx, jobID, keyword, enabled, page, limit)
	if err != nil {
		return nil, 0, err
	}
	out := make([]model.ScheduleView, 0, len(list))
	for i := range list {
		out = append(out, list[i].View())
	}
	return out, total, nil
}

func (s *Schedules) Get(ctx context.Context, serviceID, jobID, id string) (*model.ScheduleView, error) {
	if err := s.ensureJob(ctx, serviceID, jobID); err != nil {
		return nil, err
	}
	sch, err := s.store.GetSchedule(ctx, jobID, id)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	v := sch.View()
	return &v, nil
}

func (s *Schedules) Create(ctx context.Context, serviceID, jobID string, in CreateScheduleInput, updatedBy string) (*model.ScheduleView, error) {
	if err := s.ensureJob(ctx, serviceID, jobID); err != nil {
		return nil, err
	}
	code := strings.TrimSpace(in.Code)
	name := strings.TrimSpace(in.Name)
	if !scheduleCodeRe.MatchString(code) {
		return nil, Invalidf("编码")
	}
	if name == "" || len(name) > 128 {
		return nil, Invalidf("名称")
	}
	enabled := false
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	manual := true
	if in.ManualEnabled != nil {
		manual = *in.ManualEnabled
	}
	cron := ""
	if in.CronExpression != nil {
		cron = strings.TrimSpace(*in.CronExpression)
	}
	if enabled && cron == "" {
		return nil, Invalidf("启用时必须填写 Cron 表达式")
	}
	tz := "UTC"
	if in.Timezone != nil {
		tz = strings.TrimSpace(*in.Timezone)
		if tz == "" || len(tz) > 64 {
			return nil, Invalidf("时区")
		}
	}
	if err := validateJSONObject("默认参数", in.DefaultParams, true); err != nil {
		return nil, err
	}
	if err := validateJSONObject("重试退避", in.RetryBackoff, true); err != nil {
		return nil, err
	}
	policy, scope, misfire, dispatch := "", "", "", ""
	if in.ConcurrencyPolicy != nil {
		policy = strings.TrimSpace(*in.ConcurrencyPolicy)
	}
	if in.ConcurrencyScope != nil {
		scope = strings.TrimSpace(*in.ConcurrencyScope)
	}
	if in.MisfirePolicy != nil {
		misfire = strings.TrimSpace(*in.MisfirePolicy)
	}
	if in.DispatchStrategy != nil {
		dispatch = strings.TrimSpace(*in.DispatchStrategy)
	}
	if err := validateScheduleEnums(policy, scope, misfire, dispatch); err != nil {
		return nil, err
	}
	timeout := uint(300)
	if in.TimeoutSeconds != nil {
		timeout = *in.TimeoutSeconds
		if timeout < 1 || timeout > 604800 {
			return nil, Invalidf("超时秒数")
		}
	}
	var maxRetries uint
	if in.MaxRetries != nil {
		maxRetries = *in.MaxRetries
	}
	maxConc := uint(1)
	if in.MaxConcurrency != nil {
		maxConc = *in.MaxConcurrency
		if maxConc < 1 {
			return nil, Invalidf("最大并发")
		}
	}
	queueMax := uint(10)
	if in.QueueMax != nil {
		queueMax = *in.QueueMax
	}
	misfireTh := uint(60)
	if in.MisfireThresholdSeconds != nil {
		misfireTh = *in.MisfireThresholdSeconds
	}
	catchUp := uint(10)
	if in.CatchUpMax != nil {
		catchUp = *in.CatchUpMax
		if catchUp < 1 || catchUp > 100 {
			return nil, Invalidf("追赶上限")
		}
	}
	taken, err := s.store.ExistsScheduleCode(ctx, jobID, code, "")
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, Conflictf("编码已存在")
	}
	if updatedBy == "" {
		updatedBy = "admin"
	}
	var params map[string]any
	if in.DefaultParams != nil {
		params = *in.DefaultParams
	}
	backoff := map[string]any{"type": "fixed", "interval_seconds": 60}
	if in.RetryBackoff != nil {
		backoff = *in.RetryBackoff
	}
	sch := &model.Schedule{
		ID:                      idgen.New(),
		JobID:                   jobID,
		Code:                    code,
		Name:                    name,
		Enabled:                 enabled,
		ManualEnabled:           manual,
		CronExpression:          cron,
		Timezone:                tz,
		DefaultParams:           params,
		TimeoutSeconds:          timeout,
		MaxRetries:              maxRetries,
		RetryBackoff:            backoff,
		ConcurrencyPolicy:       firstNonEmpty(policy, model.ConcurrencyForbid),
		ConcurrencyScope:        firstNonEmpty(scope, model.ConcurrencyScopeJob),
		MaxConcurrency:          maxConc,
		QueueMax:                queueMax,
		MisfirePolicy:           firstNonEmpty(misfire, model.MisfireSkip),
		MisfireThresholdSeconds: misfireTh,
		CatchUpMax:              catchUp,
		DispatchStrategy:        firstNonEmpty(dispatch, model.DispatchLeastLoad),
		ConfigSource:            model.ScheduleConfigSourceAdmin,
		Version:                 0,
		UpdatedBy:               updatedBy,
	}
	if err := s.store.CreateSchedule(ctx, sch); err != nil {
		if errors.Is(err, mysql.ErrConflict) {
			return nil, ErrConflict
		}
		return nil, err
	}
	got, err := s.store.GetSchedule(ctx, jobID, sch.ID)
	if err != nil {
		return nil, err
	}
	s.requestGenerate(ctx, got)
	v := got.View()
	return &v, nil
}

func firstNonEmpty(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func (s *Schedules) Patch(ctx context.Context, serviceID, jobID, id string, in PatchScheduleInput, updatedBy string) (*model.ScheduleView, error) {
	if err := s.ensureJob(ctx, serviceID, jobID); err != nil {
		return nil, err
	}
	if in.Version == nil {
		return nil, Invalidf("缺少 version（乐观锁）")
	}
	cur, err := s.store.GetSchedule(ctx, jobID, id)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	p := mysql.SchedulePatch{ExpectedVersion: *in.Version, UpdatedBy: updatedBy}
	changed := false
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len(n) > 128 {
			return nil, Invalidf("名称")
		}
		p.Name = &n
		changed = true
	}
	if in.Enabled != nil {
		p.Enabled = in.Enabled
		changed = true
	}
	if in.ManualEnabled != nil {
		p.ManualEnabled = in.ManualEnabled
		changed = true
	}
	if in.ClearCronExpression {
		p.ClearCron = true
		changed = true
	} else if in.CronExpression != nil {
		c := strings.TrimSpace(*in.CronExpression)
		p.CronExpression = &c
		changed = true
	}
	if in.Timezone != nil {
		tz := strings.TrimSpace(*in.Timezone)
		if tz == "" || len(tz) > 64 {
			return nil, Invalidf("时区")
		}
		p.Timezone = &tz
		changed = true
	}
	if in.ClearDefaultParams {
		p.ClearDefaultParams = true
		changed = true
	} else if in.DefaultParams != nil {
		if err := validateJSONObject("默认参数", in.DefaultParams, false); err != nil {
			return nil, err
		}
		p.DefaultParams = in.DefaultParams
		changed = true
	}
	if in.TimeoutSeconds != nil {
		t := *in.TimeoutSeconds
		if t < 1 || t > 604800 {
			return nil, Invalidf("超时秒数")
		}
		p.TimeoutSeconds = &t
		changed = true
	}
	if in.MaxRetries != nil {
		p.MaxRetries = in.MaxRetries
		changed = true
	}
	if in.RetryBackoff != nil {
		if err := validateJSONObject("重试退避", in.RetryBackoff, false); err != nil {
			return nil, err
		}
		p.RetryBackoff = in.RetryBackoff
		changed = true
	}
	policy, scope, misfire, dispatch := "", "", "", ""
	if in.ConcurrencyPolicy != nil {
		policy = strings.TrimSpace(*in.ConcurrencyPolicy)
		p.ConcurrencyPolicy = &policy
		changed = true
	}
	if in.ConcurrencyScope != nil {
		scope = strings.TrimSpace(*in.ConcurrencyScope)
		p.ConcurrencyScope = &scope
		changed = true
	}
	if in.MisfirePolicy != nil {
		misfire = strings.TrimSpace(*in.MisfirePolicy)
		p.MisfirePolicy = &misfire
		changed = true
	}
	if in.DispatchStrategy != nil {
		dispatch = strings.TrimSpace(*in.DispatchStrategy)
		p.DispatchStrategy = &dispatch
		changed = true
	}
	if err := validateScheduleEnums(policy, scope, misfire, dispatch); err != nil {
		return nil, err
	}
	if in.MaxConcurrency != nil {
		if *in.MaxConcurrency < 1 {
			return nil, Invalidf("最大并发")
		}
		p.MaxConcurrency = in.MaxConcurrency
		changed = true
	}
	if in.QueueMax != nil {
		p.QueueMax = in.QueueMax
		changed = true
	}
	if in.MisfireThresholdSeconds != nil {
		p.MisfireThresholdSeconds = in.MisfireThresholdSeconds
		changed = true
	}
	if in.CatchUpMax != nil {
		if *in.CatchUpMax < 1 || *in.CatchUpMax > 100 {
			return nil, Invalidf("追赶上限")
		}
		p.CatchUpMax = in.CatchUpMax
		changed = true
	}
	if !changed {
		v := cur.View()
		return &v, nil
	}

	// enabled + cron constraint against resulting state
	willEnable := cur.Enabled
	if p.Enabled != nil {
		willEnable = *p.Enabled
	}
	willCron := cur.CronExpression
	if p.ClearCron {
		willCron = ""
	} else if p.CronExpression != nil {
		willCron = *p.CronExpression
	}
	if willEnable && strings.TrimSpace(willCron) == "" {
		return nil, Invalidf("启用时必须填写 Cron 表达式")
	}
	if updatedBy == "" {
		p.UpdatedBy = "admin"
	}

	sch, err := s.store.PatchScheduleCAS(ctx, jobID, id, p)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrNotFound
	}
	if errors.Is(err, mysql.ErrConflict) {
		return nil, Conflictf("版本冲突，请刷新后重试")
	}
	if err != nil {
		return nil, err
	}

	// cron/timezone/禁用变化时作废未来 triggers，清空 horizon 以便重生。
	redefined := p.ClearCron || p.CronExpression != nil || p.Timezone != nil
	disabled := p.Enabled != nil && !*p.Enabled
	enabledOn := p.Enabled != nil && *p.Enabled
	if redefined || disabled {
		s.invalidateFutureTriggers(ctx, id, "SCHEDULE_UPDATED")
	}
	if !disabled && (redefined || enabledOn) {
		s.requestGenerate(ctx, sch)
	}

	v := sch.View()
	return &v, nil
}

func (s *Schedules) invalidateFutureTriggers(ctx context.Context, scheduleID, _ string) {
	now := time.Now().UTC()
	// 先删 now 之后的 PENDING（及占唯一键的 CLAIMED/CANCELLED），再由 Generate 插入。
	_, _ = s.store.DeleteFuturePendingTriggers(ctx, scheduleID, now)
	_ = s.store.ClearScheduleHorizon(ctx, scheduleID)
	if s.gen != nil {
		s.gen.WakeClaim()
	}
}

func (s *Schedules) requestGenerate(ctx context.Context, sch *model.Schedule) {
	if s.gen == nil || sch == nil {
		return
	}
	if !sch.Enabled || strings.TrimSpace(sch.CronExpression) == "" {
		return
	}
	_ = s.gen.GenerateForSchedule(ctx, sch.ID)
}

func validTriggerStatus(status string) bool {
	switch status {
	case "", model.TriggerPending, model.TriggerClaimed, model.TriggerFired, model.TriggerSkipped, model.TriggerCancelled:
		return true
	default:
		return false
	}
}

func (s *Schedules) ListTriggers(
	ctx context.Context,
	scheduleID, jobID, serviceID, status, fireFrom, fireTo string,
	page, limit int,
) ([]model.ScheduleTriggerView, int64, error) {
	status = strings.TrimSpace(status)
	if !validTriggerStatus(status) {
		return nil, 0, Invalidf("状态")
	}
	from, err := parseOptionalTime("fire_from", fireFrom)
	if err != nil {
		return nil, 0, err
	}
	to, err := parseOptionalTime("fire_to", fireTo)
	if err != nil {
		return nil, 0, err
	}
	if from != nil && to != nil && !from.Before(*to) {
		return nil, 0, Invalidf("开始时间必须早于结束时间")
	}
	if sid := strings.TrimSpace(serviceID); sid != "" {
		if _, err := s.store.GetService(ctx, sid); errors.Is(err, mysql.ErrNotFound) {
			return nil, 0, NotFoundf("服务")
		} else if err != nil {
			return nil, 0, err
		}
	}
	if jid := strings.TrimSpace(jobID); jid != "" {
		if _, err := s.store.GetJobByID(ctx, jid); errors.Is(err, mysql.ErrNotFound) {
			return nil, 0, NotFoundf("任务")
		} else if err != nil {
			return nil, 0, err
		}
	}
	if schID := strings.TrimSpace(scheduleID); schID != "" {
		if _, err := s.store.GetScheduleByID(ctx, schID); errors.Is(err, mysql.ErrNotFound) {
			return nil, 0, NotFoundf("调度")
		} else if err != nil {
			return nil, 0, err
		}
	}

	rows, total, err := s.store.ListTriggers(ctx, mysql.ListTriggersFilter{
		ScheduleID: scheduleID,
		JobID:      jobID,
		ServiceID:  serviceID,
		Status:     status,
		FireFrom:   from,
		FireTo:     to,
		Page:       page,
		Limit:      limit,
	})
	if err != nil {
		return nil, 0, err
	}
	out := make([]model.ScheduleTriggerView, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].View())
	}
	return out, total, nil
}

func (s *Schedules) ListTriggersBySchedule(
	ctx context.Context,
	serviceID, jobID, scheduleID, status, fireFrom, fireTo string,
	page, limit int,
) ([]model.ScheduleTriggerView, int64, error) {
	if _, err := s.Get(ctx, serviceID, jobID, scheduleID); err != nil {
		return nil, 0, err
	}
	return s.ListTriggers(ctx, scheduleID, "", "", status, fireFrom, fireTo, page, limit)
}

type RegenerateScheduleResult struct {
	ScheduleID string `json:"schedule_id"`
	OK         bool   `json:"ok"`
}

func (s *Schedules) Regenerate(ctx context.Context, serviceID, jobID, scheduleID string) (*RegenerateScheduleResult, error) {
	schView, err := s.Get(ctx, serviceID, jobID, scheduleID)
	if err != nil {
		return nil, err
	}
	if !schView.Enabled {
		return nil, Invalidf("调度未启用")
	}
	if strings.TrimSpace(schView.CronExpression) == "" {
		return nil, Invalidf("调度未配置 Cron")
	}
	if s.gen == nil {
		return nil, Invalidf("调度器未启用")
	}
	if err := s.gen.GenerateForSchedule(ctx, scheduleID); err != nil {
		return nil, err
	}
	return &RegenerateScheduleResult{ScheduleID: scheduleID, OK: true}, nil
}

type DeleteScheduleResult struct {
	Hard     bool                `json:"hard"`
	Schedule *model.ScheduleView `json:"schedule,omitempty"`
}

func (s *Schedules) Delete(ctx context.Context, serviceID, jobID, id, updatedBy string, hard bool) (*DeleteScheduleResult, error) {
	if err := s.ensureJob(ctx, serviceID, jobID); err != nil {
		return nil, err
	}
	cur, err := s.store.GetSchedule(ctx, jobID, id)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !hard {
		if !cur.Enabled {
			v := cur.View()
			return &DeleteScheduleResult{Hard: false, Schedule: &v}, nil
		}
		if updatedBy == "" {
			updatedBy = "admin"
		}
		sch, err := s.store.SoftDisableSchedule(ctx, jobID, id, updatedBy, cur.Version)
		if errors.Is(err, mysql.ErrNotFound) {
			return nil, ErrNotFound
		}
		if errors.Is(err, mysql.ErrConflict) {
			return nil, Conflictf("版本冲突，请刷新后重试")
		}
		if err != nil {
			return nil, err
		}
		s.invalidateFutureTriggers(ctx, id, "SCHEDULE_DISABLED")
		v := sch.View()
		return &DeleteScheduleResult{Hard: false, Schedule: &v}, nil
	}
	n, err := s.store.CountScheduleExecutions(ctx, id)
	if err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, Conflictf("调度仍有依赖的执行记录（executions=%d）", n)
	}
	_, _ = s.store.CancelAllOpenTriggers(ctx, id, "SCHEDULE_DELETED")
	if err := s.store.DeleteSchedule(ctx, jobID, id); err != nil {
		if errors.Is(err, mysql.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &DeleteScheduleResult{Hard: true}, nil
}
