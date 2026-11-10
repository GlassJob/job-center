package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/GlassJob/job-center/internal/model"
	"github.com/GlassJob/job-center/internal/pkg/idgen"
	"github.com/GlassJob/job-center/internal/repository/mysql"
)

var (
	ErrInvalidInput = errors.New("invalid input")
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
)

var serviceCodeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,63}$`)

type Services struct {
	store *mysql.Store
}

func NewServices(store *mysql.Store) *Services {
	return &Services{store: store}
}

type CreateServiceInput struct {
	ServiceCode string `json:"service_code"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (s *Services) CreateService(ctx context.Context, in CreateServiceInput) (*model.Service, error) {
	code := strings.TrimSpace(in.ServiceCode)
	name := strings.TrimSpace(in.Name)
	if !serviceCodeRe.MatchString(code) {
		return nil, fmt.Errorf("%w: service_code", ErrInvalidInput)
	}
	if name == "" || len(name) > 128 {
		return nil, fmt.Errorf("%w: name", ErrInvalidInput)
	}
	if len(in.Description) > 1000 {
		return nil, fmt.Errorf("%w: description", ErrInvalidInput)
	}
	svc := &model.Service{
		ID:          idgen.New(),
		ServiceCode: code,
		Name:        name,
		Description: strings.TrimSpace(in.Description),
		Status:      model.ServiceStatusActive,
	}
	if err := s.store.CreateService(ctx, svc); err != nil {
		if errors.Is(err, mysql.ErrConflict) {
			return nil, ErrConflict
		}
		return nil, err
	}
	return s.store.GetService(ctx, svc.ID)
}

func (s *Services) GetService(ctx context.Context, id string) (*model.ServiceDetail, error) {
	d, err := s.store.GetServiceDetail(ctx, id)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrNotFound
	}
	return d, err
}

func (s *Services) ListServices(ctx context.Context, keyword string, page, limit int) ([]model.Service, int64, error) {
	return s.store.ListServices(ctx, keyword, page, limit)
}

type PatchServiceInput struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Status      *string `json:"status"`
}

func (s *Services) PatchService(ctx context.Context, id string, in PatchServiceInput) (*model.Service, error) {
	p := mysql.ServicePatch{}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len(n) > 128 {
			return nil, fmt.Errorf("%w: name", ErrInvalidInput)
		}
		p.Name = &n
	}
	if in.Description != nil {
		d := strings.TrimSpace(*in.Description)
		if len(d) > 1000 {
			return nil, fmt.Errorf("%w: description", ErrInvalidInput)
		}
		p.Description = &d
	}
	if in.Status != nil {
		st := strings.TrimSpace(*in.Status)
		if st != model.ServiceStatusActive && st != model.ServiceStatusDisabled {
			return nil, fmt.Errorf("%w: status", ErrInvalidInput)
		}
		p.Status = &st
	}
	svc, err := s.store.PatchService(ctx, id, p)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrNotFound
	}
	return svc, err
}

type DeleteServiceResult struct {
	Hard    bool           `json:"hard"`
	Service *model.Service `json:"service,omitempty"`
}

// DeleteService 默认软删（DISABLED）；hard=true 时物理删除（无依赖才可）。
func (s *Services) DeleteService(ctx context.Context, id string, hard bool) (*DeleteServiceResult, error) {
	cur, err := s.store.GetService(ctx, id)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !hard {
		if cur.Status == model.ServiceStatusDisabled {
			return &DeleteServiceResult{Hard: false, Service: cur}, nil
		}
		st := model.ServiceStatusDisabled
		svc, err := s.store.PatchService(ctx, id, mysql.ServicePatch{Status: &st})
		if errors.Is(err, mysql.ErrNotFound) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		return &DeleteServiceResult{Hard: false, Service: svc}, nil
	}
	jobs, instances, credentials, deployments, err := s.store.CountServiceDependents(ctx, id)
	if err != nil {
		return nil, err
	}
	if jobs+instances+credentials+deployments > 0 {
		return nil, fmt.Errorf("%w: service still has dependents (jobs=%d credentials=%d instances=%d deployments=%d)",
			ErrConflict, jobs, credentials, instances, deployments)
	}
	if err := s.store.DeleteService(ctx, id); err != nil {
		if errors.Is(err, mysql.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &DeleteServiceResult{Hard: true}, nil
}
