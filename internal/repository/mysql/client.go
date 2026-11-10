package mysql

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/GlassJob/job-center/internal/model"
	"gorm.io/gorm"
)

func (s *Store) CountClients(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&model.Client{}).Count(&n).Error
	return n, err
}

func (s *Store) CountActiveClients(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&model.Client{}).
		Where("status = ?", model.ClientStatusActive).Count(&n).Error
	return n, err
}

func (s *Store) CreateClient(ctx context.Context, c *model.Client) error {
	if c.CreatedBy == "" {
		c.CreatedBy = "bootstrap"
	}
	return s.db.WithContext(ctx).Create(c).Error
}

func (s *Store) GetClientByID(ctx context.Context, id string) (*model.Client, error) {
	var c model.Client
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ExistsClientName 检查非 REVOKED 的 client 是否已占用该 name。
// excludeID 非空时排除自身（用于 Patch 改名）。
func (s *Store) ExistsClientName(ctx context.Context, name, excludeID string) (bool, error) {
	q := s.db.WithContext(ctx).Model(&model.Client{}).
		Where("name = ? AND status <> ?", name, model.ClientStatusRevoked)
	if excludeID != "" {
		q = q.Where("id <> ?", excludeID)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) ListClients(ctx context.Context, keyword, status string, page, limit int) ([]model.Client, int64, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	q := s.db.WithContext(ctx).Model(&model.Client{})
	if kw := strings.TrimSpace(keyword); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("name LIKE ? OR id LIKE ?", like, like)
	}
	if st := strings.TrimSpace(status); st != "" {
		q = q.Where("status = ?", st)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var list []model.Client
	err := q.Order("created_at DESC").Limit(limit).Offset((page - 1) * limit).Find(&list).Error
	return list, total, err
}

type ClientPatch struct {
	Name        *string
	Description *string
	Status      *string
	Scopes      *[]string
}

func (s *Store) PatchClient(ctx context.Context, id string, p ClientPatch) (*model.Client, error) {
	row := model.Client{}
	fields := make([]string, 0, 4)
	if p.Name != nil {
		row.Name = *p.Name
		fields = append(fields, "name")
	}
	if p.Description != nil {
		row.Description = *p.Description
		fields = append(fields, "description")
	}
	if p.Status != nil {
		row.Status = *p.Status
		fields = append(fields, "status")
	}
	if p.Scopes != nil {
		row.Scopes = *p.Scopes
		fields = append(fields, "scopes")
	}
	if len(fields) == 0 {
		return s.GetClientByID(ctx, id)
	}
	res := s.db.WithContext(ctx).Model(&model.Client{}).Where("id = ?", id).Select(fields).Updates(&row)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		if _, err := s.GetClientByID(ctx, id); err != nil {
			return nil, err
		}
	}
	return s.GetClientByID(ctx, id)
}

func (s *Store) RotateClientSecret(ctx context.Context, id string, hash, enc []byte, hint, hashAlgo string) (*model.Client, error) {
	res := s.db.WithContext(ctx).Model(&model.Client{}).Where("id = ? AND status <> ?", id, model.ClientStatusRevoked).Updates(map[string]any{
		"secret_hash": hash,
		"secret_enc":  enc,
		"secret_hint": hint,
		"hash_algo":   hashAlgo,
		"status":      model.ClientStatusActive,
		"revoked_at":  nil,
		"revoked_by":  nil,
	})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		if _, err := s.GetClientByID(ctx, id); err != nil {
			return nil, err
		}
		return nil, ErrConflict
	}
	return s.GetClientByID(ctx, id)
}

func (s *Store) RevokeClient(ctx context.Context, id, revokedBy string) (*model.Client, error) {
	now := time.Now().UTC()
	res := s.db.WithContext(ctx).Model(&model.Client{}).Where("id = ? AND status <> ?", id, model.ClientStatusRevoked).Updates(map[string]any{
		"status":     model.ClientStatusRevoked,
		"revoked_at": now,
		"revoked_by": nullStr(revokedBy),
	})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		c, err := s.GetClientByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if c.Status == model.ClientStatusRevoked {
			return c, nil
		}
		return nil, ErrConflict
	}
	return s.GetClientByID(ctx, id)
}

func (s *Store) TouchClientUsed(ctx context.Context, id, ip string) {
	_ = s.db.WithContext(ctx).Model(&model.Client{}).Where("id = ?", id).Updates(map[string]any{
		"last_used_at": time.Now().UTC(),
		"last_used_ip": ip,
	}).Error
}

func (s *Store) DeleteClient(ctx context.Context, id string) error {
	res := s.db.WithContext(ctx).Where("id = ?", id).Delete(&model.Client{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
