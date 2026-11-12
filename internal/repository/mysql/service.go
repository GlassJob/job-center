package mysql

import (
	"context"
	"errors"
	"strings"

	"github.com/GlassJob/job-center/internal/model"
	"gorm.io/gorm"
)

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("conflict")

func (s *Store) CreateService(ctx context.Context, svc *model.Service) error {
	err := s.db.WithContext(ctx).Create(svc).Error
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return ErrConflict
	}
	return err
}

func (s *Store) GetService(ctx context.Context, id string) (*model.Service, error) {
	var svc model.Service
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&svc).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &svc, nil
}

const serviceDetailSelect = `
		SELECT s.id, s.code, s.name, COALESCE(s.description,'') AS description, s.status, s.created_at, s.updated_at,
			(SELECT COUNT(*) FROM jobs WHERE service_id = s.id) AS job_count,
			(SELECT COUNT(*) FROM instances WHERE service_id = s.id) AS instance_count,
			(SELECT COUNT(*) FROM instances WHERE service_id = s.id AND status = 'ONLINE') AS online_instance_count,
			(SELECT COUNT(*) FROM service_credentials WHERE service_id = s.id) AS credential_count
		FROM services s`

// GetServiceDetail 带子查询计数；用 Raw 保留复杂 SQL 能力。
func (s *Store) GetServiceDetail(ctx context.Context, id string) (*model.ServiceDetail, error) {
	var d model.ServiceDetail
	tx := s.db.WithContext(ctx).Raw(serviceDetailSelect+` WHERE s.id = ?`, id).Scan(&d)
	if tx.Error != nil {
		return nil, tx.Error
	}
	if tx.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return &d, nil
}

func (s *Store) ListServices(ctx context.Context, keyword string, page, limit int) ([]model.ServiceDetail, int64, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	countQ := s.db.WithContext(ctx).Model(&model.Service{})
	if kw := strings.TrimSpace(keyword); kw != "" {
		like := "%" + kw + "%"
		countQ = countQ.Where("code LIKE ? OR name LIKE ?", like, like)
	}
	var total int64
	if err := countQ.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	sql := serviceDetailSelect
	args := []any{}
	if kw := strings.TrimSpace(keyword); kw != "" {
		like := "%" + kw + "%"
		sql += ` WHERE s.code LIKE ? OR s.name LIKE ?`
		args = append(args, like, like)
	}
	sql += ` ORDER BY s.created_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, (page-1)*limit)

	var list []model.ServiceDetail
	if err := s.db.WithContext(ctx).Raw(sql, args...).Scan(&list).Error; err != nil {
		return nil, 0, err
	}
	if list == nil {
		list = []model.ServiceDetail{}
	}
	return list, total, nil
}

type ServicePatch struct {
	Name        *string
	Description *string
	Status      *string
}

func (s *Store) PatchService(ctx context.Context, id string, p ServicePatch) (*model.Service, error) {
	updates := map[string]any{}
	if p.Name != nil {
		updates["name"] = *p.Name
	}
	if p.Description != nil {
		if *p.Description == "" {
			updates["description"] = nil
		} else {
			updates["description"] = *p.Description
		}
	}
	if p.Status != nil {
		updates["status"] = *p.Status
	}
	if len(updates) == 0 {
		return s.GetService(ctx, id)
	}
	res := s.db.WithContext(ctx).Model(&model.Service{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		if _, err := s.GetService(ctx, id); err != nil {
			return nil, err
		}
	}
	return s.GetService(ctx, id)
}

func (s *Store) CountServiceDependents(ctx context.Context, id string) (jobs, instances, credentials, deployments int64, err error) {
	type row struct {
		Jobs        int64 `gorm:"column:jobs"`
		Instances   int64 `gorm:"column:instances"`
		Credentials int64 `gorm:"column:credentials"`
		Deployments int64 `gorm:"column:deployments"`
	}
	var r row
	err = s.db.WithContext(ctx).Raw(`
		SELECT
			(SELECT COUNT(*) FROM jobs WHERE service_id = ?) AS jobs,
			(SELECT COUNT(*) FROM instances WHERE service_id = ?) AS instances,
			(SELECT COUNT(*) FROM service_credentials WHERE service_id = ?) AS credentials,
			(SELECT COUNT(*) FROM deployments WHERE service_id = ?) AS deployments
	`, id, id, id, id).Scan(&r).Error
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return r.Jobs, r.Instances, r.Credentials, r.Deployments, nil
}

func (s *Store) DeleteService(ctx context.Context, id string) error {
	res := s.db.WithContext(ctx).Where("id = ?", id).Delete(&model.Service{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
