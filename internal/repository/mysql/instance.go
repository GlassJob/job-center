package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/GlassJob/job-center/internal/model"
	"github.com/GlassJob/job-center/internal/pkg/idgen"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type CredentialAuthRow struct {
	model.Credential
	ServiceCode   string `gorm:"column:service_code"` // 查询别名，对应 services.code
	ServiceStatus string `gorm:"column:service_status"`
}

func (s *Store) GetCredentialForAuth(ctx context.Context, credentialID string) (*CredentialAuthRow, error) {
	var row CredentialAuthRow
	err := s.db.WithContext(ctx).Table("service_credentials AS c").
		Select("c.*, svc.code AS service_code, svc.status AS service_status").
		Joins("INNER JOIN services AS svc ON svc.id = c.service_id").
		Where("c.id = ?", credentialID).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *Store) TouchCredentialUsed(ctx context.Context, id, ip string) {
	_ = s.db.WithContext(ctx).Model(&model.Credential{}).Where("id = ?", id).Updates(map[string]any{
		"last_used_at": time.Now().UTC(),
		"last_used_ip": ip,
	}).Error
}

func (s *Store) GetServiceByCode(ctx context.Context, code string) (*model.Service, error) {
	var svc model.Service
	err := s.db.WithContext(ctx).Where("code = ?", code).First(&svc).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &svc, nil
}

func (s *Store) GetInstanceByKey(ctx context.Context, instanceKey string) (*model.Instance, error) {
	var inst model.Instance
	err := s.db.WithContext(ctx).Where("instance_key = ?", instanceKey).First(&inst).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &inst, nil
}

func (s *Store) GetInstance(ctx context.Context, id string) (*model.Instance, error) {
	var inst model.Instance
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&inst).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &inst, nil
}

type RegisterInstanceInput struct {
	ServiceID       string
	InstanceKey     string
	SessionID       string
	Environment     string
	Hostname        string
	IPAddress       string
	ReportedIP      string
	PID             *uint
	Language        string
	RuntimeVersion  string
	SDKVersion      string
	ProtocolVersion string
	AppVersion      string
	GitCommit       string
	BuildTime       *time.Time
	StartedAt       time.Time
	Status          string
	MaxConcurrency  uint
	GatewayNodeID   string
	Metadata        map[string]any
}

// UpsertInstanceOnRegister 按 instance_key 插入或更新；每次注册 session_epoch+1。
func (s *Store) UpsertInstanceOnRegister(ctx context.Context, in RegisterInstanceInput) (*model.Instance, error) {
	now := time.Now().UTC()
	var out *model.Instance
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.Instance
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("instance_key = ?", in.InstanceKey).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			inst := model.Instance{
				ID:              idgen.New(),
				ServiceID:       in.ServiceID,
				InstanceKey:     in.InstanceKey,
				SessionID:       in.SessionID,
				SessionEpoch:    1,
				Environment:     in.Environment,
				Hostname:        in.Hostname,
				IPAddress:       in.IPAddress,
				ReportedIP:      in.ReportedIP,
				PID:             in.PID,
				Language:        in.Language,
				RuntimeVersion:  in.RuntimeVersion,
				SDKVersion:      in.SDKVersion,
				ProtocolVersion: in.ProtocolVersion,
				AppVersion:      in.AppVersion,
				GitCommit:       in.GitCommit,
				BuildTime:       in.BuildTime,
				StartedAt:       in.StartedAt,
				RegisteredAt:    now,
				ConnectedAt:     &now,
				LastHeartbeatAt: &now,
				Status:          in.Status,
				MaxConcurrency:  in.MaxConcurrency,
				RunningCount:    0,
				GatewayNodeID:   in.GatewayNodeID,
				Metadata:        in.Metadata,
			}
			if err := tx.Create(&inst).Error; err != nil {
				return err
			}
			out = &inst
			return nil
		}
		if err != nil {
			return err
		}
		if existing.ServiceID != in.ServiceID {
			return ErrConflict
		}
		status := in.Status
		if existing.Status == model.InstanceStatusDraining {
			status = model.InstanceStatusDraining
		}
		updates := map[string]any{
			"session_id":        in.SessionID,
			"session_epoch":     gorm.Expr("session_epoch + 1"),
			"environment":       in.Environment,
			"hostname":          in.Hostname,
			"ip_address":        in.IPAddress,
			"reported_ip":       in.ReportedIP,
			"pid":               in.PID,
			"language":          in.Language,
			"runtime_version":   in.RuntimeVersion,
			"sdk_version":       in.SDKVersion,
			"protocol_version":  in.ProtocolVersion,
			"app_version":       in.AppVersion,
			"git_commit":        in.GitCommit,
			"build_time":        in.BuildTime,
			"started_at":        in.StartedAt,
			"connected_at":      now,
			"disconnected_at":   nil,
			"last_heartbeat_at": now,
			"status":            status,
			"max_concurrency":   in.MaxConcurrency,
			"running_count":     0,
			"gateway_node_id":   in.GatewayNodeID,
		}
		if in.Metadata != nil {
			updates["metadata"] = in.Metadata
		}
		if err := tx.Model(&model.Instance{}).Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
			return err
		}
		var refreshed model.Instance
		if err := tx.Where("id = ?", existing.ID).First(&refreshed).Error; err != nil {
			return err
		}
		out = &refreshed
		return nil
	})
	return out, err
}

func (s *Store) ReplaceInstanceJobs(ctx context.Context, instanceID string, jobs []model.InstanceJob) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("instance_id = ?", instanceID).Delete(&model.InstanceJob{}).Error; err != nil {
			return err
		}
		if len(jobs) == 0 {
			return nil
		}
		return tx.Create(&jobs).Error
	})
}

func (s *Store) HeartbeatInstance(ctx context.Context, instanceID string, sessionEpoch uint64, running uint, maxConcurrency *uint, draining bool) (string, error) {
	now := time.Now().UTC()
	updates := map[string]any{
		"last_heartbeat_at": now,
		"running_count":     running,
	}
	if maxConcurrency != nil && *maxConcurrency > 0 {
		updates["max_concurrency"] = *maxConcurrency
	}
	if draining {
		updates["status"] = model.InstanceStatusDraining
	} else {
		updates["status"] = model.InstanceStatusOnline
	}
	res := s.db.WithContext(ctx).Model(&model.Instance{}).
		Where("id = ? AND session_epoch = ? AND status IN ?", instanceID, sessionEpoch,
			[]string{model.InstanceStatusOnline, model.InstanceStatusDraining, model.InstanceStatusRegistering}).
		Updates(updates)
	if res.Error != nil {
		return "", res.Error
	}
	if res.RowsAffected == 0 {
		return "", ErrNotFound
	}
	inst, err := s.GetInstance(ctx, instanceID)
	if err != nil {
		return "", err
	}
	return inst.Status, nil
}

// UpdateInstanceRegistration 在既有 session_epoch 上更新注册元数据（不升 epoch）。
func (s *Store) UpdateInstanceRegistration(ctx context.Context, instanceID string, sessionEpoch uint64, in RegisterInstanceInput) (*model.Instance, error) {
	now := time.Now().UTC()
	updates := map[string]any{
		"environment":       in.Environment,
		"hostname":          in.Hostname,
		"ip_address":        in.IPAddress,
		"reported_ip":       in.ReportedIP,
		"pid":               in.PID,
		"language":          in.Language,
		"runtime_version":   in.RuntimeVersion,
		"sdk_version":       in.SDKVersion,
		"protocol_version":  in.ProtocolVersion,
		"app_version":       in.AppVersion,
		"git_commit":        in.GitCommit,
		"build_time":        in.BuildTime,
		"started_at":        in.StartedAt,
		"connected_at":      now,
		"disconnected_at":   nil,
		"last_heartbeat_at": now,
		"status":            in.Status,
		"max_concurrency":   in.MaxConcurrency,
		"gateway_node_id":   in.GatewayNodeID,
	}
	res := s.db.WithContext(ctx).Model(&model.Instance{}).
		Where("id = ? AND session_epoch = ?", instanceID, sessionEpoch).Updates(updates)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return s.GetInstance(ctx, instanceID)
}

func (s *Store) MarkInstanceDisconnected(ctx context.Context, instanceID string, sessionEpoch uint64, status string) error {
	now := time.Now().UTC()
	res := s.db.WithContext(ctx).Model(&model.Instance{}).
		Where("id = ? AND session_epoch = ?", instanceID, sessionEpoch).
		Updates(map[string]any{
			"status":          status,
			"disconnected_at": now,
		})
	if res.Error != nil {
		return res.Error
	}
	return nil
}

func (s *Store) SetInstanceDraining(ctx context.Context, instanceID string, sessionEpoch uint64) error {
	now := time.Now().UTC()
	res := s.db.WithContext(ctx).Model(&model.Instance{}).
		Where("id = ? AND session_epoch = ?", instanceID, sessionEpoch).
		Updates(map[string]any{
			"status":            model.InstanceStatusDraining,
			"last_heartbeat_at": now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetJobByCode(ctx context.Context, serviceID, code string) (*model.Job, error) {
	var j model.Job
	err := s.db.WithContext(ctx).Where("service_id = ? AND code = ?", serviceID, code).First(&j).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

type RegisterJobMeta struct {
	Name                  string
	Description           string
	Language              string
	Handler               string
	HandlerVersion        string
	HandlerMajor          uint
	ParamsSchema          map[string]any
	SchemaHash            string
	DefaultTimeoutSeconds uint
}

// ApplyJobRegistration 写入/升级 Job，并维护 job_versions。upgradeContent=false 时仅 touch last_registered_at。
func (s *Store) ApplyJobRegistration(ctx context.Context, serviceID, code string, meta RegisterJobMeta, upgradeContent bool) (*model.Job, error) {
	now := time.Now().UTC()
	var out *model.Job
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.Job
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("service_id = ? AND code = ?", serviceID, code).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			timeout := meta.DefaultTimeoutSeconds
			if timeout == 0 {
				timeout = 300
			}
			j := model.Job{
				ID:                    idgen.New(),
				ServiceID:             serviceID,
				Code:                  code,
				Name:                  meta.Name,
				Description:           meta.Description,
				Enabled:               true,
				Language:              meta.Language,
				Handler:               meta.Handler,
				HandlerVersion:        meta.HandlerVersion,
				ParamsSchema:          meta.ParamsSchema,
				SchemaHash:            meta.SchemaHash,
				MetadataVersion:       1,
				IdempotencyMode:       model.IdempotencyModeUnknown,
				SupportsTimeout:       true,
				DefaultTimeoutSeconds: timeout,
				Capabilities:          map[string]any{},
				Status:                model.JobStatusActive,
				FirstRegisteredAt:     &now,
				LastRegisteredAt:      &now,
			}
			if err := tx.Create(&j).Error; err != nil {
				return err
			}
			if err := upsertJobVersionTx(tx, j.ID, meta); err != nil {
				return err
			}
			out = &j
			return nil
		}
		if err != nil {
			return err
		}

		updates := map[string]any{
			"last_registered_at": now,
			"status":             model.JobStatusActive,
		}
		if existing.FirstRegisteredAt == nil {
			updates["first_registered_at"] = now
		}
		if upgradeContent {
			updates["name"] = meta.Name
			if meta.Description != "" {
				updates["description"] = meta.Description
			}
			updates["language"] = meta.Language
			if meta.Handler != "" {
				updates["handler"] = meta.Handler
			}
			updates["handler_version"] = meta.HandlerVersion
			if meta.ParamsSchema == nil {
				updates["params_schema"] = nil
			} else {
				raw, mErr := json.Marshal(meta.ParamsSchema)
				if mErr != nil {
					return mErr
				}
				updates["params_schema"] = raw
			}
			updates["schema_hash"] = meta.SchemaHash
			updates["metadata_version"] = gorm.Expr("metadata_version + 1")
			if meta.DefaultTimeoutSeconds > 0 {
				updates["default_timeout_seconds"] = meta.DefaultTimeoutSeconds
			}
		}
		if err := tx.Model(&model.Job{}).Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
			return err
		}
		if upgradeContent {
			if err := upsertJobVersionTx(tx, existing.ID, meta); err != nil {
				return err
			}
		} else {
			_ = touchJobVersionTx(tx, existing.ID, meta)
		}
		var refreshed model.Job
		if err := tx.Where("id = ?", existing.ID).First(&refreshed).Error; err != nil {
			return err
		}
		out = &refreshed
		return nil
	})
	return out, err
}

func upsertJobVersionTx(tx *gorm.DB, jobID string, meta RegisterJobMeta) error {
	now := time.Now().UTC()
	var existing model.JobVersion
	err := tx.Where("job_id = ? AND handler_version = ? AND schema_hash = ?", jobID, meta.HandlerVersion, meta.SchemaHash).
		First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		v := model.JobVersion{
			ID:              idgen.New(),
			JobID:           jobID,
			HandlerVersion:  meta.HandlerVersion,
			HandlerMajor:    meta.HandlerMajor,
			SchemaHash:      meta.SchemaHash,
			ParamsSchema:    meta.ParamsSchema,
			IdempotencyMode: model.IdempotencyModeUnknown,
			FirstSeenAt:     now,
			LastSeenAt:      now,
		}
		return tx.Create(&v).Error
	}
	if err != nil {
		return err
	}
	return tx.Model(&model.JobVersion{}).Where("id = ?", existing.ID).Update("last_seen_at", now).Error
}

func touchJobVersionTx(tx *gorm.DB, jobID string, meta RegisterJobMeta) error {
	now := time.Now().UTC()
	return tx.Model(&model.JobVersion{}).
		Where("job_id = ? AND handler_version = ? AND schema_hash = ?", jobID, meta.HandlerVersion, meta.SchemaHash).
		Update("last_seen_at", now).Error
}
