package service

import (
	"context"
	"errors"
	"fmt"
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
		return fmt.Errorf("%w: service", ErrNotFound)
	}
	if err != nil {
		return err
	}
	if svc.Status == model.ServiceStatusDisabled {
		return fmt.Errorf("%w: service disabled", ErrInvalidInput)
	}
	return nil
}

func (s *Jobs) ensureServiceExists(ctx context.Context, serviceID string) error {
	_, err := s.store.GetService(ctx, serviceID)
	if errors.Is(err, mysql.ErrNotFound) {
		return fmt.Errorf("%w: service", ErrNotFound)
	}
	return err
}

func (s *Jobs) List(ctx context.Context, serviceID, keyword, status, enabledRaw string, page, limit int) ([]model.JobView, int64, error) {
	if err := s.ensureServiceExists(ctx, serviceID); err != nil {
		return nil, 0, err
	}
	if status != "" && status != model.JobStatusActive && status != model.JobStatusOrphaned && status != model.JobStatusDisabled {
		return nil, 0, fmt.Errorf("%w: status", ErrInvalidInput)
	}
	var enabled *bool
	if strings.TrimSpace(enabledRaw) != "" {
		v, err := strconv.ParseBool(strings.TrimSpace(enabledRaw))
		if err != nil {
			return nil, 0, fmt.Errorf("%w: enabled", ErrInvalidInput)
		}
		enabled = &v
	}
	list, total, err := s.store.ListJobs(ctx, serviceID, keyword, status, enabled, page, limit)
	if err != nil {
		return nil, 0, err
	}
	out := make([]model.JobView, 0, len(list))
	for i := range list {
		out = append(out, list[i].View())
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
	return &v, nil
}

func (s *Jobs) Create(ctx context.Context, serviceID string, in CreateJobInput) (*model.JobView, error) {
	if err := s.ensureService(ctx, serviceID); err != nil {
		return nil, err
	}
	code := strings.TrimSpace(in.Code)
	name := strings.TrimSpace(in.Name)
	if !jobCodeRe.MatchString(code) {
		return nil, fmt.Errorf("%w: code", ErrInvalidInput)
	}
	if name == "" || len(name) > 128 {
		return nil, fmt.Errorf("%w: name", ErrInvalidInput)
	}
	desc := strings.TrimSpace(in.Description)
	if len(desc) > 1000 {
		return nil, fmt.Errorf("%w: description", ErrInvalidInput)
	}
	timeout := uint(300)
	if in.DefaultTimeoutSeconds != nil {
		timeout = *in.DefaultTimeoutSeconds
		if timeout < 1 || timeout > 604800 {
			return nil, fmt.Errorf("%w: default_timeout_seconds", ErrInvalidInput)
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
		return nil, fmt.Errorf("%w: code already exists", ErrConflict)
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
			return nil, fmt.Errorf("%w: name", ErrInvalidInput)
		}
		p.Name = &n
		changed = true
	}
	if in.Description != nil {
		d := strings.TrimSpace(*in.Description)
		if len(d) > 1000 {
			return nil, fmt.Errorf("%w: description", ErrInvalidInput)
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
			return nil, fmt.Errorf("%w: default_timeout_seconds", ErrInvalidInput)
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
		return nil, fmt.Errorf("%w: job still has dependents (versions=%d schedules=%d states=%d instance_jobs=%d executions=%d)",
			ErrConflict, deps.Versions, deps.Schedules, deps.States, deps.InstanceJobs, deps.Executions)
	}
	if err := s.store.DeleteJob(ctx, serviceID, id); err != nil {
		if errors.Is(err, mysql.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &DeleteJobResult{Hard: true}, nil
}
