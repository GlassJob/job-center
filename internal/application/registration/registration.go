// Package registration 处理执行器认证、服务/实例/任务注册、心跳、下线（任务书第四章）。
// 规则要点：
//   - 每次认证成功 session_epoch+1；所有会话内写操作带 `WHERE session_epoch=?`（会话 fencing）。
//   - 注册只写任务定义与实例能力；调度配置仅对新任务插入默认值，永不覆盖（任务书 4.3）。
//   - 任务定义内容只在上报版本 ≥ 已记录版本时更新（滚动发布中旧实例不会回退定义）。
package registration

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"time"

	"github.com/GlassJob/job-center/internal/application/apperr"
	"github.com/GlassJob/job-center/internal/domain/execution"
	"github.com/GlassJob/job-center/internal/domain/instance"
	"github.com/GlassJob/job-center/internal/domain/task"
	"github.com/GlassJob/job-center/internal/infrastructure/security"
	"github.com/GlassJob/job-center/internal/protocol"
	repo "github.com/GlassJob/job-center/internal/repository/mysql"
)

var (
	// ErrSessionStale 本连接的 session_epoch 已被新连接取代。
	ErrSessionStale = apperr.New(apperr.KindConflict, protocol.ErrSessionReplaced, "session replaced by a newer connection")
	// ErrNotRegistered 实例未处于可接收心跳的状态（需要重新 service.register）。
	ErrNotRegistered = apperr.New(apperr.KindInvalid, protocol.ErrInstanceNotRegistered, "instance is not registered; send service.register")
)

type Service struct {
	store     *repo.Store
	pepper    string
	nodeID    string
	lostGrace time.Duration
	log       *slog.Logger
}

func New(store *repo.Store, pepper, nodeID string, lostGrace time.Duration, log *slog.Logger) *Service {
	return &Service{store: store, pepper: pepper, nodeID: nodeID, lostGrace: lostGrace, log: log}
}

// Session 认证成功后的会话（连接对象持有）。
type Session struct {
	ServiceID        int64
	ServiceCode      string
	InstancePK       int64
	InstanceID       string
	Epoch            int64
	KeyID            string
	AllowedTaskCodes []string
	Registered       bool
}

type AuthInput struct {
	Request         protocol.AuthRequest
	PeerIP          string
	ConnectionID    string
	ProtocolVersion string
}

func authErr(code, msg string) *apperr.Error { return apperr.New(apperr.KindUnauthenticated, code, msg) }

// Authenticate 校验凭据并开启新会话（session_epoch+1）。
func (s *Service) Authenticate(ctx context.Context, in AuthInput) (*Session, error) {
	r := in.Request
	keyID, secret, err := security.ParseCredential(r.Credential)
	if err != nil {
		return nil, authErr(protocol.ErrAuthInvalidCredential, "invalid credential")
	}
	cred, err := s.store.Q().GetCredentialForAuth(ctx, keyID)
	if errors.Is(err, repo.ErrNotFound) {
		return nil, authErr(protocol.ErrAuthInvalidCredential, "invalid credential")
	}
	if err != nil {
		return nil, apperr.Storage(err)
	}
	// 统一错误信息，避免泄露 key 是否存在/属于哪个服务。
	if !security.VerifySecret(s.pepper, secret, cred.SecretHash) || cred.ServiceCode != r.ServiceCode ||
		(cred.Status != "ACTIVE" && cred.Status != "ROTATING") || cred.Expired {
		return nil, authErr(protocol.ErrAuthInvalidCredential, "invalid credential")
	}
	if cred.ServiceStatus != "ACTIVE" {
		return nil, authErr(protocol.ErrAuthServiceDisabled, "service is disabled")
	}
	sess := &Session{ServiceID: cred.ServiceID, ServiceCode: cred.ServiceCode, InstanceID: r.InstanceID, KeyID: keyID, AllowedTaskCodes: cred.AllowedTaskCodes}
	info := repo.SessionInfo{InstanceID: r.InstanceID, ServiceID: cred.ServiceID, PeerIP: in.PeerIP, NodeID: s.nodeID, ConnectionID: in.ConnectionID,
		Language: r.SDK.Language, SDKVersion: r.SDK.Version, ProtocolVersion: in.ProtocolVersion}
	err = s.withRetry(ctx, func(q *repo.Queries) error {
		l, err := q.LockInstance(ctx, r.InstanceID)
		if errors.Is(err, repo.ErrNotFound) {
			pk, err := q.InsertInstance(ctx, info)
			if err != nil {
				return err
			}
			sess.InstancePK, sess.Epoch = pk, 1
			return nil
		}
		if err != nil {
			return err
		}
		if l.ServiceID != cred.ServiceID {
			return authErr(protocol.ErrAuthInvalidCredential, "instance_id belongs to another service")
		}
		if err := q.NewSession(ctx, l.PK, info); err != nil {
			return err
		}
		sess.InstancePK, sess.Epoch = l.PK, l.Epoch+1
		return nil
	})
	if err != nil {
		return nil, toAppErr(err)
	}
	if err := s.store.Q().TouchCredential(ctx, cred.ID, in.PeerIP); err != nil {
		s.log.Warn("touch credential failed", "key_id", keyID, "err", err)
	}
	return sess, nil
}

// withRetry 事务 + 对唯一键冲突（并发首次插入）额外重试。
func (s *Service) withRetry(ctx context.Context, fn func(q *repo.Queries) error) error {
	var err error
	for i := 0; i < 3; i++ {
		err = s.store.InTx(ctx, fn)
		if err == nil || !(repo.IsDuplicate(err) || repo.IsRetryable(err)) {
			return err
		}
	}
	return err
}

func toAppErr(err error) error {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae
	}
	return apperr.Storage(err)
}

func parseTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil
	}
	t = t.UTC()
	return &t
}

type preparedTask struct {
	decl  protocol.TaskDecl
	input repo.DefinitionInput
	ver   task.Version
}

// Register 处理 service.register。返回的 ack.OK=false 表示整体拒绝（例如服务/实例不匹配）。
func (s *Service) Register(ctx context.Context, sess *Session, d protocol.RegisterData) (*protocol.RegisterAck, error) {
	if d.ServiceCode != sess.ServiceCode {
		return &protocol.RegisterAck{OK: false, Error: &protocol.ErrorBody{Code: "REGISTER_SERVICE_MISMATCH", Message: "service_code differs from authenticated service"}}, nil
	}
	if d.InstanceID != sess.InstanceID {
		return &protocol.RegisterAck{OK: false, Error: &protocol.ErrorBody{Code: "REGISTER_INSTANCE_MISMATCH", Message: "instance_id differs from authenticated instance"}}, nil
	}
	ack := &protocol.RegisterAck{OK: true, InstanceStatus: string(instance.Online)}
	seen := map[string]bool{}
	var tasks []preparedTask
	for _, t := range d.Tasks {
		reject := func(code, msg string) { ack.RejectedTasks = append(ack.RejectedTasks, protocol.TaskIssue{TaskCode: t.TaskCode, Code: code, Message: msg}) }
		if seen[t.TaskCode] {
			reject(protocol.ErrRegisterDuplicateTask, "task_code declared more than once")
			continue
		}
		seen[t.TaskCode] = true
		if !task.TaskCodeRe.MatchString(t.TaskCode) {
			reject(protocol.ErrProtocolInvalidMessage, "invalid task_code")
			continue
		}
		if !task.TaskAllowed(sess.AllowedTaskCodes, t.TaskCode) {
			reject(protocol.ErrRegisterTaskNotAllowed, "credential is not allowed to register this task_code")
			continue
		}
		v, err := task.ParseVersion(t.HandlerVersion)
		if err != nil {
			reject(protocol.ErrRegisterInvalidVersion, err.Error())
			continue
		}
		canon, hash, err := task.NormalizeSchema(t.InputSchema)
		if err != nil {
			reject(protocol.ErrRegisterInvalidSchema, err.Error())
			continue
		}
		if len(t.InputSchema) == 0 || string(t.InputSchema) == "null" {
			canon = nil // 未声明 schema：落库为 NULL，hash 仍为 {} 的 hash
		}
		cronExpr := t.DefaultCron
		if cronExpr != nil && *cronExpr == "" {
			cronExpr = nil
		}
		if cronExpr != nil {
			if _, err := task.ValidateCron(*cronExpr); err != nil {
				ack.Warnings = append(ack.Warnings, protocol.TaskIssue{TaskCode: t.TaskCode, Code: protocol.ErrRegisterInvalidCron, Message: err.Error()})
				cronExpr = nil
			}
		}
		tasks = append(tasks, preparedTask{decl: t, ver: v, input: repo.DefinitionInput{
			ServiceID: sess.ServiceID, TaskCode: t.TaskCode, TaskName: t.TaskName, Description: t.Description,
			HandlerVersion: t.HandlerVersion, HandlerMajor: v.Major, InputSchema: canon, SchemaHash: hash,
			DefaultCron: cronExpr, TimeoutSeconds: t.TimeoutSeconds, MaxConcurrency: t.MaxConcurrency}})
	}
	// 固定加锁顺序，避免多实例并发注册死锁。
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].decl.TaskCode < tasks[j].decl.TaskCode })

	env := d.Environment
	if env == "" {
		env = "dev"
	}
	reg := repo.RegistrationInfo{Environment: env, Language: d.Runtime.Language, RuntimeVersion: d.Runtime.Version,
		AppVersion: d.Deployment.Version, GitCommit: d.Deployment.GitCommit, BuildTime: parseTime(d.Deployment.BuildTime),
		StartedAt: parseTime(d.StartedAt), MaxConcurrency: d.Capacity.MaxConcurrency, Labels: d.Labels, Status: string(instance.Online)}
	if d.Host != nil {
		reg.Hostname, reg.ReportedIP, reg.PID = d.Host.Hostname, d.Host.IP, d.Host.PID
	}
	if reg.MaxConcurrency < 1 {
		reg.MaxConcurrency = 1
	}

	var accepted []protocol.AcceptedTask
	var newSchedules int
	err := s.withRetry(ctx, func(q *repo.Queries) error {
		accepted, newSchedules = nil, 0
		l, err := q.LockInstanceByPK(ctx, sess.InstancePK)
		if err != nil {
			return err
		}
		if l.Epoch != sess.Epoch {
			return ErrSessionStale
		}
		if l.Status == string(instance.Draining) {
			reg.Status = string(instance.Draining)
		}
		if _, err := q.UpdateRegistration(ctx, sess.InstancePK, sess.Epoch, reg); err != nil {
			return err
		}
		its := make([]repo.InstanceTask, 0, len(tasks))
		for _, t := range tasks {
			id, err := s.upsertDefinition(ctx, q, t)
			if err != nil {
				return err
			}
			created, err := q.EnsureDefaultSchedule(ctx, id, t.input.DefaultCron, t.input.TimeoutSeconds, t.input.MaxConcurrency)
			if err != nil {
				return err
			}
			if created {
				newSchedules++
			}
			its = append(its, repo.InstanceTask{DefinitionID: id, HandlerVersion: t.input.HandlerVersion, HandlerMajor: t.ver.Major,
				SchemaHash: t.input.SchemaHash, MaxConcurrency: t.input.MaxConcurrency})
			accepted = append(accepted, protocol.AcceptedTask{TaskCode: t.decl.TaskCode, TaskID: id})
		}
		return q.ReplaceInstanceTasks(ctx, sess.InstancePK, its)
	})
	if err != nil {
		return nil, toAppErr(err)
	}
	ack.AcceptedTasks = accepted
	ack.InstanceStatus = reg.Status
	sess.Registered = true

	for _, ra := range d.RunningAttempts {
		ack.Attempts = append(ack.Attempts, s.reconcile(ctx, sess, ra))
	}
	s.log.Info("instance registered", "service_code", sess.ServiceCode, "instance_id", sess.InstanceID, "session_epoch", sess.Epoch,
		"app_version", d.Deployment.Version, "git_commit", d.Deployment.GitCommit, "accepted", len(accepted), "rejected", len(ack.RejectedTasks),
		"new_schedules", newSchedules, "warnings", len(ack.Warnings))
	return ack, nil
}

func (s *Service) upsertDefinition(ctx context.Context, q *repo.Queries, t preparedTask) (int64, error) {
	existing, err := q.LockDefinition(ctx, t.input.ServiceID, t.input.TaskCode)
	var id int64
	switch {
	case errors.Is(err, repo.ErrNotFound):
		if id, err = q.InsertDefinition(ctx, t.input); err != nil {
			return 0, err
		}
	case err != nil:
		return 0, err
	default:
		id = existing.ID
		cur, perr := task.ParseVersion(existing.HandlerVersion)
		if perr != nil || t.ver.Compare(cur) >= 0 {
			err = q.UpdateDefinitionContent(ctx, id, t.input)
		} else {
			err = q.TouchDefinition(ctx, id)
		}
		if err != nil {
			return 0, err
		}
	}
	return id, q.UpsertDefinitionVersion(ctx, id, t.input)
}

// reconcile 重连时对 SDK 上报的运行中 Attempt 做只读裁决（续租在 P2 实现）。
func (s *Service) reconcile(ctx context.Context, sess *Session, ra protocol.RunningAttempt) protocol.AttemptDecision {
	snap, err := s.store.Q().GetAttemptSnapshot(ctx, ra.AttemptID)
	in := execution.ReconcileInput{LostGrace: s.lostGrace, LocalState: ra.LocalState}
	switch {
	case errors.Is(err, repo.ErrNotFound):
	case err != nil:
		s.log.Warn("reconcile lookup failed", "attempt_id", ra.AttemptID, "err", err)
		return protocol.AttemptDecision{AttemptID: ra.AttemptID, Decision: execution.DecisionContinue, Reason: protocol.ErrStorageUnavailable}
	default:
		in.Found, in.Status, in.IsCurrent, in.LostAt, in.Now = true, snap.Status, snap.IsCurrent, snap.LostAt, snap.DBNow
		in.LeaseMatches = snap.LeaseToken != "" && snap.LeaseToken == ra.LeaseToken
		in.OwnedByCaller = snap.InstanceID == sess.InstanceID
	}
	dec, reason := execution.Decide(in)
	return protocol.AttemptDecision{AttemptID: ra.AttemptID, Decision: dec, Reason: reason}
}

// Heartbeat 处理 service.heartbeat。
func (s *Service) Heartbeat(ctx context.Context, sess *Session, hb protocol.Heartbeat) (*protocol.HeartbeatAck, error) {
	q := s.store.Q()
	n, err := q.Heartbeat(ctx, sess.InstancePK, sess.Epoch, hb.Running, hb.MaxConcurrency, hb.Status == string(instance.Draining))
	if err != nil {
		return nil, apperr.Storage(err)
	}
	epoch, status, err := q.InstanceState(ctx, sess.InstancePK)
	if err != nil {
		return nil, apperr.Storage(err)
	}
	if epoch != sess.Epoch {
		return nil, ErrSessionStale
	}
	if n == 0 {
		sess.Registered = false
		return nil, ErrNotRegistered
	}
	return &protocol.HeartbeatAck{ServerTime: protocol.FormatTime(time.Now()), InstanceStatus: status}, nil
}

// Unregister service.unregister → DRAINING；连接关闭后转 OFFLINE。
func (s *Service) Unregister(ctx context.Context, sess *Session, u protocol.Unregister) error {
	if _, err := s.store.Q().SetDraining(ctx, sess.InstancePK, sess.Epoch); err != nil {
		return apperr.Storage(err)
	}
	s.log.Info("instance draining", "instance_id", sess.InstanceID, "reason", u.Reason)
	return nil
}

// Disconnect 连接关闭：仅影响仍属于本会话的实例记录。
func (s *Service) Disconnect(ctx context.Context, sess *Session, normalClose bool) (string, error) {
	q := s.store.Q()
	epoch, status, err := q.InstanceState(ctx, sess.InstancePK)
	if err != nil {
		return "", err
	}
	if epoch != sess.Epoch {
		return "", nil // 已被新会话取代
	}
	active, err := q.CountActiveAttempts(ctx, sess.InstanceID)
	if err != nil {
		return "", err
	}
	target := instance.AfterDisconnect(instance.Status(status), normalClose, active)
	if _, err := q.MarkDisconnected(ctx, sess.InstancePK, sess.Epoch, string(target)); err != nil {
		return "", err
	}
	return string(target), nil
}
