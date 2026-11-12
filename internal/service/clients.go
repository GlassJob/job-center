package service

import (
	"context"
	"errors"
	"strings"

	"github.com/GlassJob/job-center/internal/model"
	"github.com/GlassJob/job-center/internal/pkg/auth"
	"github.com/GlassJob/job-center/internal/pkg/idgen"
	"github.com/GlassJob/job-center/internal/repository/mysql"
)

type Clients struct {
	store  *mysql.Store
	pepper string
}

func NewClients(store *mysql.Store, pepper string) *Clients {
	return &Clients{store: store, pepper: pepper}
}

type CreateClientInput struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Scopes      []string `json:"scopes"`
}

type PatchClientInput struct {
	Name        *string   `json:"name"`
	Description *string   `json:"description"`
	Status      *string   `json:"status"`
	Scopes      *[]string `json:"scopes"`
}

func (s *Clients) List(ctx context.Context, keyword, status string, page, limit int) ([]model.ClientView, int64, error) {
	if status != "" && status != model.ClientStatusActive && status != model.ClientStatusRotating && status != model.ClientStatusRevoked {
		return nil, 0, Invalidf("状态")
	}
	list, total, err := s.store.ListClients(ctx, keyword, status, page, limit)
	if err != nil {
		return nil, 0, err
	}
	out := make([]model.ClientView, 0, len(list))
	for i := range list {
		out = append(out, list[i].View())
	}
	return out, total, nil
}

func (s *Clients) Get(ctx context.Context, id string) (*model.ClientView, error) {
	c, err := s.store.GetClientByID(ctx, id)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	v := c.View()
	return &v, nil
}

func (s *Clients) Create(ctx context.Context, in CreateClientInput, createdBy string) (*model.ClientCreated, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 128 {
		return nil, Invalidf("名称")
	}
	desc := strings.TrimSpace(in.Description)
	if len(desc) > 1000 {
		return nil, Invalidf("描述")
	}
	scopes, err := normalizeScopes(in.Scopes)
	if err != nil {
		return nil, err
	}
	taken, err := s.store.ExistsClientName(ctx, name, "")
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, Conflictf("名称已存在")
	}
	secret, err := auth.NewClientSecret()
	if err != nil {
		return nil, err
	}
	enc, err := auth.EncryptSecret(s.pepper, secret)
	if err != nil {
		return nil, err
	}
	c := &model.Client{
		ID:          idgen.New(),
		Name:        name,
		Description: desc,
		SecretHash:  auth.HashSecret(s.pepper, secret),
		SecretEnc:   enc,
		HashAlgo:    auth.HashAlgo,
		SecretHint:  auth.SecretHint(secret),
		Status:      model.ClientStatusActive,
		Scopes:      scopes,
		CreatedBy:   createdBy,
	}
	if err := s.store.CreateClient(ctx, c); err != nil {
		return nil, err
	}
	got, err := s.store.GetClientByID(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	return &model.ClientCreated{ClientView: got.View(), Secret: secret}, nil
}

func (s *Clients) Patch(ctx context.Context, id string, in PatchClientInput) (*model.ClientView, error) {
	p := mysql.ClientPatch{}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len(n) > 128 {
			return nil, Invalidf("名称")
		}
		taken, err := s.store.ExistsClientName(ctx, n, id)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, Conflictf("名称已存在")
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
		// PATCH 只允许 ACTIVE；吊销走 revoke，轮换走 rotate
		if st != model.ClientStatusActive {
			return nil, Invalidf("请使用吊销/轮换接口变更状态")
		}
		cur, err := s.store.GetClientByID(ctx, id)
		if errors.Is(err, mysql.ErrNotFound) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if cur.Status == model.ClientStatusRevoked {
			return nil, Invalidf("已吊销的客户端不能重新激活")
		}
		p.Status = &st
	}
	if in.Scopes != nil {
		scopes, err := normalizeScopes(*in.Scopes)
		if err != nil {
			return nil, err
		}
		p.Scopes = &scopes
	}
	c, err := s.store.PatchClient(ctx, id, p)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	v := c.View()
	return &v, nil
}

func (s *Clients) Rotate(ctx context.Context, id string) (*model.ClientCreated, error) {
	cur, err := s.store.GetClientByID(ctx, id)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if cur.Status == model.ClientStatusRevoked {
		return nil, Invalidf("客户端已吊销")
	}
	secret, err := auth.NewClientSecret()
	if err != nil {
		return nil, err
	}
	enc, err := auth.EncryptSecret(s.pepper, secret)
	if err != nil {
		return nil, err
	}
	c, err := s.store.RotateClientSecret(ctx, id, auth.HashSecret(s.pepper, secret), enc, auth.SecretHint(secret), auth.HashAlgo)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrNotFound
	}
	if errors.Is(err, mysql.ErrConflict) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	return &model.ClientCreated{ClientView: c.View(), Secret: secret}, nil
}

func (s *Clients) Revoke(ctx context.Context, id, revokedBy string) (*model.ClientView, error) {
	cur, err := s.store.GetClientByID(ctx, id)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if cur.Status == model.ClientStatusRevoked {
		v := cur.View()
		return &v, nil
	}
	if cur.Status == model.ClientStatusActive {
		n, err := s.store.CountActiveClients(ctx)
		if err != nil {
			return nil, err
		}
		if n <= 1 {
			return nil, Forbiddenf("不能吊销最后一个可用客户端")
		}
	}
	c, err := s.store.RevokeClient(ctx, id, revokedBy)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	v := c.View()
	return &v, nil
}

type DeleteClientResult struct {
	Hard   bool              `json:"hard"`
	Client *model.ClientView `json:"client,omitempty"`
}

// Delete 默认软删（REVOKED，同 Revoke）；hard=true 时物理删除。
// 最后一个 ACTIVE client 不允许软删/硬删。
func (s *Clients) Delete(ctx context.Context, id, revokedBy string, hard bool) (*DeleteClientResult, error) {
	if !hard {
		v, err := s.Revoke(ctx, id, revokedBy)
		if err != nil {
			return nil, err
		}
		return &DeleteClientResult{Hard: false, Client: v}, nil
	}
	cur, err := s.store.GetClientByID(ctx, id)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if cur.Status == model.ClientStatusActive {
		n, err := s.store.CountActiveClients(ctx)
		if err != nil {
			return nil, err
		}
		if n <= 1 {
			return nil, Forbiddenf("不能删除最后一个可用客户端")
		}
	}
	if err := s.store.DeleteClient(ctx, id); err != nil {
		if errors.Is(err, mysql.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &DeleteClientResult{Hard: true}, nil
}

func normalizeScopes(in []string) ([]string, error) {
	if in == nil {
		return []string{}, nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if len(s) > 64 {
			return nil, Invalidf("scope 过长")
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out, nil
}
