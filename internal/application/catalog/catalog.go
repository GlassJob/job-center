// Package catalog 管理端的服务/凭据/实例/任务/调度配置用例（P1 最小集）。
// 写操作全部记录 job_operation_audit。
package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/GlassJob/job-center/internal/application/apperr"
	"github.com/GlassJob/job-center/internal/domain/schedule"
	"github.com/GlassJob/job-center/internal/domain/task"
	"github.com/GlassJob/job-center/internal/infrastructure/security"
	repo "github.com/GlassJob/job-center/internal/repository/mysql"
)

// Actor 已通过签名校验的调用方（客户端 + 操作员）。
type Actor struct {
	Client   string
	Operator security.Operator
	TraceID  string
}

type Service struct {
	store      *repo.Store
	pepper     string
	defaultTZ  string
	defaultLoc *time.Location
}

func New(store *repo.Store, pepper string, defaultLoc *time.Location, defaultTZ string) *Service {
	return &Service{store: store, pepper: pepper, defaultLoc: defaultLoc, defaultTZ: defaultTZ}
}

func invalid(code, msg string) *apperr.Error { return apperr.New(apperr.KindInvalid, code, msg) }

func (s *Service) audit(ctx context.Context, a Actor, action, perm, rtype, rid string, before, after any, err error) {
	rec := repo.Audit{OperatorSource: a.Client, OperatorID: a.Operator.ID, OperatorName: a.Operator.Name, Action: action, Permission: perm,
		ResourceType: rtype, ResourceID: rid, RequestID: a.Operator.RequestID, TraceID: a.TraceID, ClientIP: a.Operator.ClientIP,
		Before: before, After: after, Result: "SUCCESS"}
	if err != nil {
		rec.Result, rec.ErrorMessage = "FAILED", truncate(err.Error(), 1000)
	}
	_ = s.store.Q().InsertAudit(ctx, rec)
}

// AuditDenied 记录权限拒绝（由 HTTP 层调用）。
func (s *Service) AuditDenied(ctx context.Context, a Actor, action, perm string) {
	_ = s.store.Q().InsertAudit(ctx, repo.Audit{OperatorSource: a.Client, OperatorID: a.Operator.ID, OperatorName: a.Operator.Name, Action: action,
		Permission: perm, ResourceType: "api", RequestID: a.Operator.RequestID, TraceID: a.TraceID, ClientIP: a.Operator.ClientIP, Result: "DENIED"})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

type CreateServiceInput struct {
	ServiceCode string `json:"service_code"`
	ServiceName string `json:"service_name"`
	Description string `json:"description"`
}

func (s *Service) CreateService(ctx context.Context, a Actor, in CreateServiceInput) (*repo.Service, error) {
	if !task.ServiceCodeRe.MatchString(in.ServiceCode) {
		return nil, invalid("PARAMS_INVALID", "service_code must match ^[a-z0-9][a-z0-9._-]{1,63}$")
	}
	if in.ServiceName == "" || utf8.RuneCountInString(in.ServiceName) > 128 || utf8.RuneCountInString(in.Description) > 1000 {
		return nil, invalid("PARAMS_INVALID", "service_name required (≤128), description ≤1000")
	}
	id, err := s.store.Q().CreateService(ctx, in.ServiceCode, in.ServiceName, in.Description)
	if errors.Is(err, repo.ErrDuplicate) {
		e := apperr.New(apperr.KindConflict, "ALREADY_EXISTS", "service_code already exists")
		s.audit(ctx, a, "service.create", "job.credential.manage", "service", in.ServiceCode, nil, in, e)
		return nil, e
	}
	if err != nil {
		return nil, apperr.Storage(err)
	}
	svc, err := s.store.Q().GetService(ctx, id)
	if err != nil {
		return nil, apperr.Storage(err)
	}
	s.audit(ctx, a, "service.create", "job.credential.manage", "service", strconv.FormatInt(id, 10), nil, svc, nil)
	return svc, nil
}

func (s *Service) GetService(ctx context.Context, id int64) (*repo.Service, error) {
	svc, err := s.store.Q().GetService(ctx, id)
	if errors.Is(err, repo.ErrNotFound) {
		return nil, apperr.New(apperr.KindNotFound, "NOT_FOUND", "service not found")
	}
	if err != nil {
		return nil, apperr.Storage(err)
	}
	return svc, nil
}

func (s *Service) ListServices(ctx context.Context, keyword string, page, limit int) ([]*repo.Service, int, error) {
	l, n, err := s.store.Q().ListServices(ctx, keyword, page, limit)
	if err != nil {
		return nil, 0, apperr.Storage(err)
	}
	return l, n, nil
}

type CreateCredentialInput struct {
	ExpiresAt        *time.Time `json:"expires_at"`
	AllowedTaskCodes []string   `json:"allowed_task_codes"`
	RotateFromKeyID  string     `json:"rotate_from_key_id"`
}

type CreatedCredential struct {
	repo.Credential
	Token string `json:"token"`
}

// CreateCredential 生成凭据；明文 token 只在返回值中出现一次，库中只存 HMAC。
func (s *Service) CreateCredential(ctx context.Context, a Actor, serviceID int64, in CreateCredentialInput) (*CreatedCredential, error) {
	if in.RotateFromKeyID != "" {
		return nil, invalid("NOT_IMPLEMENTED", "rotate_from_key_id is implemented in P7")
	}
	if _, err := s.GetService(ctx, serviceID); err != nil {
		return nil, err
	}
	for _, c := range in.AllowedTaskCodes {
		base := c
		if len(c) > 0 && c[len(c)-1] == '*' {
			base = c[:len(c)-1] + "x"
		}
		if !task.TaskCodeRe.MatchString(base) {
			return nil, invalid("PARAMS_INVALID", "invalid allowed_task_codes entry: "+c)
		}
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(time.Now()) {
		return nil, invalid("PARAMS_INVALID", "expires_at must be in the future")
	}
	tok, keyID, secret, err := security.NewCredential()
	if err != nil {
		return nil, apperr.Wrap(apperr.KindInternal, "INTERNAL_ERROR", "generate credential", err)
	}
	c := repo.Credential{ServiceID: serviceID, KeyID: keyID, SecretHash: security.HashSecret(s.pepper, secret), SecretHint: security.SecretHint(secret),
		Status: "ACTIVE", AllowedTaskCodes: in.AllowedTaskCodes, ExpiresAt: in.ExpiresAt, CreatedBy: a.Client + ":" + a.Operator.ID, CreatedAt: time.Now().UTC()}
	if c.ExpiresAt != nil {
		t := c.ExpiresAt.UTC()
		c.ExpiresAt = &t
	}
	if err := s.store.Q().CreateCredential(ctx, &c); err != nil {
		return nil, apperr.Storage(err)
	}
	s.audit(ctx, a, "credential.create", "job.credential.manage", "credential", keyID, nil,
		map[string]any{"service_id": serviceID, "key_id": keyID, "allowed_task_codes": in.AllowedTaskCodes, "expires_at": c.ExpiresAt}, nil)
	return &CreatedCredential{Credential: c, Token: tok}, nil
}

func (s *Service) ListCredentials(ctx context.Context, serviceID int64) ([]*repo.Credential, error) {
	if _, err := s.GetService(ctx, serviceID); err != nil {
		return nil, err
	}
	l, err := s.store.Q().ListCredentials(ctx, serviceID)
	if err != nil {
		return nil, apperr.Storage(err)
	}
	return l, nil
}

type UpdateServiceInput struct {
	ServiceName *string `json:"service_name"`
	Description *string `json:"description"`
	Status      *string `json:"status"`
}

func (s *Service) UpdateService(ctx context.Context, a Actor, id int64, in UpdateServiceInput) (*repo.Service, error) {
	before, err := s.GetService(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.Status != nil && *in.Status != "ACTIVE" && *in.Status != "DISABLED" {
		return nil, invalid("PARAMS_INVALID", "status must be ACTIVE or DISABLED")
	}
	if in.ServiceName != nil {
		n := strings.TrimSpace(*in.ServiceName)
		if n == "" || utf8.RuneCountInString(n) > 128 {
			return nil, invalid("PARAMS_INVALID", "service_name required (≤128)")
		}
		in.ServiceName = &n
	}
	if in.Description != nil && utf8.RuneCountInString(*in.Description) > 1000 {
		return nil, invalid("PARAMS_INVALID", "description ≤1000")
	}
	if err := s.store.Q().UpdateService(ctx, id, in.ServiceName, in.Description, in.Status); err != nil {
		return nil, apperr.Storage(err)
	}
	after, err := s.GetService(ctx, id)
	if err != nil {
		return nil, err
	}
	s.audit(ctx, a, "service.update", "job.credential.manage", "service", strconv.FormatInt(id, 10), before, after, nil)
	return after, nil
}

func (s *Service) RevokeCredential(ctx context.Context, a Actor, keyID string) error {
	cur, err := s.store.Q().GetCredentialByKeyID(ctx, keyID)
	if errors.Is(err, repo.ErrNotFound) {
		return apperr.New(apperr.KindNotFound, "NOT_FOUND", "credential not found")
	}
	if err != nil {
		return apperr.Storage(err)
	}
	n, err := s.store.Q().RevokeCredential(ctx, keyID)
	if err != nil {
		return apperr.Storage(err)
	}
	if n == 0 {
		return apperr.New(apperr.KindConflict, "ALREADY_REVOKED", "credential already revoked")
	}
	s.audit(ctx, a, "credential.revoke", "job.credential.manage", "credential", keyID, cur, map[string]any{"status": "REVOKED"}, nil)
	return nil
}

func (s *Service) ListInstances(ctx context.Context, f repo.InstanceFilter) ([]*repo.Instance, int, error) {
	l, n, err := s.store.Q().ListInstances(ctx, f)
	if err != nil {
		return nil, 0, apperr.Storage(err)
	}
	return l, n, nil
}

func (s *Service) fillTZ(sc *repo.Schedule) {
	if sc == nil {
		return
	}
	if sc.Timezone != nil && *sc.Timezone != "" {
		sc.EffectiveTimezone = *sc.Timezone
	} else {
		sc.EffectiveTimezone = s.defaultTZ
	}
}

func (s *Service) ListTasks(ctx context.Context, f repo.TaskFilter) ([]*repo.Task, int, error) {
	l, n, err := s.store.Q().ListTasks(ctx, f)
	if err != nil {
		return nil, 0, apperr.Storage(err)
	}
	for _, t := range l {
		s.fillTZ(t.Schedule)
	}
	return l, n, nil
}

func (s *Service) GetTask(ctx context.Context, id int64) (*repo.TaskDetail, error) {
	t, err := s.store.Q().GetTask(ctx, id)
	if errors.Is(err, repo.ErrNotFound) {
		return nil, apperr.New(apperr.KindNotFound, "NOT_FOUND", "task not found")
	}
	if err != nil {
		return nil, apperr.Storage(err)
	}
	s.fillTZ(t.Schedule)
	return t, nil
}

// ScheduleUpdate 可写字段；缺省字段保持原值。timezone 显式传 null 表示使用服务器默认时区。
type ScheduleUpdate struct {
	ScheduleType            *string          `json:"schedule_type"`
	CronExpression          json.RawMessage  `json:"cron_expression"`
	Timezone                json.RawMessage  `json:"timezone"`
	DefaultParams           json.RawMessage  `json:"default_params"`
	Enabled                 *bool            `json:"enabled"`
	ManualEnabled           *bool            `json:"manual_enabled"`
	TimeoutSeconds          *int             `json:"timeout_seconds"`
	ConcurrencyPolicy       *string          `json:"concurrency_policy"`
	ConcurrencyScope        *string          `json:"concurrency_scope"`
	MaxConcurrency          *int             `json:"max_concurrency"`
	QueueMax                *int             `json:"queue_max"`
	RetryPolicy             *json.RawMessage `json:"retry_policy"`
	MisfirePolicy           *string          `json:"misfire_policy"`
	MisfireThresholdSeconds *int             `json:"misfire_threshold_seconds"`
	CatchUpMax              *int             `json:"catch_up_max"`
	DispatchStrategy        *string          `json:"dispatch_strategy"`
}

// nullableString 解析 JSON 字段：缺省 → (nil,false)；null → (nil,true)；"x" → (&x,true)。
func nullableString(raw json.RawMessage) (*string, bool, error) {
	if raw == nil {
		return nil, false, nil
	}
	if string(raw) == "null" {
		return nil, true, nil
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, true, err
	}
	return &v, true, nil
}

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// UpdateSchedule 乐观锁修改调度配置；成功后 config_source=ADMIN，此后注册永不覆盖。
func (s *Service) UpdateSchedule(ctx context.Context, a Actor, taskID, expectedVersion int64, in ScheduleUpdate) (*repo.Schedule, error) {
	before, err := s.store.Q().GetSchedule(ctx, taskID)
	if errors.Is(err, repo.ErrNotFound) {
		return nil, apperr.New(apperr.KindNotFound, "NOT_FOUND", "task not found")
	}
	if err != nil {
		return nil, apperr.Storage(err)
	}
	n := *before
	if in.ScheduleType != nil {
		n.ScheduleType = *in.ScheduleType
	}
	if v, set, err := nullableString(in.CronExpression); err != nil {
		return nil, invalid("PARAMS_INVALID", "cron_expression must be string or null")
	} else if set {
		n.CronExpression = v
	}
	if v, set, err := nullableString(in.Timezone); err != nil {
		return nil, invalid("PARAMS_INVALID", "timezone must be string or null")
	} else if set {
		if v != nil && *v == "" {
			v = nil
		}
		n.Timezone = v
	}
	if in.DefaultParams != nil {
		t := bytes.TrimSpace(in.DefaultParams)
		if bytes.Equal(t, []byte("null")) {
			n.DefaultParams = nil
		} else {
			clean, err := task.ValidateJSONObject(t, task.MaxParamsBytes)
			if err != nil {
				return nil, invalid("PARAMS_INVALID", "default_params: "+err.Error())
			}
			n.DefaultParams = clean
		}
	}
	setB := func(dst *bool, v *bool) {
		if v != nil {
			*dst = *v
		}
	}
	setI := func(dst *int, v *int) {
		if v != nil {
			*dst = *v
		}
	}
	setS := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	setB(&n.Enabled, in.Enabled)
	setB(&n.ManualEnabled, in.ManualEnabled)
	setI(&n.TimeoutSeconds, in.TimeoutSeconds)
	setS(&n.ConcurrencyPolicy, in.ConcurrencyPolicy)
	setS(&n.ConcurrencyScope, in.ConcurrencyScope)
	setI(&n.MaxConcurrency, in.MaxConcurrency)
	setI(&n.QueueMax, in.QueueMax)
	setS(&n.MisfirePolicy, in.MisfirePolicy)
	setI(&n.MisfireThresholdSeconds, in.MisfireThresholdSeconds)
	setI(&n.CatchUpMax, in.CatchUpMax)
	setS(&n.DispatchStrategy, in.DispatchStrategy)
	if in.RetryPolicy != nil {
		n.RetryPolicy = *in.RetryPolicy
	}

	// 校验
	switch {
	case !oneOf(n.ScheduleType, "CRON", "NONE"):
		return nil, invalid("PARAMS_INVALID", "schedule_type must be CRON or NONE")
	case !oneOf(n.ConcurrencyPolicy, "ALLOW", "SKIP", "QUEUE"):
		return nil, invalid("PARAMS_INVALID", "concurrency_policy must be ALLOW|SKIP|QUEUE")
	case !oneOf(n.ConcurrencyScope, "TASK", "SERVICE", "IDEMPOTENCY_KEY"):
		return nil, invalid("PARAMS_INVALID", "concurrency_scope must be TASK|SERVICE|IDEMPOTENCY_KEY")
	case !oneOf(n.MisfirePolicy, "SKIP", "FIRE_ONCE", "CATCH_UP"):
		return nil, invalid("PARAMS_INVALID", "misfire_policy must be SKIP|FIRE_ONCE|CATCH_UP")
	case !oneOf(n.DispatchStrategy, "LEAST_LOAD", "ROUND_ROBIN", "RANDOM"):
		return nil, invalid("PARAMS_INVALID", "dispatch_strategy must be LEAST_LOAD|ROUND_ROBIN|RANDOM")
	case n.TimeoutSeconds < 1 || n.TimeoutSeconds > 604800:
		return nil, invalid("PARAMS_INVALID", "timeout_seconds must be 1..604800")
	case n.MaxConcurrency < 1 || n.QueueMax < 0 || n.MisfireThresholdSeconds < 0:
		return nil, invalid("PARAMS_INVALID", "max_concurrency ≥1, queue_max ≥0, misfire_threshold_seconds ≥0")
	case n.CatchUpMax < 1 || n.CatchUpMax > 100:
		return nil, invalid("PARAMS_INVALID", "catch_up_max must be 1..100")
	}
	var rp map[string]any
	if err := json.Unmarshal(n.RetryPolicy, &rp); err != nil || rp == nil {
		return nil, invalid("PARAMS_INVALID", "retry_policy must be a JSON object")
	}
	if err := schedule.ValidateTimezone(n.Timezone); err != nil {
		return nil, apperr.New(apperr.KindInvalid, "TIMEZONE_INVALID", err.Error())
	}
	loc := s.defaultLoc
	if n.Timezone != nil {
		loc, _ = time.LoadLocation(*n.Timezone)
	}
	n.NextFireAt = nil
	if n.ScheduleType == "CRON" {
		if n.CronExpression == nil {
			return nil, apperr.New(apperr.KindInvalid, "CRON_INVALID", "cron_expression required when schedule_type=CRON")
		}
		sched, err := task.ValidateCron(*n.CronExpression)
		if err != nil {
			return nil, apperr.New(apperr.KindInvalid, "CRON_INVALID", err.Error())
		}
		if n.Enabled {
			next := sched.Next(time.Now().In(loc)).UTC()
			n.NextFireAt = &next
		}
	} else if n.Enabled {
		return nil, invalid("PARAMS_INVALID", "enabled requires schedule_type=CRON")
	}

	if detail, err := s.store.Q().GetTask(ctx, taskID); err == nil && detail != nil && len(detail.InputSchema) > 0 {
		if len(n.DefaultParams) > 0 {
			if errs, verr := task.ValidateParamsAgainstSchema(detail.InputSchema, n.DefaultParams); verr != nil {
				return nil, &apperr.Error{Kind: apperr.KindInvalid, Code: "PARAMS_SCHEMA_INVALID", Message: verr.Error(), Details: map[string]any{"errors": errs}}
			}
		} else if n.ScheduleType == "CRON" {
			if errs, verr := task.ValidateParamsAgainstSchema(detail.InputSchema, []byte(`{}`)); verr != nil {
				return nil, &apperr.Error{Kind: apperr.KindInvalid, Code: "PARAMS_SCHEMA_INVALID", Message: verr.Error(), Details: map[string]any{"errors": errs}}
			}
		}
	}
	rows, err := s.store.Q().UpdateScheduleCAS(ctx, expectedVersion, &n, a.Client+":"+a.Operator.ID)
	if err != nil {
		return nil, apperr.Storage(err)
	}
	rid := strconv.FormatInt(taskID, 10)
	if rows == 0 {
		e := &apperr.Error{Kind: apperr.KindConflict, Code: "VERSION_CONFLICT", Message: "schedule was modified by someone else",
			Details: map[string]any{"current_version": before.Version}}
		s.audit(ctx, a, "schedule.update", "job.schedule.update", "task", rid, before, in, e)
		return nil, e
	}
	after, err := s.store.Q().GetSchedule(ctx, taskID)
	if err != nil {
		return nil, apperr.Storage(err)
	}
	s.fillTZ(after)
	s.audit(ctx, a, "schedule.update", "job.schedule.update", "task", rid, before, after, nil)
	return after, nil
}
