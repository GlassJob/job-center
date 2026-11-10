package model

import "time"

const (
	ClientStatusActive   = "ACTIVE"
	ClientStatusRotating = "ROTATING"
	ClientStatusRevoked  = "REVOKED"
)

type Client struct {
	ID          string     `gorm:"column:id;primaryKey"`
	Name        string     `gorm:"column:name"`
	Description string     `gorm:"column:description"`
	SecretHash  []byte     `gorm:"column:secret_hash"`
	SecretEnc   []byte     `gorm:"column:secret_enc"`
	HashAlgo    string     `gorm:"column:hash_algo"`
	SecretHint  string     `gorm:"column:secret_hint"`
	Status      string     `gorm:"column:status"`
	Scopes      []string   `gorm:"column:scopes;serializer:json"`
	CreatedBy   string     `gorm:"column:created_by"`
	LastUsedAt  *time.Time `gorm:"column:last_used_at"`
	LastUsedIP  string     `gorm:"column:last_used_ip"`
	RevokedAt   *time.Time `gorm:"column:revoked_at"`
	RevokedBy   string     `gorm:"column:revoked_by"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
	UpdatedAt   time.Time  `gorm:"column:updated_at"`
}

func (Client) TableName() string { return "clients" }

// ClientView 对外视图：永不包含 secret_hash / secret_enc / 明文 secret。
type ClientView struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	SecretHint  string     `json:"secret_hint,omitempty"`
	HashAlgo    string     `json:"hash_algo"`
	Status      string     `json:"status"`
	Scopes      []string   `json:"scopes"`
	CreatedBy   string     `json:"created_by,omitempty"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	LastUsedIP  string     `json:"last_used_ip,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	RevokedBy   string     `json:"revoked_by,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func (c *Client) View() ClientView {
	scopes := c.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	return ClientView{
		ID:          c.ID,
		Name:        c.Name,
		Description: c.Description,
		SecretHint:  c.SecretHint,
		HashAlgo:    c.HashAlgo,
		Status:      c.Status,
		Scopes:      scopes,
		CreatedBy:   c.CreatedBy,
		LastUsedAt:  c.LastUsedAt,
		LastUsedIP:  c.LastUsedIP,
		RevokedAt:   c.RevokedAt,
		RevokedBy:   c.RevokedBy,
		CreatedAt:   c.CreatedAt,
		UpdatedAt:   c.UpdatedAt,
	}
}

// ClientCreated 创建/轮换响应：secret 仅此一次。
type ClientCreated struct {
	ClientView
	Secret string `json:"secret"`
}
