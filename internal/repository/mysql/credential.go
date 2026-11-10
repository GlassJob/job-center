package mysql

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/GlassJob/job-center/internal/model"
	"gorm.io/gorm"
)

func (s *Store) CreateCredential(ctx context.Context, c *model.Credential) error {
	if c.CreatedBy == "" {
		c.CreatedBy = "admin"
	}
	return s.db.WithContext(ctx).Create(c).Error
}

func (s *Store) GetCredential(ctx context.Context, serviceID, id string) (*model.Credential, error) {
	var c model.Credential
	err := s.db.WithContext(ctx).Where("id = ? AND service_id = ?", id, serviceID).First(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) ExistsCredentialName(ctx context.Context, serviceID, name, excludeID string) (bool, error) {
	q := s.db.WithContext(ctx).Model(&model.Credential{}).
		Where("service_id = ? AND name = ?", serviceID, name)
	if excludeID != "" {
		q = q.Where("id <> ?", excludeID)
	}
	var n int64
	if err := q.Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) ListCredentials(ctx context.Context, serviceID, keyword, status string, page, limit int) ([]model.Credential, int64, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	q := s.db.WithContext(ctx).Model(&model.Credential{}).Where("service_id = ?", serviceID)
	if kw := strings.TrimSpace(keyword); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("id LIKE ? OR name LIKE ?", like, like)
	}
	if st := strings.TrimSpace(status); st != "" {
		q = q.Where("status = ?", st)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var list []model.Credential
	err := q.Order("created_at DESC").Limit(limit).Offset((page - 1) * limit).Find(&list).Error
	return list, total, err
}

type CredentialPatch struct {
	Name               *string
	SetAllowedJobCodes bool
	AllowedJobCodes    *[]string
	SetExpiresAt       bool
	ExpiresAt          *time.Time
}

func (s *Store) PatchCredential(ctx context.Context, serviceID, id string, p CredentialPatch) (*model.Credential, error) {
	if p.Name == nil && !p.SetAllowedJobCodes && !p.SetExpiresAt {
		return s.GetCredential(ctx, serviceID, id)
	}
	if p.SetAllowedJobCodes && p.AllowedJobCodes != nil {
		row := model.Credential{AllowedJobCodes: p.AllowedJobCodes}
		fields := []string{"allowed_job_codes"}
		if p.Name != nil {
			row.Name = *p.Name
			fields = append(fields, "name")
		}
		if p.SetExpiresAt {
			row.ExpiresAt = p.ExpiresAt
			fields = append(fields, "expires_at")
		}
		res := s.db.WithContext(ctx).Model(&model.Credential{}).
			Where("id = ? AND service_id = ?", id, serviceID).
			Select(fields).Updates(&row)
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected == 0 {
			if _, err := s.GetCredential(ctx, serviceID, id); err != nil {
				return nil, err
			}
		}
		return s.GetCredential(ctx, serviceID, id)
	}

	updates := map[string]any{}
	if p.Name != nil {
		updates["name"] = *p.Name
	}
	if p.SetAllowedJobCodes {
		updates["allowed_job_codes"] = nil
	}
	if p.SetExpiresAt {
		if p.ExpiresAt == nil {
			updates["expires_at"] = nil
		} else {
			updates["expires_at"] = *p.ExpiresAt
		}
	}
	res := s.db.WithContext(ctx).Model(&model.Credential{}).
		Where("id = ? AND service_id = ?", id, serviceID).Updates(updates)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		if _, err := s.GetCredential(ctx, serviceID, id); err != nil {
			return nil, err
		}
	}
	return s.GetCredential(ctx, serviceID, id)
}

func (s *Store) RotateCredentialSecret(ctx context.Context, serviceID, id string, hash []byte, hint, hashAlgo string) (*model.Credential, error) {
	res := s.db.WithContext(ctx).Model(&model.Credential{}).
		Where("id = ? AND service_id = ? AND status <> ?", id, serviceID, model.CredentialStatusRevoked).
		Updates(map[string]any{
			"secret_hash": hash,
			"secret_hint": hint,
			"hash_algo":   hashAlgo,
			"status":      model.CredentialStatusActive,
			"revoked_at":  nil,
			"revoked_by":  nil,
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		if _, err := s.GetCredential(ctx, serviceID, id); err != nil {
			return nil, err
		}
		return nil, ErrConflict
	}
	return s.GetCredential(ctx, serviceID, id)
}

func (s *Store) RevokeCredential(ctx context.Context, serviceID, id, revokedBy string) (*model.Credential, error) {
	now := time.Now().UTC()
	res := s.db.WithContext(ctx).Model(&model.Credential{}).
		Where("id = ? AND service_id = ? AND status <> ?", id, serviceID, model.CredentialStatusRevoked).
		Updates(map[string]any{
			"status":     model.CredentialStatusRevoked,
			"revoked_at": now,
			"revoked_by": nullStr(revokedBy),
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		c, err := s.GetCredential(ctx, serviceID, id)
		if err != nil {
			return nil, err
		}
		if c.Status == model.CredentialStatusRevoked {
			return c, nil
		}
		return nil, ErrConflict
	}
	return s.GetCredential(ctx, serviceID, id)
}

func (s *Store) DeleteCredential(ctx context.Context, serviceID, id string) error {
	res := s.db.WithContext(ctx).Where("id = ? AND service_id = ?", id, serviceID).Delete(&model.Credential{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
