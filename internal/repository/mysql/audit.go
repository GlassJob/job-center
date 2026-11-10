package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/GlassJob/job-center/internal/model"
	"gorm.io/gorm"
)

type AuditRow struct {
	ClientID     string
	OperatorID   string
	OperatorName string
	Action       string
	Permission   string
	ResourceType string
	ResourceID   string
	RequestID    string
	TraceID      string
	ClientIP     string
	Before       any
	After        any
	Result       string
	ErrorMessage string
}

type ListAuditsFilter struct {
	ClientID     string
	OperatorID   string
	Action       string
	ResourceType string
	ResourceID   string
	Result       string
	CreatedFrom  *time.Time
	CreatedTo    *time.Time
	Page         int
	Limit        int
}

type operationAuditWrite struct {
	ClientID     string  `gorm:"column:client_id"`
	OperatorID   *string `gorm:"column:operator_id"`
	OperatorName *string `gorm:"column:operator_name"`
	Action       string  `gorm:"column:action"`
	Permission   *string `gorm:"column:permission"`
	ResourceType string  `gorm:"column:resource_type"`
	ResourceID   *string `gorm:"column:resource_id"`
	RequestID    *string `gorm:"column:request_id"`
	TraceID      *string `gorm:"column:trace_id"`
	ClientIP     *string `gorm:"column:client_ip"`
	BeforeData   []byte  `gorm:"column:before_data"`
	AfterData    []byte  `gorm:"column:after_data"`
	Result       string  `gorm:"column:result"`
	ErrorMessage *string `gorm:"column:error_message"`
}

func (operationAuditWrite) TableName() string { return "operation_audits" }

func (s *Store) InsertAudit(ctx context.Context, a AuditRow) error {
	row := operationAuditWrite{
		ClientID:     a.ClientID,
		OperatorID:   nullPtr(a.OperatorID),
		OperatorName: nullPtr(a.OperatorName),
		Action:       a.Action,
		Permission:   nullPtr(a.Permission),
		ResourceType: a.ResourceType,
		ResourceID:   nullPtr(a.ResourceID),
		RequestID:    nullPtr(a.RequestID),
		TraceID:      nullPtr(a.TraceID),
		ClientIP:     nullPtr(a.ClientIP),
		Result:       a.Result,
		ErrorMessage: nullPtr(a.ErrorMessage),
	}
	if a.Before != nil {
		b, err := json.Marshal(a.Before)
		if err != nil {
			return err
		}
		row.BeforeData = b
	}
	if a.After != nil {
		b, err := json.Marshal(a.After)
		if err != nil {
			return err
		}
		row.AfterData = b
	}
	return s.db.WithContext(ctx).Create(&row).Error
}

func (s *Store) GetAudit(ctx context.Context, id uint64) (*model.OperationAudit, error) {
	var row model.OperationAudit
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *Store) ListAudits(ctx context.Context, f ListAuditsFilter) ([]model.OperationAudit, int64, error) {
	page, limit := f.Page, f.Limit
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	q := s.db.WithContext(ctx).Model(&model.OperationAudit{})
	if v := strings.TrimSpace(f.ClientID); v != "" {
		q = q.Where("client_id = ?", v)
	}
	if v := strings.TrimSpace(f.OperatorID); v != "" {
		q = q.Where("operator_id = ?", v)
	}
	if v := strings.TrimSpace(f.Action); v != "" {
		q = q.Where("action = ?", v)
	}
	if v := strings.TrimSpace(f.ResourceType); v != "" {
		q = q.Where("resource_type = ?", v)
	}
	if v := strings.TrimSpace(f.ResourceID); v != "" {
		q = q.Where("resource_id = ?", v)
	}
	if v := strings.TrimSpace(f.Result); v != "" {
		q = q.Where("result = ?", v)
	}
	if f.CreatedFrom != nil {
		q = q.Where("created_at >= ?", *f.CreatedFrom)
	}
	if f.CreatedTo != nil {
		q = q.Where("created_at < ?", *f.CreatedTo)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var list []model.OperationAudit
	err := q.Order("id DESC").Limit(limit).Offset((page - 1) * limit).Find(&list).Error
	if err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

func nullPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
