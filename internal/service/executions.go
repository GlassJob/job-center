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
		return nil, Invalidf("任务未启用或状态非激活")
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
		return s.afterNoCapacity(ctx, exec, now)
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

// afterNoCapacity Pick 失败：无 ONLINE → FAILED；有 ONLINE 但无空位 → QUEUED。
func (s *Executions) afterNoCapacity(ctx context.Context, exec *model.Execution, now time.Time) (*TriggerExecutionResult, error) {
	has, err := s.store.HasOnlineInstanceForJob(ctx, exec.ServiceID, exec.JobID)
	if err != nil {
		return nil, err
	}
	if !has {
		_ = s.store.UpdateExecutionFields(ctx, exec.ID, map[string]any{
			"status":         model.ExecStatusFailed,
			"finished_at":    now,
			"status_reason":  "NO_ONLINE_INSTANCE",
			"error_code":     "NO_ONLINE_INSTANCE",
			"error_message":  "无在线实例可执行该任务",
		})
		return &TriggerExecutionResult{
			ExecutionID: exec.ID,
			Status:      model.ExecStatusFailed,
			Message:     "无在线实例，已失败",
		}, nil
	}
	_ = s.store.UpdateExecutionFields(ctx, exec.ID, map[string]any{
		"status":    model.ExecStatusQueued,
		"queued_at": now,
	})
	return &TriggerExecutionResult{
		ExecutionID: exec.ID,
		Status:      model.ExecStatusQueued,
		Message:     "在线实例暂无空位，已排队",
	}, nil
}

// CronTriggerResult Cron 开火结果；Skipped 时不创建 execution。
type CronTriggerResult struct {
	ExecutionID string
	Status      string
	AttemptID   string
	InstanceID  string
	Skipped     bool
	SkipReason  string
	Replay      bool
	Message     string
}

// CronTrigger 由 DueClaimer 调用：按 schedule+fireAt 创建 CRON execution 并尝试派发。
func (s *Executions) CronTrigger(ctx context.Context, scheduleID string, fireAt time.Time, nodeID string) (*CronTriggerResult, error) {
	sch, err := s.store.GetScheduleByID(ctx, scheduleID)
	if errors.Is(err, mysql.ErrNotFound) {
		return &CronTriggerResult{Skipped: true, SkipReason: "SCHEDULE_NOT_FOUND"}, nil
	}
	if err != nil {
		return nil, err
	}
	if !sch.Enabled {
		return &CronTriggerResult{Skipped: true, SkipReason: "SCHEDULE_DISABLED"}, nil
	}

	job, err := s.store.GetJobByID(ctx, sch.JobID)
	if errors.Is(err, mysql.ErrNotFound) {
		return &CronTriggerResult{Skipped: true, SkipReason: "JOB_NOT_FOUND"}, nil
	}
	if err != nil {
		return nil, err
	}
	if job.Status != model.JobStatusActive || !job.Enabled {
		return &CronTriggerResult{Skipped: true, SkipReason: "JOB_NOT_ACTIVE"}, nil
	}

	if existing, err := s.store.GetExecutionByScheduleTick(ctx, scheduleID, fireAt); err == nil {
		out := &CronTriggerResult{
			ExecutionID: existing.ID,
			Status:      existing.Status,
			Replay:      true,
			Message:     "idempotent schedule tick",
		}
		if existing.CurrentAttemptID != nil {
			out.AttemptID = *existing.CurrentAttemptID
		}
		return out, nil
	} else if !errors.Is(err, mysql.ErrNotFound) {
		return nil, err
	}

	// FORBID：同 job 已有非终态 execution → 跳过本 tick。
	if sch.ConcurrencyPolicy == "" || sch.ConcurrencyPolicy == model.ConcurrencyForbid {
		n, err := s.store.CountActiveExecutionsForJob(ctx, job.ID)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			return &CronTriggerResult{Skipped: true, SkipReason: "CONCURRENCY_FORBID"}, nil
		}
	}

	params, err := mysql.MarshalJSONObject(sch.DefaultParams)
	if err != nil {
		return nil, err
	}
	timeout := sch.TimeoutSeconds
	if timeout == 0 {
		timeout = job.DefaultTimeoutSeconds
	}
	if timeout == 0 {
		timeout = 300
	}
	maxAttempts := sch.MaxRetries + 1
	if maxAttempts == 0 {
		maxAttempts = 1
	}
	var major *uint
	if job.HandlerVersion != "" {
		if m, ok := parseMajor(job.HandlerVersion); ok {
			major = &m
		}
	}
	sid := scheduleID
	fire := fireAt.UTC()
	triggeredBy := strings.TrimSpace(nodeID)
	if triggeredBy == "" {
		triggeredBy = "scheduler"
	}
	exec := &model.Execution{
		ID:          idgen.New(),
		ServiceID:   job.ServiceID,
		JobID:       job.ID,
		JobCode:     job.Code,
		ScheduleID:  &sid,
		TriggerType: model.TriggerCron,
		TriggeredBy: triggeredBy,
		TriggerContext: map[string]any{
			"schedule_id": scheduleID,
			"fire_at":     fire.Format(time.RFC3339Nano),
		},
		Params: params,
		PolicySnapshot: map[string]any{
			"max_attempts":       maxAttempts,
			"timeout_seconds":    timeout,
			"concurrency_policy": sch.ConcurrencyPolicy,
			"misfire_policy":     sch.MisfirePolicy,
		},
		Status:         model.ExecStatusPending,
		HandlerVersion: job.HandlerVersion,
		HandlerMajor:   major,
		SchemaHash:     job.SchemaHash,
		TimeoutSeconds: timeout,
		MaxAttempts:    maxAttempts,
		ScheduledAt:    &fire,
	}
	if err := s.store.CreateExecution(ctx, exec); err != nil {
		if isDup(err) {
			existing, e2 := s.store.GetExecutionByScheduleTick(ctx, scheduleID, fire)
			if e2 == nil {
				return &CronTriggerResult{
					ExecutionID: existing.ID,
					Status:      existing.Status,
					Replay:      true,
					Message:     "idempotent schedule tick",
				}, nil
			}
		}
		return nil, err
	}

	inst, err := s.store.PickOnlineInstanceForJob(ctx, job.ServiceID, job.ID, "")
	if errors.Is(err, mysql.ErrNotFound) {
		out, err := s.afterNoCapacity(ctx, exec, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		return &CronTriggerResult{
			ExecutionID: out.ExecutionID,
			Status:      out.Status,
			Message:     out.Message,
		}, nil
	}
	if err != nil {
		return nil, err
	}

	attempt, err := s.dispatch(ctx, exec, job, inst)
	if err != nil {
		return nil, err
	}
	return &CronTriggerResult{
		ExecutionID: exec.ID,
		Status:      model.ExecStatusDispatching,
		AttemptID:   attempt.ID,
		InstanceID:  inst.ID,
	}, nil
}

func validExecutionStatus(st string) bool {
	switch st {
	case model.ExecStatusPending, model.ExecStatusQueued, model.ExecStatusDispatching, model.ExecStatusRunning,
		model.ExecStatusRetryWait, model.ExecStatusSucceeded, model.ExecStatusFailed, model.ExecStatusCancelled,
		model.ExecStatusTimedOut, model.ExecStatusSkipped, model.ExecStatusUnknown:
		return true
	default:
		return false
	}
}

// List Admin 执行列表（全局或按服务/任务过滤）。
func (s *Executions) List(ctx context.Context, keyword, serviceID, jobID, status, instanceID, createdFrom, createdTo string, page, limit int) ([]model.ExecutionView, int64, error) {
	status = strings.TrimSpace(status)
	if status != "" && !validExecutionStatus(status) {
		return nil, 0, Invalidf("状态")
	}
	from, err := parseOptionalTime("created_from", createdFrom)
	if err != nil {
		return nil, 0, err
	}
	to, err := parseOptionalTime("created_to", createdTo)
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

	rows, total, err := s.store.ListExecutions(ctx, mysql.ListExecutionsFilter{
		Keyword: keyword, ServiceID: serviceID, JobID: jobID, Status: status, InstanceID: instanceID,
		CreatedFrom: from, CreatedTo: to, Page: page, Limit: limit,
	})
	if err != nil {
		return nil, 0, err
	}
	attemptIDs := make([]string, 0, len(rows))
	for i := range rows {
		if rows[i].CurrentAttemptID != nil && *rows[i].CurrentAttemptID != "" {
			attemptIDs = append(attemptIDs, *rows[i].CurrentAttemptID)
		}
	}
	attempts, err := s.store.GetAttemptsByIDs(ctx, attemptIDs)
	if err != nil {
		return nil, 0, err
	}
	out := make([]model.ExecutionView, 0, len(rows))
	for i := range rows {
		var att *model.Attempt
		if rows[i].CurrentAttemptID != nil {
			if a, ok := attempts[*rows[i].CurrentAttemptID]; ok {
				cp := a
				att = &cp
			}
		}
		v := rows[i].View(att)
		v.JobName = rows[i].JobName
		v.ServiceCode = rows[i].ServiceCode
		v.ServiceName = rows[i].ServiceName
		// 列表省略大体量字段
		v.Params = nil
		v.Result = nil
		if v.Attempt != nil {
			v.Attempt.Result = nil
		}
		out = append(out, v)
	}
	return out, total, nil
}

// ListByJob 任务维度执行列表。
func (s *Executions) ListByJob(ctx context.Context, serviceID, jobID, keyword, status, instanceID, createdFrom, createdTo string, page, limit int) ([]model.ExecutionView, int64, error) {
	job, err := s.store.GetJob(ctx, serviceID, jobID)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, 0, ErrNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	return s.List(ctx, keyword, job.ServiceID, job.ID, status, instanceID, createdFrom, createdTo, page, limit)
}

// ListLogs 拉取执行日志（after=sequence）。
func (s *Executions) ListLogs(ctx context.Context, serviceID, jobID, executionID, attemptID string, afterSeq uint64, limit int) ([]model.ExecutionLogView, error) {
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
	list, err := s.store.ListExecutionLogs(ctx, executionID, strings.TrimSpace(attemptID), afterSeq, limit)
	if err != nil {
		return nil, err
	}
	out := make([]model.ExecutionLogView, 0, len(list))
	for i := range list {
		out = append(out, list[i].View())
	}
	return out, nil
}

// DispatchQueued 扫 QUEUED executions，有容量则派发。
func (s *Executions) DispatchQueued(ctx context.Context, limit int) (int, error) {
	list, err := s.store.ListQueuedExecutions(ctx, limit)
	if err != nil {
		return 0, err
	}
	dispatched := 0
	for i := range list {
		e := &list[i]
		job, err := s.store.GetJobByID(ctx, e.JobID)
		if err != nil {
			continue
		}
		if job.Status != model.JobStatusActive || !job.Enabled {
			continue
		}
		inst, err := s.store.PickOnlineInstanceForJob(ctx, e.ServiceID, e.JobID, "")
		if errors.Is(err, mysql.ErrNotFound) {
			continue
		}
		if err != nil {
			return dispatched, err
		}
		// 再读一次，避免并发扫队重复派发。
		cur, err := s.store.GetExecution(ctx, e.ID)
		if err != nil || cur.Status != model.ExecStatusQueued {
			continue
		}
		if _, err := s.dispatch(ctx, cur, job, inst); err != nil {
			return dispatched, err
		}
		dispatched++
	}
	return dispatched, nil
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
	if job, err := s.store.GetJobByID(ctx, e.JobID); err == nil && job != nil {
		v.JobName = job.Name
	}
	if svc, err := s.store.GetService(ctx, e.ServiceID); err == nil && svc != nil {
		v.ServiceCode = svc.Code
		v.ServiceName = svc.Name
	}
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
		return nil, Conflictf("执行已结束（%s）", e.Status)
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
	reason := normalizeCancelReason(in.Reason)
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
	if errors.Is(err, errAttemptFinished) {
		return ackIfAttemptStatus(a, model.AttemptCancelled)
	}
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

// normalizeCancelReason 写入 status_reason（最长 128）；空则 ADMIN_CANCEL。
func normalizeCancelReason(raw string) string {
	r := strings.TrimSpace(raw)
	if r == "" {
		return "ADMIN_CANCEL"
	}
	runes := []rune(r)
	if len(runes) > 128 {
		return string(runes[:128])
	}
	return r
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
	stateRaw, stateVer := s.loadRuntimeState(ctx, job.ID)
	msg := protocol.TaskExecute{
		ExecutionID:    exec.ID,
		AttemptID:      a.ID,
		AttemptNumber:  int(attemptNo),
		LeaseToken:     token,
		LeaseExpiresAt: protocol.FormatTime(leaseExp),
		JobCode:        job.Code,
		JobName:        job.Name,
		Params:         params,
		State:          stateRaw,
		StateVersion:   stateVer,
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
	if errors.Is(err, errAttemptFinished) {
		return ackIfAttemptStatus(a, model.AttemptSucceeded)
	}
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
	if len(msg.NextState) > 0 && string(msg.NextState) != "null" {
		_ = s.persistRuntimeState(ctx, a.JobID, a.ExecutionID, msg.NextState)
	}
	if a.InstanceID != nil {
		_ = s.store.AdjustInstanceRunning(ctx, *a.InstanceID, -1)
	}
	return okAck(msg.AttemptID)
}

func (s *Executions) OnFailed(ctx context.Context, instanceID string, msg protocol.TaskFailed) *protocol.TaskResultAck {
	a, err := s.loadAndCheckLease(ctx, msg.AttemptID, msg.LeaseToken, instanceID)
	if errors.Is(err, errAttemptFinished) {
		return ackIfAttemptStatus(a, model.AttemptFailed, model.AttemptTimedOut)
	}
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

func normalizeLogLevel(level string) string {
	switch strings.ToUpper(strings.TrimSpace(level)) {
	case "DEBUG":
		return "DEBUG"
	case "WARN", "WARNING":
		return "WARN"
	case "ERROR", "FATAL", "PANIC":
		return "ERROR"
	default:
		return "INFO"
	}
}

// OnLog 校验租约、幂等落库 execution_logs，并累加 log_*_collected / log_*_dropped。
func (s *Executions) OnLog(ctx context.Context, instanceID string, msg protocol.TaskLog) *protocol.TaskResultAck {
	a, err := s.loadAndCheckLease(ctx, msg.AttemptID, msg.LeaseToken, instanceID)
	if err != nil {
		return rejectAck(msg.AttemptID, err)
	}
	now := time.Now().UTC()
	logs := make([]model.ExecutionLog, 0, len(msg.Entries))
	var maxSeq uint64
	var collectedInfo, collectedWarn, collectedError uint
	for _, ent := range msg.Entries {
		if ent.Seq <= 0 {
			continue
		}
		seq := uint64(ent.Seq)
		if seq <= a.LastLogSeq {
			continue // 幂等：已确认水位内的 seq 跳过
		}
		if seq > maxSeq {
			maxSeq = seq
		}
		loggedAt := now
		if ent.Ts != "" {
			if t, err := time.Parse(time.RFC3339Nano, ent.Ts); err == nil {
				loggedAt = t.UTC()
			} else if t, err := time.Parse(time.RFC3339, ent.Ts); err == nil {
				loggedAt = t.UTC()
			}
		}
		msgText := ent.Message
		truncated := false
		const maxMsg = 8 * 1024
		if len(msgText) > maxMsg {
			msgText = msgText[:maxMsg]
			truncated = true
		}
		var stream *string
		if st := strings.TrimSpace(ent.Stream); st != "" {
			stream = &st
		}
		var fields json.RawMessage
		if len(ent.Fields) > 0 && string(ent.Fields) != "null" {
			fields = ent.Fields
		}
		level := normalizeLogLevel(ent.Level)
		switch level {
		case "WARN":
			collectedWarn++
		case "ERROR":
			collectedError++
		default:
			collectedInfo++ // DEBUG/INFO
		}
		logs = append(logs, model.ExecutionLog{
			ExecutionID: a.ExecutionID,
			AttemptID:   a.ID,
			Sequence:    seq,
			Level:       level,
			Stream:      stream,
			Message:     msgText,
			Fields:      fields,
			Truncated:   truncated,
			LoggedAt:    loggedAt,
			CreatedAt:   now,
		})
	}
	if err := s.store.InsertExecutionLogs(ctx, logs); err != nil {
		return &protocol.TaskResultAck{
			Status: "REJECTED", AttemptID: msg.AttemptID, ErrorCode: "LOG_PERSIST_FAILED", Message: err.Error(),
		}
	}
	di, dw, de := msg.DroppedInfo, msg.DroppedWarn, msg.DroppedError
	if di < 0 {
		di = 0
	}
	if dw < 0 {
		dw = 0
	}
	if de < 0 {
		de = 0
	}
	// 兼容仅带 Dropped 总数的旧帧：无法拆分时记入 info
	if di == 0 && dw == 0 && de == 0 && msg.Dropped > 0 {
		di = msg.Dropped
	}
	// 整帧 entries 均为重复 seq 时不再累加 dropped，避免 soft 重发双计
	if len(logs) == 0 && len(msg.Entries) > 0 {
		di, dw, de = 0, 0, 0
	}
	acked := a.LastLogSeq
	if maxSeq > acked {
		acked = maxSeq
	}
	needStats := collectedInfo+collectedWarn+collectedError > 0 || di+dw+de > 0 || maxSeq > a.LastLogSeq
	if needStats {
		_ = s.store.IncrAttemptLogStats(ctx, a.ID, mysql.AttemptLogStatsDelta{
			InfoCollected: collectedInfo, WarnCollected: collectedWarn, ErrorCollected: collectedError,
			InfoDropped: uint(di), WarnDropped: uint(dw), ErrorDropped: uint(de),
			LastLogSeq: acked,
		})
	}
	return okAckWithSeq(msg.AttemptID, int64(acked))
}

func (s *Executions) OnCheckpoint(ctx context.Context, instanceID string, msg protocol.TaskCheckpoint) *protocol.TaskResultAck {
	a, err := s.loadAndCheckLease(ctx, msg.AttemptID, msg.LeaseToken, instanceID)
	if err != nil {
		return rejectAck(msg.AttemptID, err)
	}
	if err := s.persistRuntimeState(ctx, a.JobID, a.ExecutionID, msg.State); err != nil {
		return &protocol.TaskResultAck{
			Status: "REJECTED", AttemptID: msg.AttemptID, ErrorCode: "STATE_PERSIST_FAILED", Message: err.Error(),
		}
	}
	return okAck(msg.AttemptID)
}

// RuntimeStateKey 执行器读写的 job_states 默认键。
const RuntimeStateKey = "default"

func (s *Executions) loadRuntimeState(ctx context.Context, jobID string) (json.RawMessage, uint64) {
	st, err := s.store.GetJobStateByKey(ctx, jobID, RuntimeStateKey)
	if err != nil || st == nil {
		return json.RawMessage(`{}`), 0
	}
	raw, err := json.Marshal(st.Value)
	if err != nil || len(raw) == 0 {
		return json.RawMessage(`{}`), st.Version
	}
	return raw, st.Version
}

func (s *Executions) persistRuntimeState(ctx context.Context, jobID, executionID string, raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("invalid state json: %w", err)
	}
	if value == nil {
		value = map[string]any{}
	}
	_, err := s.store.UpsertJobStateByKey(ctx, jobID, RuntimeStateKey, value, "executor", executionID)
	return err
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

var errAttemptFinished = errors.New("attempt already finished")

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
		return a, errAttemptFinished
	}
	return a, nil
}

// SyncRunning 重连同步：续租仍在跑的 attempt，终态返回 GONE。
func (s *Executions) SyncRunning(ctx context.Context, instanceID string, msg protocol.TaskSyncRunning) *protocol.TaskSyncRunningAck {
	out := &protocol.TaskSyncRunningAck{Results: make([]protocol.TaskSyncRunningResult, 0, len(msg.Attempts))}
	now := time.Now().UTC()
	until := now.Add(s.leaseTTL)
	for _, item := range msg.Attempts {
		res := protocol.TaskSyncRunningResult{AttemptID: item.AttemptID}
		a, err := s.store.GetAttempt(ctx, strings.TrimSpace(item.AttemptID))
		if errors.Is(err, mysql.ErrNotFound) {
			res.Status = "GONE"
			res.Message = "attempt not found"
			out.Results = append(out.Results, res)
			continue
		}
		if err != nil {
			res.Status = "REJECTED"
			res.Message = err.Error()
			out.Results = append(out.Results, res)
			continue
		}
		switch a.Status {
		case model.AttemptSucceeded, model.AttemptFailed, model.AttemptCancelled, model.AttemptTimedOut, model.AttemptLost:
			res.Status = "GONE"
			res.Message = "attempt finished: " + a.Status
			out.Results = append(out.Results, res)
			continue
		}
		if a.LeaseToken == "" || a.LeaseToken != item.LeaseToken {
			res.Status = "REJECTED"
			res.Message = "invalid lease"
			out.Results = append(out.Results, res)
			continue
		}
		if a.InstanceID == nil || *a.InstanceID != instanceID {
			res.Status = "REJECTED"
			res.Message = "instance mismatch"
			out.Results = append(out.Results, res)
			continue
		}
		_ = s.store.UpdateAttemptFields(ctx, a.ID, map[string]any{
			"lease_expires_at":  until,
			"last_heartbeat_at": now,
		})
		res.Status = "OK"
		res.LeaseExpiresAt = protocol.FormatTime(until)
		out.Results = append(out.Results, res)
	}
	s.reconcileInstanceRunning(ctx, instanceID)
	return out
}

// RenewLeasesForInstance 心跳续租并校正 running_count。
func (s *Executions) RenewLeasesForInstance(ctx context.Context, instanceID string) {
	until := time.Now().UTC().Add(s.leaseTTL)
	_, _ = s.store.RenewAttemptLeasesForInstance(ctx, instanceID, until)
	s.reconcileInstanceRunning(ctx, instanceID)
}

func (s *Executions) reconcileInstanceRunning(ctx context.Context, instanceID string) {
	n, err := s.store.CountActiveAttemptsForInstance(ctx, instanceID)
	if err != nil {
		return
	}
	_ = s.store.SetInstanceRunningCount(ctx, instanceID, uint(n))
}

func okAck(attemptID string) *protocol.TaskResultAck {
	return &protocol.TaskResultAck{Status: "OK", AttemptID: attemptID}
}

func okAckWithSeq(attemptID string, ackedThroughSeq int64) *protocol.TaskResultAck {
	seq := ackedThroughSeq
	return &protocol.TaskResultAck{Status: "OK", AttemptID: attemptID, AckedThroughSeq: &seq}
}

func rejectAck(attemptID string, err error) *protocol.TaskResultAck {
	return &protocol.TaskResultAck{
		Status: "REJECTED", AttemptID: attemptID, ErrorCode: "STALE_OR_INVALID", Message: err.Error(),
	}
}

func ackIfAttemptStatus(a *model.Attempt, want ...string) *protocol.TaskResultAck {
	if a == nil {
		return rejectAck("", fmt.Errorf("attempt missing"))
	}
	for _, w := range want {
		if a.Status == w {
			return okAck(a.ID)
		}
	}
	return &protocol.TaskResultAck{
		Status: "REJECTED", AttemptID: a.ID, ErrorCode: "ALREADY_FINISHED",
		Message: "attempt status=" + a.Status,
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
