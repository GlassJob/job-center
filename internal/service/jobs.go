package service

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/GlassJob/job-center/internal/model"
	"github.com/GlassJob/job-center/internal/pkg/idgen"
	"github.com/GlassJob/job-center/internal/repository/mysql"
)

var jobCodeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,127}$`)

type Jobs struct {
	store *mysql.Store
}

func NewJobs(store *mysql.Store) *Jobs {
	return &Jobs{store: store}
}

type CreateJobInput struct {
	Code                  string `json:"code"`
	Name                  string `json:"name"`
	Description           string `json:"description"`
	Enabled               *bool  `json:"enabled"`
	DefaultTimeoutSeconds *uint  `json:"default_timeout_seconds"`
}

type PatchJobInput struct {
	Name                  *string `json:"name"`
	Description           *string `json:"description"`
	Enabled               *bool   `json:"enabled"`
	IdempotencyVerified   *bool   `json:"idempotency_verified"`
	DefaultTimeoutSeconds *uint   `json:"default_timeout_seconds"`
}

func (s *Jobs) ensureService(ctx context.Context, serviceID string) error {
	svc, err := s.store.GetService(ctx, serviceID)
	if errors.Is(err, mysql.ErrNotFound) {
		return NotFoundf("服务")
	}
	if err != nil {
		return err
	}
	if svc.Status == model.ServiceStatusDisabled {
		return Invalidf("服务已禁用")
	}
	return nil
}

func (s *Jobs) ensureServiceExists(ctx context.Context, serviceID string) error {
	_, err := s.store.GetService(ctx, serviceID)
	if errors.Is(err, mysql.ErrNotFound) {
		return NotFoundf("服务")
	}
	return err
}

func (s *Jobs) List(ctx context.Context, serviceID, keyword, status, enabledRaw string, page, limit int) ([]model.JobView, int64, error) {
	if sid := strings.TrimSpace(serviceID); sid != "" {
		if err := s.ensureServiceExists(ctx, sid); err != nil {
			return nil, 0, err
		}
	}
	if status != "" && status != model.JobStatusActive && status != model.JobStatusOrphaned && status != model.JobStatusDisabled {
		return nil, 0, Invalidf("状态")
	}
	var enabled *bool
	if strings.TrimSpace(enabledRaw) != "" {
		v, err := strconv.ParseBool(strings.TrimSpace(enabledRaw))
		if err != nil {
			return nil, 0, Invalidf("enabled 参数")
		}
		enabled = &v
	}
	return s.listJobs(ctx, serviceID, keyword, status, enabled, page, limit, false)
}

// ListGlobal 全局任务列表，附带服务字段、调度摘要与最近执行。
func (s *Jobs) ListGlobal(ctx context.Context, serviceID, keyword, status, enabledRaw string, page, limit int) ([]model.JobView, int64, error) {
	if sid := strings.TrimSpace(serviceID); sid != "" {
		if err := s.ensureServiceExists(ctx, sid); err != nil {
			return nil, 0, err
		}
	}
	if status != "" && status != model.JobStatusActive && status != model.JobStatusOrphaned && status != model.JobStatusDisabled {
		return nil, 0, Invalidf("状态")
	}
	var enabled *bool
	if strings.TrimSpace(enabledRaw) != "" {
		v, err := strconv.ParseBool(strings.TrimSpace(enabledRaw))
		if err != nil {
			return nil, 0, Invalidf("enabled 参数")
		}
		enabled = &v
	}
	return s.listJobs(ctx, serviceID, keyword, status, enabled, page, limit, true)
}

func (s *Jobs) listJobs(ctx context.Context, serviceID, keyword, status string, enabled *bool, page, limit int, enrich bool) ([]model.JobView, int64, error) {
	list, total, err := s.store.ListJobs(ctx, serviceID, keyword, status, enabled, page, limit)
	if err != nil {
		return nil, 0, err
	}
	out := make([]model.JobView, 0, len(list))
	jobIDs := make([]string, 0, len(list))
	for i := range list {
		v := list[i].View()
		v.ServiceCode = list[i].ServiceCode
		v.ServiceName = list[i].ServiceName
		out = append(out, v)
		jobIDs = append(jobIDs, list[i].ID)
	}
	if !enrich || len(jobIDs) == 0 {
		return out, total, nil
	}

	schedules, err := s.store.ListSchedulesByJobIDs(ctx, jobIDs)
	if err != nil {
		return nil, 0, err
	}
	byJob := map[string][]model.ScheduleSummary{}
	for i := range schedules {
		sch := schedules[i]
		byJob[sch.JobID] = append(byJob[sch.JobID], model.ScheduleSummary{
			ID: sch.ID, Code: sch.Code, Name: sch.Name, Enabled: sch.Enabled,
			CronExpression: sch.CronExpression, Timezone: sch.Timezone,
			NextFireAt: sch.NextFireAt, LastFireAt: sch.LastFireAt,
		})
	}
	latest, err := s.store.LatestExecutionByJobIDs(ctx, jobIDs)
	if err != nil {
		return nil, 0, err
	}
	for i := range out {
		if sum, ok := byJob[out[i].ID]; ok {
			out[i].Schedules = sum
		} else {
			out[i].Schedules = []model.ScheduleSummary{}
		}
		if ex, ok := latest[out[i].ID]; ok {
			id := ex.ID
			out[i].LastExecutionID = &id
			out[i].LastExecutionStatus = ex.Status
			at := ex.CreatedAt
			if ex.FinishedAt != nil {
				at = *ex.FinishedAt
			} else if ex.StartedAt != nil {
				at = *ex.StartedAt
			}
			out[i].LastExecutionAt = &at
		}
	}
	return out, total, nil
}

func (s *Jobs) Get(ctx context.Context, serviceID, id string) (*model.JobView, error) {
	j, err := s.store.GetJob(ctx, serviceID, id)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	v := j.View()
	if svc, err := s.store.GetService(ctx, j.ServiceID); err == nil {
		v.ServiceCode = svc.Code
		v.ServiceName = svc.Name
	} else if err != nil && !errors.Is(err, mysql.ErrNotFound) {
		return nil, err
	}
	schedules, err := s.store.ListSchedulesByJobIDs(ctx, []string{j.ID})
	if err != nil {
		return nil, err
	}
	v.Schedules = make([]model.ScheduleSummary, 0, len(schedules))
	for i := range schedules {
		sch := schedules[i]
		v.Schedules = append(v.Schedules, model.ScheduleSummary{
			ID: sch.ID, Code: sch.Code, Name: sch.Name, Enabled: sch.Enabled,
			CronExpression: sch.CronExpression, Timezone: sch.Timezone,
			NextFireAt: sch.NextFireAt, LastFireAt: sch.LastFireAt,
		})
	}
	rows, _, err := s.store.ListExecutions(ctx, mysql.ListExecutionsFilter{
		ServiceID: serviceID, JobID: id, Page: 1, Limit: 5,
	})
	if err != nil {
		return nil, err
	}
	v.RecentExecutions = make([]model.ExecutionView, 0, len(rows))
	for i := range rows {
		ev := rows[i].View(nil)
		ev.JobName = rows[i].JobName
		ev.ServiceCode = rows[i].ServiceCode
		ev.ServiceName = rows[i].ServiceName
		ev.Params = nil
		ev.Result = nil
		v.RecentExecutions = append(v.RecentExecutions, ev)
	}
	return &v, nil
}

func (s *Jobs) Create(ctx context.Context, serviceID string, in CreateJobInput) (*model.JobView, error) {
	if err := s.ensureService(ctx, serviceID); err != nil {
		return nil, err
	}
	code := strings.TrimSpace(in.Code)
	name := strings.TrimSpace(in.Name)
	if !jobCodeRe.MatchString(code) {
		return nil, Invalidf("编码")
	}
	if name == "" || len(name) > 128 {
		return nil, Invalidf("名称")
	}
	desc := strings.TrimSpace(in.Description)
	if len(desc) > 1000 {
		return nil, Invalidf("描述")
	}
	timeout := uint(300)
	if in.DefaultTimeoutSeconds != nil {
		timeout = *in.DefaultTimeoutSeconds
		if timeout < 1 || timeout > 604800 {
			return nil, Invalidf("默认超时秒数")
		}
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	taken, err := s.store.ExistsJobCode(ctx, serviceID, code, "")
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, Conflictf("编码已存在")
	}
	j := &model.Job{
		ID:                    idgen.New(),
		ServiceID:             serviceID,
		Code:                  code,
		Name:                  name,
		Description:           desc,
		Enabled:               enabled,
		IdempotencyMode:       model.IdempotencyModeUnknown,
		SupportsTimeout:       true,
		DefaultTimeoutSeconds: timeout,
		Capabilities:          map[string]any{},
		Status:                model.JobStatusActive,
	}
	if err := s.store.CreateJob(ctx, j); err != nil {
		if errors.Is(err, mysql.ErrConflict) {
			return nil, ErrConflict
		}
		return nil, err
	}
	got, err := s.store.GetJob(ctx, serviceID, j.ID)
	if err != nil {
		return nil, err
	}
	v := got.View()
	return &v, nil
}

func (s *Jobs) Patch(ctx context.Context, serviceID, id string, in PatchJobInput) (*model.JobView, error) {
	p := mysql.JobPatch{BumpMetadataVersion: true}
	changed := false
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len(n) > 128 {
			return nil, Invalidf("名称")
		}
		p.Name = &n
		changed = true
	}
	if in.Description != nil {
		d := strings.TrimSpace(*in.Description)
		if len(d) > 1000 {
			return nil, Invalidf("描述")
		}
		p.Description = &d
		changed = true
	}
	if in.Enabled != nil {
		p.Enabled = in.Enabled
		changed = true
	}
	if in.IdempotencyVerified != nil {
		p.IdempotencyVerified = in.IdempotencyVerified
		changed = true
	}
	if in.DefaultTimeoutSeconds != nil {
		t := *in.DefaultTimeoutSeconds
		if t < 1 || t > 604800 {
			return nil, Invalidf("默认超时秒数")
		}
		p.DefaultTimeoutSeconds = &t
		changed = true
	}
	if !changed {
		p.BumpMetadataVersion = false
	}
	j, err := s.store.PatchJob(ctx, serviceID, id, p)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	v := j.View()
	return &v, nil
}

type DeleteJobResult struct {
	Hard bool           `json:"hard"`
	Job  *model.JobView `json:"job,omitempty"`
}

func (s *Jobs) Delete(ctx context.Context, serviceID, id string, hard bool) (*DeleteJobResult, error) {
	cur, err := s.store.GetJob(ctx, serviceID, id)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !hard {
		if cur.Status == model.JobStatusDisabled {
			v := cur.View()
			return &DeleteJobResult{Hard: false, Job: &v}, nil
		}
		j, err := s.store.SoftDisableJob(ctx, serviceID, id)
		if errors.Is(err, mysql.ErrNotFound) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		v := j.View()
		return &DeleteJobResult{Hard: false, Job: &v}, nil
	}
	deps, err := s.store.CountJobDependents(ctx, id)
	if err != nil {
		return nil, err
	}
	if deps.Total() > 0 {
		return nil, Conflictf("任务仍有依赖资源（versions=%d schedules=%d states=%d instance_jobs=%d executions=%d）", deps.Versions, deps.Schedules, deps.States, deps.InstanceJobs, deps.Executions)
	}
	if err := s.store.DeleteJob(ctx, serviceID, id); err != nil {
		if errors.Is(err, mysql.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &DeleteJobResult{Hard: true}, nil
}
