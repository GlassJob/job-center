package model

import "time"

const (
	ServiceStatusActive   = "ACTIVE"
	ServiceStatusDisabled = "DISABLED"
)

type Service struct {
	ID          string    `json:"id" gorm:"column:id;primaryKey"`
	Code        string    `json:"code" gorm:"column:code"`
	Name        string    `json:"name" gorm:"column:name"`
	Description string    `json:"description,omitempty" gorm:"column:description"`
	Status      string    `json:"status" gorm:"column:status"`
	CreatedAt   time.Time `json:"created_at" gorm:"column:created_at"`
	UpdatedAt   time.Time `json:"updated_at" gorm:"column:updated_at"`
}

func (Service) TableName() string { return "services" }

type ServiceDetail struct {
	Service
	JobCount            int64 `json:"job_count" gorm:"column:job_count"`
	InstanceCount       int64 `json:"instance_count" gorm:"column:instance_count"`
	OnlineInstanceCount int64 `json:"online_instance_count" gorm:"column:online_instance_count"`
	CredentialCount     int64 `json:"credential_count" gorm:"column:credential_count"`
}
