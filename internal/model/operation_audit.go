package model

import (
	"encoding/json"
	"time"
)

const (
	AuditResultSuccess = "SUCCESS"
	AuditResultDenied  = "DENIED"
	AuditResultFailed  = "FAILED"
)

type OperationAudit struct {
	ID           uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	ClientID     string    `gorm:"column:client_id"`
	OperatorID   string    `gorm:"column:operator_id"`
	OperatorName string    `gorm:"column:operator_name"`
	Action       string    `gorm:"column:action"`
	Permission   string    `gorm:"column:permission"`
	ResourceType string    `gorm:"column:resource_type"`
	ResourceID   string    `gorm:"column:resource_id"`
	RequestID    string    `gorm:"column:request_id"`
	TraceID      string    `gorm:"column:trace_id"`
	ClientIP     string    `gorm:"column:client_ip"`
	BeforeData   []byte    `gorm:"column:before_data"`
	AfterData    []byte    `gorm:"column:after_data"`
	Result       string    `gorm:"column:result"`
	ErrorMessage string    `gorm:"column:error_message"`
	CreatedAt    time.Time `gorm:"column:created_at"`
}

func (OperationAudit) TableName() string { return "operation_audits" }

type OperationAuditView struct {
	ID           uint64    `json:"id"`
	ClientID     string    `json:"client_id"`
	OperatorID   string    `json:"operator_id,omitempty"`
	OperatorName string    `json:"operator_name,omitempty"`
	Action       string    `json:"action"`
	Permission   string    `json:"permission,omitempty"`
	ResourceType string    `json:"resource_type"`
	ResourceID   string    `json:"resource_id,omitempty"`
	RequestID    string    `json:"request_id,omitempty"`
	TraceID      string    `json:"trace_id,omitempty"`
	ClientIP     string    `json:"client_ip,omitempty"`
	BeforeData   any       `json:"before_data,omitempty"`
	AfterData    any       `json:"after_data,omitempty"`
	Result       string    `json:"result"`
	ErrorMessage string    `json:"error_message,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

func (a *OperationAudit) View() OperationAuditView {
	return OperationAuditView{
		ID:           a.ID,
		ClientID:     a.ClientID,
		OperatorID:   a.OperatorID,
		OperatorName: a.OperatorName,
		Action:       a.Action,
		Permission:   a.Permission,
		ResourceType: a.ResourceType,
		ResourceID:   a.ResourceID,
		RequestID:    a.RequestID,
		TraceID:      a.TraceID,
		ClientIP:     a.ClientIP,
		BeforeData:   decodeAuditJSON(a.BeforeData),
		AfterData:    decodeAuditJSON(a.AfterData),
		Result:       a.Result,
		ErrorMessage: a.ErrorMessage,
		CreatedAt:    a.CreatedAt,
	}
}

func decodeAuditJSON(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return json.RawMessage(raw)
	}
	return v
}
