package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/GlassJob/job-center/internal/model"
	"github.com/GlassJob/job-center/internal/pkg/auth"
	"github.com/GlassJob/job-center/internal/pkg/idgen"
	"github.com/GlassJob/job-center/internal/repository/mysql"
)

type Credentials struct {
	store  *mysql.Store
	pepper string
}

func NewCredentials(store *mysql.Store, pepper string) *Credentials {
	return &Credentials{store: store, pepper: pepper}
}

type CreateCredentialInput struct {
	Name            string     `json:"name"`
	AllowedJobCodes *[]string  `json:"allowed_job_codes"`
	AllowedIPs      *[]string  `json:"allowed_ips"`
	ExpiresAt       *time.Time `json:"expires_at"`
}

type PatchCredentialInput struct {
	Name *string `json:"name"`
	// AllowedJobCodes: omit=不改；显式 null JSON 用 ClearAllowedJobCodes
	AllowedJobCodes      *[]string  `json:"allowed_job_codes"`
	ClearAllowedJobCodes bool       `json:"clear_allowed_job_codes"`
	AllowedIPs           *[]string  `json:"allowed_ips"`
	ClearAllowedIPs      bool       `json:"clear_allowed_ips"`
	ExpiresAt            *time.Time `json:"expires_at"`
	ClearExpiresAt       bool       `json:"clear_expires_at"`
}

func (s *Credentials) ensureService(ctx context.Context, serviceID string) error {
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

func (s *Credentials) List(ctx context.Context, serviceID, keyword, status string, page, limit int) ([]model.CredentialView, int64, error) {
	if err := s.ensureServiceExists(ctx, serviceID); err != nil {
		return nil, 0, err
	}
	if status != "" && status != model.CredentialStatusActive && status != model.CredentialStatusRotating && status != model.CredentialStatusRevoked {
		return nil, 0, Invalidf("状态")
	}
	list, total, err := s.store.ListCredentials(ctx, serviceID, keyword, status, page, limit)
	if err != nil {
		return nil, 0, err
	}
	out := make([]model.CredentialView, 0, len(list))
	for i := range list {
		out = append(out, list[i].View())
	}
	return out, total, nil
}

func (s *Credentials) ensureServiceExists(ctx context.Context, serviceID string) error {
	_, err := s.store.GetService(ctx, serviceID)
	if errors.Is(err, mysql.ErrNotFound) {
		return NotFoundf("服务")
	}
	return err
}

func (s *Credentials) Get(ctx context.Context, serviceID, id string) (*model.CredentialView, error) {
	c, err := s.store.GetCredential(ctx, serviceID, id)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	v := c.View()
	return &v, nil
}

func (s *Credentials) Create(ctx context.Context, serviceID string, in CreateCredentialInput, createdBy string) (*model.CredentialCreated, error) {
	if err := s.ensureService(ctx, serviceID); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 128 {
		return nil, Invalidf("名称")
	}
	taken, err := s.store.ExistsCredentialName(ctx, serviceID, name, "")
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, Conflictf("名称已存在")
	}
	codes, err := normalizeJobCodes(in.AllowedJobCodes)
	if err != nil {
		return nil, err
	}
	ips, err := normalizeIPs(in.AllowedIPs)
	if err != nil {
		return nil, err
	}
	secret, err := auth.NewClientSecret()
	if err != nil {
		return nil, err
	}
	enc, err := auth.EncryptSecret(s.pepper, secret)
	if err != nil {
		return nil, err
	}
	c := &model.Credential{
		ID:              idgen.New(),
		ServiceID:       serviceID,
		Name:            name,
		SecretHash:      auth.HashSecret(s.pepper, secret),
		SecretEnc:       enc,
		HashAlgo:        auth.HashAlgo,
		SecretHint:      auth.SecretHint(secret),
		Status:          model.CredentialStatusActive,
		AllowedJobCodes: codes,
		AllowedIPs:      ips,
		ExpiresAt:       in.ExpiresAt,
		CreatedBy:       createdBy,
	}
	if err := s.store.CreateCredential(ctx, c); err != nil {
		return nil, err
	}
	got, err := s.store.GetCredential(ctx, serviceID, c.ID)
	if err != nil {
		return nil, err
	}
	return &model.CredentialCreated{
		CredentialView: got.View(),
		Secret:         secret,
		Token:          auth.FormatCredentialToken(got.ID, secret),
	}, nil
}

func (s *Credentials) Patch(ctx context.Context, serviceID, id string, in PatchCredentialInput) (*model.CredentialView, error) {
	p := mysql.CredentialPatch{}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len(n) > 128 {
			return nil, Invalidf("名称")
		}
		taken, err := s.store.ExistsCredentialName(ctx, serviceID, n, id)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, Conflictf("名称已存在")
		}
		p.Name = &n
	}
	if in.ClearAllowedJobCodes {
		p.SetAllowedJobCodes = true
		p.AllowedJobCodes = nil
	} else if in.AllowedJobCodes != nil {
		codes, err := normalizeJobCodes(in.AllowedJobCodes)
		if err != nil {
			return nil, err
		}
		p.SetAllowedJobCodes = true
		p.AllowedJobCodes = codes
	}
	if in.ClearAllowedIPs {
		p.SetAllowedIPs = true
		p.AllowedIPs = nil
	} else if in.AllowedIPs != nil {
		ips, err := normalizeIPs(in.AllowedIPs)
		if err != nil {
			return nil, err
		}
		p.SetAllowedIPs = true
		p.AllowedIPs = ips
	}
	if in.ClearExpiresAt {
		p.SetExpiresAt = true
		p.ExpiresAt = nil
	} else if in.ExpiresAt != nil {
		p.SetExpiresAt = true
		p.ExpiresAt = in.ExpiresAt
	}
	c, err := s.store.PatchCredential(ctx, serviceID, id, p)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	v := c.View()
	return &v, nil
}

func (s *Credentials) Rotate(ctx context.Context, serviceID, id string) (*model.CredentialCreated, error) {
	cur, err := s.store.GetCredential(ctx, serviceID, id)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if cur.Status == model.CredentialStatusRevoked {
		return nil, Invalidf("凭据已吊销")
	}
	secret, err := auth.NewClientSecret()
	if err != nil {
		return nil, err
	}
	enc, err := auth.EncryptSecret(s.pepper, secret)
	if err != nil {
		return nil, err
	}
	c, err := s.store.RotateCredentialSecret(ctx, serviceID, id, auth.HashSecret(s.pepper, secret), enc, auth.SecretHint(secret), auth.HashAlgo)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrNotFound
	}
	if errors.Is(err, mysql.ErrConflict) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	return &model.CredentialCreated{
		CredentialView: c.View(),
		Secret:         secret,
		Token:          auth.FormatCredentialToken(c.ID, secret),
	}, nil
}

type DeleteCredentialResult struct {
	Hard       bool                  `json:"hard"`
	Credential *model.CredentialView `json:"credential,omitempty"`
}

func (s *Credentials) Delete(ctx context.Context, serviceID, id, revokedBy string, hard bool) (*DeleteCredentialResult, error) {
	if !hard {
		c, err := s.store.RevokeCredential(ctx, serviceID, id, revokedBy)
		if errors.Is(err, mysql.ErrNotFound) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		v := c.View()
		return &DeleteCredentialResult{Hard: false, Credential: &v}, nil
	}
	if err := s.store.DeleteCredential(ctx, serviceID, id); err != nil {
		if errors.Is(err, mysql.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &DeleteCredentialResult{Hard: true}, nil
}

func normalizeJobCodes(in *[]string) (*[]string, error) {
	if in == nil {
		return nil, nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(*in))
	for _, s := range *in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if len(s) > 128 {
			return nil, Invalidf("job_code 过长")
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return &out, nil
}

func normalizeIPs(in *[]string) (*[]string, error) {
	if in == nil {
		return nil, nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(*in))
	for _, s := range *in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if len(s) > 64 {
			return nil, Invalidf("allowed_ips 条目过长")
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return &out, nil
}
