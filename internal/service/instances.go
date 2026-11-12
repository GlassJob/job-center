package service

import (
	"context"
	"errors"
	"strings"

	"github.com/GlassJob/job-center/internal/model"
	"github.com/GlassJob/job-center/internal/repository/mysql"
)

type Instances struct {
	store *mysql.Store
}

func NewInstances(store *mysql.Store) *Instances {
	return &Instances{store: store}
}

func (s *Instances) ensureServiceExists(ctx context.Context, serviceID string) error {
	_, err := s.store.GetService(ctx, serviceID)
	if errors.Is(err, mysql.ErrNotFound) {
		return NotFoundf("服务")
	}
	return err
}

func (s *Instances) List(ctx context.Context, serviceID, keyword, status string, page, limit int) ([]model.InstanceView, int64, error) {
	if sid := strings.TrimSpace(serviceID); sid != "" {
		if err := s.ensureServiceExists(ctx, sid); err != nil {
			return nil, 0, err
		}
	}
	status = strings.TrimSpace(status)
	if status != "" {
		switch status {
		case model.InstanceStatusRegistering, model.InstanceStatusOnline, model.InstanceStatusSuspect,
			model.InstanceStatusOffline, model.InstanceStatusDraining:
		default:
			return nil, 0, Invalidf("状态")
		}
	}
	list, total, err := s.store.ListInstances(ctx, serviceID, keyword, status, page, limit)
	if err != nil {
		return nil, 0, err
	}
	out := make([]model.InstanceView, 0, len(list))
	for i := range list {
		jobs, err := s.store.ListInstanceJobViews(ctx, list[i].ID)
		if err != nil {
			return nil, 0, err
		}
		v := list[i].View(jobs)
		v.ServiceCode = list[i].ServiceCode
		v.ServiceName = list[i].ServiceName
		out = append(out, v)
	}
	return out, total, nil
}

// ListGlobal 全局实例列表（serviceID 可空）。
func (s *Instances) ListGlobal(ctx context.Context, serviceID, keyword, status string, page, limit int) ([]model.InstanceView, int64, error) {
	return s.List(ctx, serviceID, keyword, status, page, limit)
}

func (s *Instances) Get(ctx context.Context, serviceID, instanceID string) (*model.InstanceView, error) {
	if err := s.ensureServiceExists(ctx, serviceID); err != nil {
		return nil, err
	}
	inst, err := s.store.GetInstanceInService(ctx, serviceID, instanceID)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	jobs, err := s.store.ListInstanceJobViews(ctx, inst.ID)
	if err != nil {
		return nil, err
	}
	v := inst.View(jobs)
	return &v, nil
}
