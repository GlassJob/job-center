package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/GlassJob/job-center/internal/model"
	"github.com/GlassJob/job-center/internal/repository/mysql"
)

type Audits struct {
	store *mysql.Store
}

func NewAudits(store *mysql.Store) *Audits {
	return &Audits{store: store}
}

func (s *Audits) List(
	ctx context.Context,
	clientID, operatorID, action, resourceType, resourceID, result, createdFrom, createdTo string,
	page, limit int,
) ([]model.OperationAuditView, int64, error) {
	if result != "" &&
		result != model.AuditResultSuccess &&
		result != model.AuditResultDenied &&
		result != model.AuditResultFailed {
		return nil, 0, Invalidf("结果")
	}

	from, err := parseOptionalTime("created_from", createdFrom)
	if err != nil {
		return nil, 0, err
	}
	to, err := parseOptionalTime("created_to", createdTo)
	if err != nil {
		return nil, 0, err
	}
	if from != nil && to != nil && !from.Before(*to) {
		return nil, 0, Invalidf("开始时间必须早于结束时间")
	}

	list, total, err := s.store.ListAudits(ctx, mysql.ListAuditsFilter{
		ClientID:     strings.TrimSpace(clientID),
		OperatorID:   strings.TrimSpace(operatorID),
		Action:       strings.TrimSpace(action),
		ResourceType: strings.TrimSpace(resourceType),
		ResourceID:   strings.TrimSpace(resourceID),
		Result:       strings.TrimSpace(result),
		CreatedFrom:  from,
		CreatedTo:    to,
		Page:         page,
		Limit:        limit,
	})
	if err != nil {
		return nil, 0, err
	}
	out := make([]model.OperationAuditView, 0, len(list))
	for i := range list {
		out = append(out, list[i].View())
	}
	return out, total, nil
}

func (s *Audits) Get(ctx context.Context, auditID string) (*model.OperationAuditView, error) {
	id, err := strconv.ParseUint(strings.TrimSpace(auditID), 10, 64)
	if err != nil || id == 0 {
		return nil, Invalidf("审计 ID")
	}
	row, err := s.store.GetAudit(ctx, id)
	if err != nil {
		if errors.Is(err, mysql.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	v := row.View()
	return &v, nil
}

func parseOptionalTime(name, raw string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t, err = time.Parse(time.RFC3339, raw)
	}
	if err != nil {
		return nil, Invalidf("%s 须为 RFC3339 时间", name)
	}
	t = t.UTC()
	return &t, nil
}
