package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/GlassJob/job-center/internal/model"
	"github.com/GlassJob/job-center/internal/pkg/auth"
	"github.com/GlassJob/job-center/internal/pkg/idgen"
	"github.com/GlassJob/job-center/internal/pkg/jobmeta"
	"github.com/GlassJob/job-center/internal/pkg/netutil"
	"github.com/GlassJob/job-center/internal/repository/mysql"
	"github.com/GlassJob/job-center/pkg/protocol"
)

var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrSessionStale = errors.New("session replaced")
)

type Executor struct {
	store  *mysql.Store
	pepper string
	nodeID string
	skew   time.Duration
	nonces *auth.NonceCache
}

func NewExecutor(store *mysql.Store, pepper, nodeID string, skew time.Duration) *Executor {
	if skew <= 0 {
		skew = 5 * time.Minute
	}
	return &Executor{
		store:  store,
		pepper: pepper,
		nodeID: nodeID,
		skew:   skew,
		nonces: auth.NewNonceCache(10 * time.Minute),
	}
}

// Session 连接内会话（auth 成功后持有）。
type Session struct {
	CredentialID    string
	ServiceID       string
	ServiceCode     string
	InstanceID      string
	InstanceKey     string
	SessionID       string
	SessionEpoch    uint64
	AllowedJobCodes *[]string
	Registered      bool
}

// AuthenticateHMAC WS auth.request：验签 + IP 白名单 + 开启会话骨架。
func (s *Executor) AuthenticateHMAC(ctx context.Context, req protocol.AuthRequest, peerIP string) (*Session, *protocol.AuthResponse, error) {
	method := strings.TrimSpace(strings.ToLower(req.Method))
	if method == "" {
		method = "hmac"
	}
	if method != "hmac" {
		return nil, &protocol.AuthResponse{OK: false, Error: &protocol.ErrorBody{Code: protocol.ErrProtocolUnsupported, Message: "unsupported auth method"}}, nil
	}
	code := strings.TrimSpace(req.Code)
	credID := strings.TrimSpace(req.CredentialID)
	instKey := strings.TrimSpace(req.InstanceKey)
	if code == "" || credID == "" || instKey == "" || req.Timestamp == "" || req.Nonce == "" || req.Signature == "" {
		return nil, &protocol.AuthResponse{OK: false, Error: &protocol.ErrorBody{Code: protocol.ErrAuthInvalidCredential, Message: "missing auth fields"}}, nil
	}
	ts, err := strconv.ParseInt(req.Timestamp, 10, 64)
	if err != nil {
		return nil, &protocol.AuthResponse{OK: false, Error: &protocol.ErrorBody{Code: protocol.ErrAuthInvalidCredential, Message: "bad timestamp"}}, nil
	}
	diff := time.Now().Unix() - ts
	if diff < 0 {
		diff = -diff
	}
	if diff > int64(s.skew/time.Second) {
		return nil, &protocol.AuthResponse{OK: false, Error: &protocol.ErrorBody{Code: protocol.ErrAuthInvalidCredential, Message: "timestamp skew"}}, nil
	}
	if !s.nonces.Use(credID, req.Nonce) {
		return nil, &protocol.AuthResponse{OK: false, Error: &protocol.ErrorBody{Code: protocol.ErrAuthInvalidCredential, Message: "nonce replay"}}, nil
	}

	ver, ok := pickProtocolVersion(req.ProtocolVersions)
	if !ok {
		return nil, &protocol.AuthResponse{OK: false, Error: &protocol.ErrorBody{Code: protocol.ErrProtocolUnsupported, Message: "no common protocol version"}}, nil
	}

	row, err := s.store.GetCredentialForAuth(ctx, credID)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, &protocol.AuthResponse{OK: false, Error: &protocol.ErrorBody{Code: protocol.ErrAuthInvalidCredential, Message: "invalid credential"}}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if row.ServiceCode != code ||
		(row.Status != model.CredentialStatusActive && row.Status != model.CredentialStatusRotating) {
		return nil, &protocol.AuthResponse{OK: false, Error: &protocol.ErrorBody{Code: protocol.ErrAuthInvalidCredential, Message: "invalid credential"}}, nil
	}
	if row.ExpiresAt != nil && !row.ExpiresAt.After(time.Now().UTC()) {
		return nil, &protocol.AuthResponse{OK: false, Error: &protocol.ErrorBody{Code: protocol.ErrAuthInvalidCredential, Message: "invalid credential"}}, nil
	}
	if row.ServiceStatus != model.ServiceStatusActive {
		return nil, &protocol.AuthResponse{OK: false, Error: &protocol.ErrorBody{Code: protocol.ErrAuthServiceDisabled, Message: "service is disabled"}}, nil
	}
	if !netutil.IPAllowed(row.AllowedIPs, peerIP) {
		return nil, &protocol.AuthResponse{OK: false, Error: &protocol.ErrorBody{Code: protocol.ErrAuthIPDenied, Message: "ip not allowed"}}, nil
	}
	if len(row.SecretEnc) == 0 {
		return nil, &protocol.AuthResponse{OK: false, Error: &protocol.ErrorBody{Code: protocol.ErrAuthInvalidCredential, Message: "credential not ready for hmac"}}, nil
	}
	secret, err := auth.DecryptSecret(s.pepper, row.SecretEnc)
	if err != nil {
		return nil, nil, err
	}
	canon := auth.ExecutorAuthCanonical(code, credID, instKey, req.Timestamp, req.Nonce)
	if !auth.VerifySignature(secret, canon, req.Signature) {
		return nil, &protocol.AuthResponse{OK: false, Error: &protocol.ErrorBody{Code: protocol.ErrAuthInvalidCredential, Message: "invalid credential"}}, nil
	}
	s.store.TouchCredentialUsed(ctx, row.ID, peerIP)

	lang := strings.TrimSpace(req.SDK.Language)
	if lang == "" {
		lang = "unknown"
	}
	sessionID := idgen.New()
	now := time.Now().UTC()
	inst, err := s.store.UpsertInstanceOnRegister(ctx, mysql.RegisterInstanceInput{
		ServiceID:       row.ServiceID,
		InstanceKey:     instKey,
		SessionID:       sessionID,
		Environment:     "dev",
		Hostname:        instKey,
		IPAddress:       peerIP,
		Language:        lang,
		SDKVersion:      strings.TrimSpace(req.SDK.Version),
		ProtocolVersion: ver,
		StartedAt:       now,
		Status:          model.InstanceStatusRegistering,
		MaxConcurrency:  1,
		GatewayNodeID:   s.nodeID,
	})
	if errors.Is(err, mysql.ErrConflict) {
		return nil, &protocol.AuthResponse{OK: false, Error: &protocol.ErrorBody{Code: protocol.ErrAuthInvalidCredential, Message: "instance_key belongs to another service"}}, nil
	}
	if err != nil {
		return nil, nil, err
	}

	sess := &Session{
		CredentialID:    row.ID,
		ServiceID:       row.ServiceID,
		ServiceCode:     row.ServiceCode,
		InstanceID:      inst.ID,
		InstanceKey:     inst.InstanceKey,
		SessionID:       inst.SessionID,
		SessionEpoch:    inst.SessionEpoch,
		AllowedJobCodes: row.AllowedJobCodes,
	}
	return sess, &protocol.AuthResponse{
		OK:              true,
		SessionID:       sess.SessionID,
		SessionEpoch:    int64(sess.SessionEpoch),
		InstanceID:      sess.InstanceID,
		ProtocolVersion: ver,
		ServerTime:      protocol.FormatTime(time.Now()),
	}, nil
}

func pickProtocolVersion(versions []string) (string, bool) {
	for _, v := range versions {
		if v == protocol.Version || v == "1.1" || v == "1.0" || v == "1" {
			return protocol.Version, true
		}
	}
	return "", false
}

func (s *Executor) RegisterWithSession(ctx context.Context, sess *Session, req protocol.RegisterRequest, peerIP string, createSession bool) (*protocol.RegisterResponse, error) {
	req.ServiceCode = strings.TrimSpace(req.ServiceCode)
	req.InstanceKey = strings.TrimSpace(req.InstanceKey)
	if req.ServiceCode == "" || req.InstanceKey == "" {
		return &protocol.RegisterResponse{OK: false, Error: &protocol.ErrorBody{Code: "INVALID_INPUT", Message: "code and instance_key required"}}, nil
	}
	if sess.ServiceCode != "" && req.ServiceCode != sess.ServiceCode {
		return &protocol.RegisterResponse{OK: false, Error: &protocol.ErrorBody{Code: "REGISTER_SERVICE_MISMATCH", Message: "code differs from authenticated service"}}, nil
	}
	if sess.InstanceKey != "" && req.InstanceKey != sess.InstanceKey {
		return &protocol.RegisterResponse{OK: false, Error: &protocol.ErrorBody{Code: "REGISTER_INSTANCE_MISMATCH", Message: "instance_key differs from authenticated instance"}}, nil
	}
	if len(req.InstanceKey) > 128 {
		return &protocol.RegisterResponse{OK: false, Error: &protocol.ErrorBody{Code: "INVALID_INPUT", Message: "instance_key too long"}}, nil
	}
	if strings.TrimSpace(req.Runtime.Language) == "" {
		return &protocol.RegisterResponse{OK: false, Error: &protocol.ErrorBody{Code: "INVALID_INPUT", Message: "runtime.language required"}}, nil
	}

	ack := &protocol.RegisterResponse{OK: true, InstanceStatus: model.InstanceStatusOnline}
	type prepared struct {
		decl    protocol.JobDecl
		ver     jobmeta.Version
		schema  map[string]any
		hash    string
		maxConc *uint
		timeout uint
		handler string
		cron    *string
	}
	seen := map[string]bool{}
	var jobs []prepared
	var allowed []string
	if sess.AllowedJobCodes != nil {
		allowed = *sess.AllowedJobCodes
	}

	for _, j := range req.Jobs {
		code := strings.TrimSpace(j.Code)
		reject := func(codeName, msg string) {
			ack.RejectedJobs = append(ack.RejectedJobs, protocol.JobIssue{JobCode: code, Code: codeName, Message: msg})
		}
		if code == "" {
			reject("INVALID_JOB_CODE", "job_code required")
			continue
		}
		if seen[code] {
			reject("DUPLICATE_JOB", "job_code declared more than once")
			continue
		}
		seen[code] = true
		if !jobmeta.JobCodeRe.MatchString(code) {
			reject("INVALID_JOB_CODE", "invalid job_code")
			continue
		}
		if !jobmeta.JobAllowed(allowed, code) {
			reject("JOB_NOT_ALLOWED", "credential is not allowed to register this job_code")
			continue
		}
		ver, err := jobmeta.ParseVersion(strings.TrimSpace(j.HandlerVersion))
		if err != nil {
			reject("INVALID_VERSION", err.Error())
			continue
		}
		canon, hash, err := jobmeta.NormalizeSchema(j.ParamsSchema)
		if err != nil {
			reject("INVALID_SCHEMA", err.Error())
			continue
		}
		var schemaObj map[string]any
		if string(canon) != jobmeta.EmptySchemaJSON {
			_ = json.Unmarshal(canon, &schemaObj)
		}
		name := strings.TrimSpace(j.Name)
		if name == "" {
			name = code
		}
		j.Name = name
		var maxPtr *uint
		if j.MaxConcurrency > 0 {
			v := uint(j.MaxConcurrency)
			maxPtr = &v
		}
		timeout := uint(0)
		if j.TimeoutSeconds > 0 {
			timeout = uint(j.TimeoutSeconds)
		}
		handler := strings.TrimSpace(j.Handler)
		if handler == "" {
			handler = code
		}
		var cron *string
		if j.DefaultCron != nil {
			c := strings.TrimSpace(*j.DefaultCron)
			if c == "" {
				cron = nil
			} else if err := jobmeta.ValidateCron(c); err != nil {
				ack.Warnings = append(ack.Warnings, protocol.JobIssue{JobCode: code, Code: "INVALID_DEFAULT_CRON", Message: err.Error()})
			} else {
				cron = &c
			}
		}
		jobs = append(jobs, prepared{
			decl: j, ver: ver, schema: schemaObj, hash: hash, maxConc: maxPtr, timeout: timeout, handler: handler, cron: cron,
		})
	}
	sort.Slice(jobs, func(i, k int) bool { return jobs[i].decl.Code < jobs[k].decl.Code })

	env := strings.TrimSpace(req.Environment)
	if env == "" {
		env = "dev"
	}
	maxConc := uint(req.Capacity.MaxConcurrency)
	if maxConc < 1 {
		maxConc = 1
	}
	hostname := req.InstanceKey
	var reportedIP string
	var pid *uint
	if req.Host != nil {
		if strings.TrimSpace(req.Host.Hostname) != "" {
			hostname = req.Host.Hostname
		}
		reportedIP = strings.TrimSpace(req.Host.IP)
		if req.Host.PID > 0 {
			v := uint(req.Host.PID)
			pid = &v
		}
	}
	startedAt := time.Now().UTC()
	if t := parseRFC3339(req.StartedAt); t != nil {
		startedAt = *t
	}

	var inst *model.Instance
	var err error
	if createSession || sess.InstanceID == "" {
		sessionID := idgen.New()
		inst, err = s.store.UpsertInstanceOnRegister(ctx, mysql.RegisterInstanceInput{
			ServiceID:       sess.ServiceID,
			InstanceKey:     req.InstanceKey,
			SessionID:       sessionID,
			Environment:     env,
			Hostname:        hostname,
			IPAddress:       peerIP,
			ReportedIP:      reportedIP,
			PID:             pid,
			Language:        strings.TrimSpace(req.Runtime.Language),
			RuntimeVersion:  strings.TrimSpace(req.Runtime.Version),
			SDKVersion:      strings.TrimSpace(req.SDKVersion),
			ProtocolVersion: protocol.Version,
			AppVersion:      strings.TrimSpace(req.Deployment.Version),
			GitCommit:       strings.TrimSpace(req.Deployment.GitCommit),
			BuildTime:       parseRFC3339(req.Deployment.BuildTime),
			StartedAt:       startedAt,
			Status:          model.InstanceStatusOnline,
			MaxConcurrency:  maxConc,
			GatewayNodeID:   s.nodeID,
		})
	} else {
		// 已 auth：同 epoch 更新元数据，不升 epoch
		inst, err = s.store.UpdateInstanceRegistration(ctx, sess.InstanceID, sess.SessionEpoch, mysql.RegisterInstanceInput{
			Environment:     env,
			Hostname:        hostname,
			IPAddress:       peerIP,
			ReportedIP:      reportedIP,
			PID:             pid,
			Language:        strings.TrimSpace(req.Runtime.Language),
			RuntimeVersion:  strings.TrimSpace(req.Runtime.Version),
			SDKVersion:      strings.TrimSpace(req.SDKVersion),
			ProtocolVersion: protocol.Version,
			AppVersion:      strings.TrimSpace(req.Deployment.Version),
			GitCommit:       strings.TrimSpace(req.Deployment.GitCommit),
			BuildTime:       parseRFC3339(req.Deployment.BuildTime),
			StartedAt:       startedAt,
			Status:          model.InstanceStatusOnline,
			MaxConcurrency:  maxConc,
			GatewayNodeID:   s.nodeID,
		})
	}
	if errors.Is(err, mysql.ErrConflict) {
		return &protocol.RegisterResponse{OK: false, Error: &protocol.ErrorBody{Code: "INSTANCE_SERVICE_MISMATCH", Message: "instance_key belongs to another service"}}, nil
	}
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrSessionStale
	}
	if err != nil {
		return nil, err
	}

	accepted := make([]protocol.AcceptedJob, 0, len(jobs))
	ijobs := make([]model.InstanceJob, 0, len(jobs))
	for _, j := range jobs {
		upgrade := true
		wasNew := false
		existing, gerr := s.store.GetJobByCode(ctx, sess.ServiceID, j.decl.Code)
		if errors.Is(gerr, mysql.ErrNotFound) {
			wasNew = true
		} else if gerr == nil && existing.HandlerVersion != "" {
			cur, perr := jobmeta.ParseVersion(existing.HandlerVersion)
			if perr == nil && j.ver.Compare(cur) < 0 {
				upgrade = false
			}
		} else if gerr != nil {
			return nil, gerr
		}
		meta := mysql.RegisterJobMeta{
			Name:                  j.decl.Name,
			Description:           strings.TrimSpace(j.decl.Description),
			Language:              strings.TrimSpace(req.Runtime.Language),
			Handler:               j.handler,
			HandlerVersion:        j.ver.Raw,
			HandlerMajor:          uint(j.ver.Major),
			ParamsSchema:          j.schema,
			SchemaHash:            j.hash,
			DefaultTimeoutSeconds: j.timeout,
		}
		job, err := s.store.ApplyJobRegistration(ctx, sess.ServiceID, j.decl.Code, meta, upgrade)
		if err != nil {
			return nil, err
		}
		accepted = append(accepted, protocol.AcceptedJob{JobCode: j.decl.Code, JobID: job.ID})
		ijobs = append(ijobs, model.InstanceJob{
			ID:             idgen.New(),
			InstanceID:     inst.ID,
			JobID:          job.ID,
			HandlerVersion: j.ver.Raw,
			HandlerMajor:   uint(j.ver.Major),
			SchemaHash:     j.hash,
			MaxConcurrency: j.maxConc,
			RegisteredAt:   time.Now().UTC(),
		})
		warn, err := s.ensureDefaultSchedule(ctx, j.decl.Code, job.ID, j.cron, j.timeout, wasNew)
		if err != nil {
			return nil, err
		}
		if warn != nil {
			ack.Warnings = append(ack.Warnings, *warn)
		}
	}
	if err := s.store.ReplaceInstanceJobs(ctx, inst.ID, ijobs); err != nil {
		return nil, err
	}

	sess.InstanceID = inst.ID
	sess.SessionID = inst.SessionID
	sess.SessionEpoch = inst.SessionEpoch
	sess.Registered = true

	ack.InstanceID = inst.ID
	ack.InstanceKey = inst.InstanceKey
	ack.SessionID = inst.SessionID
	ack.SessionEpoch = int64(inst.SessionEpoch)
	ack.InstanceStatus = inst.Status
	ack.AcceptedJobs = accepted
	return ack, nil
}

func (s *Executor) ensureDefaultSchedule(ctx context.Context, jobCode, jobID string, cron *string, timeout uint, _ bool) (*protocol.JobIssue, error) {
	existing, err := s.store.GetScheduleByJobAndCode(ctx, jobID, "default")
	if errors.Is(err, mysql.ErrNotFound) {
		if cron == nil {
			return nil, nil
		}
		to := timeout
		if to == 0 {
			to = 300
		}
		sch := &model.Schedule{
			ID:             idgen.New(),
			JobID:          jobID,
			Code:           "default",
			Name:           "default",
			Enabled:        true,
			ManualEnabled:  true,
			CronExpression: *cron,
			Timezone:       "UTC",
			TimeoutSeconds: to,
			ConfigSource:   model.ScheduleConfigSourceDefault,
			UpdatedBy:      "executor-register",
		}
		if err := s.store.CreateSchedule(ctx, sch); err != nil {
			if errors.Is(err, mysql.ErrConflict) {
				return nil, nil
			}
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if existing.ConfigSource == model.ScheduleConfigSourceAdmin {
		return nil, nil
	}
	if cron != nil && existing.CronExpression != "" && existing.CronExpression != *cron {
		return &protocol.JobIssue{
			JobCode: jobCode,
			Code:    "DEFAULT_CRON_CONFLICT",
			Message: fmt.Sprintf("schedule default cron in db=%q reported=%q; not overwritten", existing.CronExpression, *cron),
		}, nil
	}
	return nil, nil
}

func (s *Executor) HeartbeatWithSession(ctx context.Context, sess *Session, req protocol.HeartbeatRequest) (*protocol.HeartbeatResponse, error) {
	draining := strings.EqualFold(req.Status, model.InstanceStatusDraining)
	var maxPtr *uint
	if req.MaxConcurrency != nil && *req.MaxConcurrency > 0 {
		v := uint(*req.MaxConcurrency)
		maxPtr = &v
	}
	running := req.Running
	if running < 0 {
		running = 0
	}
	status, err := s.store.HeartbeatInstance(ctx, sess.InstanceID, sess.SessionEpoch, uint(running), maxPtr, draining)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, NotFoundf("实例未注册")
	}
	if err != nil {
		return nil, err
	}
	return &protocol.HeartbeatResponse{
		ServerTime:     protocol.FormatTime(time.Now()),
		InstanceStatus: status,
	}, nil
}

func (s *Executor) UnregisterWithSession(ctx context.Context, sess *Session) error {
	if err := s.store.SetInstanceDraining(ctx, sess.InstanceID, sess.SessionEpoch); err != nil {
		if errors.Is(err, mysql.ErrNotFound) {
			return ErrSessionStale
		}
		return err
	}
	return nil
}

func (s *Executor) Disconnect(ctx context.Context, sess *Session) error {
	if sess == nil || sess.InstanceID == "" {
		return nil
	}
	if err := s.store.MarkInstanceDisconnected(ctx, sess.InstanceID, sess.SessionEpoch, model.InstanceStatusOffline); err != nil {
		return err
	}
	return nil
}

func parseRFC3339(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t, err = time.Parse(time.RFC3339, raw)
	}
	if err != nil {
		return nil
	}
	t = t.UTC()
	return &t
}
