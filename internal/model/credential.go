package model

import "time"

const (
	CredentialStatusActive   = "ACTIVE"
	CredentialStatusRotating = "ROTATING"
	CredentialStatusRevoked  = "REVOKED"
)

type Credential struct {
	ID              string     `gorm:"column:id;primaryKey"`
	ServiceID       string     `gorm:"column:service_id"`
	Name            string     `gorm:"column:name"`
	SecretHash      []byte     `gorm:"column:secret_hash"`
	HashAlgo        string     `gorm:"column:hash_algo"`
	SecretHint      string     `gorm:"column:secret_hint"`
	Status          string     `gorm:"column:status"`
	AllowedJobCodes *[]string  `gorm:"column:allowed_job_codes;serializer:json"`
	ExpiresAt       *time.Time `gorm:"column:expires_at"`
	LastUsedAt      *time.Time `gorm:"column:last_used_at"`
	LastUsedIP      string     `gorm:"column:last_used_ip"`
	CreatedBy       string     `gorm:"column:created_by"`
	RevokedAt       *time.Time `gorm:"column:revoked_at"`
	RevokedBy       string     `gorm:"column:revoked_by"`
	CreatedAt       time.Time  `gorm:"column:created_at"`
}

func (Credential) TableName() string { return "service_credentials" }

// CredentialView 对外视图：永不包含 secret_hash / 明文 secret。
type CredentialView struct {
	ID              string     `json:"id"`
	ServiceID       string     `json:"service_id"`
	Name            string     `json:"name"`
	SecretHint      string     `json:"secret_hint,omitempty"`
	HashAlgo        string     `json:"hash_algo"`
	Status          string     `json:"status"`
	AllowedJobCodes *[]string  `json:"allowed_job_codes"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	LastUsedAt      *time.Time `json:"last_used_at,omitempty"`
	LastUsedIP      string     `json:"last_used_ip,omitempty"`
	CreatedBy       string     `json:"created_by,omitempty"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`
	RevokedBy       string     `json:"revoked_by,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

func (c *Credential) View() CredentialView {
	return CredentialView{
		ID:              c.ID,
		ServiceID:       c.ServiceID,
		Name:            c.Name,
		SecretHint:      c.SecretHint,
		HashAlgo:        c.HashAlgo,
		Status:          c.Status,
		AllowedJobCodes: c.AllowedJobCodes,
		ExpiresAt:       c.ExpiresAt,
		LastUsedAt:      c.LastUsedAt,
		LastUsedIP:      c.LastUsedIP,
		CreatedBy:       c.CreatedBy,
		RevokedAt:       c.RevokedAt,
		RevokedBy:       c.RevokedBy,
		CreatedAt:       c.CreatedAt,
	}
}

// CredentialCreated 创建/轮换响应：secret 仅此一次。
type CredentialCreated struct {
	CredentialView
	Secret string `json:"secret"`
}
