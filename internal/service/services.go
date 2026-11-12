package service

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/GlassJob/job-center/internal/model"
	"github.com/GlassJob/job-center/internal/pkg/idgen"
	"github.com/GlassJob/job-center/internal/repository/mysql"
)

var serviceCodeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,63}$`)

type Services struct {
	store *mysql.Store
}

func NewServices(store *mysql.Store) *Services {
	return &Services{store: store}
}

type CreateServiceInput struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (s *Services) CreateService(ctx context.Context, in CreateServiceInput) (*model.Service, error) {
	code := strings.TrimSpace(in.Code)
	name := strings.TrimSpace(in.Name)
	if !serviceCodeRe.MatchString(code) {
		return nil, Invalidf("编码")
	}
	if name == "" || len(name) > 128 {
		return nil, Invalidf("名称")
	}
	if len(in.Description) > 1000 {
		return nil, Invalidf("描述")
	}
	svc := &model.Service{
		ID:          idgen.New(),
		Code:        code,
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

func (s *Services) ListServices(ctx context.Context, keyword string, page, limit int) ([]model.ServiceDetail, int64, error) {
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
			return nil, Invalidf("名称")
		}
		p.Name = &n
	}
	if in.Description != nil {
		d := strings.TrimSpace(*in.Description)
		if len(d) > 1000 {
			return nil, Invalidf("描述")
		}
		p.Description = &d
	}
	if in.Status != nil {
		st := strings.TrimSpace(*in.Status)
		if st != model.ServiceStatusActive && st != model.ServiceStatusDisabled {
			return nil, Invalidf("状态")
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
		return nil, Conflictf("服务仍有依赖资源（jobs=%d credentials=%d instances=%d deployments=%d）",
			jobs, credentials, instances, deployments)
	}
	if err := s.store.DeleteService(ctx, id); err != nil {
		if errors.Is(err, mysql.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &DeleteServiceResult{Hard: true}, nil
}
