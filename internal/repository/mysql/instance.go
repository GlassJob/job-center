package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// InstanceLock 认证时锁定的实例行。
type InstanceLock struct {
	PK        int64
	ServiceID int64
	Epoch     int64
	Status    string
}

// LockInstance 按 instance_id 加行锁（FOR UPDATE）。
func (q *Queries) LockInstance(ctx context.Context, instanceID string) (*InstanceLock, error) {
	var l InstanceLock
	err := q.db.QueryRowContext(ctx, "SELECT id, service_id, session_epoch, status FROM job_instance WHERE instance_id=? FOR UPDATE", instanceID).
		Scan(&l.PK, &l.ServiceID, &l.Epoch, &l.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &l, err
}

// LockInstanceByPK 注册/会话写入前锁定实例行，用于 session_epoch fencing。
func (q *Queries) LockInstanceByPK(ctx context.Context, pk int64) (*InstanceLock, error) {
	var l InstanceLock
	err := q.db.QueryRowContext(ctx, "SELECT id, service_id, session_epoch, status FROM job_instance WHERE id=? FOR UPDATE", pk).
		Scan(&l.PK, &l.ServiceID, &l.Epoch, &l.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &l, err
}

// SessionInfo 认证成功时写入的连接信息。
type SessionInfo struct {
	InstanceID, PeerIP, NodeID, ConnectionID, Language, SDKVersion, ProtocolVersion string
	ServiceID                                                                     int64
}

// InsertInstance 新实例（session_epoch=1，状态 REGISTERING）。hostname/started_at 在 register 时补齐。
func (q *Queries) InsertInstance(ctx context.Context, s SessionInfo) (int64, error) {
	res, err := q.db.ExecContext(ctx, `INSERT INTO job_instance(instance_id, service_id, hostname, ip_address, language, sdk_version, protocol_version,
		started_at, connected_at, status, session_epoch, gateway_node_id, connection_id)
		VALUES (?,?,'',?,?,?,?,NOW(3),NOW(3),'REGISTERING',1,?,?)`,
		s.InstanceID, s.ServiceID, nullStr(s.PeerIP), s.Language, nullStr(s.SDKVersion), s.ProtocolVersion, nullStr(s.NodeID), nullStr(s.ConnectionID))
	if err != nil {
		if IsDuplicate(err) {
			return 0, ErrDuplicate
		}
		return 0, err
	}
	return res.LastInsertId()
}

// NewSession session_epoch+1，状态回到 REGISTERING（同 instance_id 重连）。调用方需已持有行锁。
func (q *Queries) NewSession(ctx context.Context, pk int64, s SessionInfo) error {
	_, err := q.db.ExecContext(ctx, `UPDATE job_instance SET session_epoch=session_epoch+1, status='REGISTERING', connected_at=NOW(3), disconnected_at=NULL,
		ip_address=?, sdk_version=?, protocol_version=?, gateway_node_id=?, connection_id=?, language=? WHERE id=?`,
		nullStr(s.PeerIP), nullStr(s.SDKVersion), s.ProtocolVersion, nullStr(s.NodeID), nullStr(s.ConnectionID), s.Language, pk)
	return err
}

// RegistrationInfo service.register 上报的实例信息。
type RegistrationInfo struct {
	Environment, Hostname, ReportedIP, Language, RuntimeVersion, AppVersion, GitCommit string
	PID                                                                                int
	BuildTime, StartedAt                                                               *time.Time
	MaxConcurrency                                                                     int
	Labels                                                                             map[string]string
	Status                                                                             string // ONLINE|DRAINING
}

// UpdateRegistration 写入实例注册信息（带 session_epoch 条件）。
func (q *Queries) UpdateRegistration(ctx context.Context, pk, epoch int64, r RegistrationInfo) (int64, error) {
	var meta any
	if len(r.Labels) > 0 {
		b, _ := json.Marshal(map[string]any{"labels": r.Labels})
		meta = string(b)
	}
	var pid any
	if r.PID > 0 {
		pid = r.PID
	}
	res, err := q.db.ExecContext(ctx, `UPDATE job_instance SET environment=?, hostname=?, reported_ip=?, pid=?, language=?, runtime_version=?,
		app_version=?, git_commit=?, build_time=?, started_at=COALESCE(?, started_at), max_concurrency=?, metadata=?, status=?,
		registered_at=NOW(3), last_heartbeat_at=NOW(3) WHERE id=? AND session_epoch=?`,
		r.Environment, r.Hostname, nullStr(r.ReportedIP), pid, r.Language, nullStr(r.RuntimeVersion),
		nullStr(r.AppVersion), nullStr(r.GitCommit), r.BuildTime, r.StartedAt, r.MaxConcurrency, meta, r.Status, pk, epoch)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Heartbeat 更新心跳；SUSPECT 恢复为 ONLINE；上报 DRAINING 则进入 DRAINING（不可逆）。
// 只对 ONLINE/SUSPECT/DRAINING 生效（REGISTERING/OFFLINE 需要重新 register）。
func (q *Queries) Heartbeat(ctx context.Context, pk, epoch int64, running int, maxConc *int, draining bool) (int64, error) {
	res, err := q.db.ExecContext(ctx, `UPDATE job_instance SET last_heartbeat_at=NOW(3), running_count=?, max_concurrency=COALESCE(?, max_concurrency),
		status=CASE WHEN status='DRAINING' OR ? THEN 'DRAINING' ELSE 'ONLINE' END
		WHERE id=? AND session_epoch=? AND status IN ('ONLINE','SUSPECT','DRAINING')`, running, maxConc, draining, pk, epoch)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// InstanceState 当前 epoch 与状态。
func (q *Queries) InstanceState(ctx context.Context, pk int64) (epoch int64, status string, err error) {
	err = q.db.QueryRowContext(ctx, "SELECT session_epoch, status FROM job_instance WHERE id=?", pk).Scan(&epoch, &status)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

// SetDraining service.unregister → DRAINING。
func (q *Queries) SetDraining(ctx context.Context, pk, epoch int64) (int64, error) {
	res, err := q.db.ExecContext(ctx, `UPDATE job_instance SET status='DRAINING' WHERE id=? AND session_epoch=? AND status IN ('REGISTERING','ONLINE','SUSPECT')`, pk, epoch)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// MarkDisconnected 连接关闭：仅当 epoch 仍是本会话时生效（被替换的旧连接不影响新会话）。
func (q *Queries) MarkDisconnected(ctx context.Context, pk, epoch int64, status string) (int64, error) {
	res, err := q.db.ExecContext(ctx, `UPDATE job_instance SET status=?, disconnected_at=NOW(3), connection_id=NULL
		WHERE id=? AND session_epoch=? AND status<>'OFFLINE'`, status, pk, epoch)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountActiveAttempts 实例上未终结的 Attempt 数（P1 无派发，恒为 0；P2 起生效）。
func (q *Queries) CountActiveAttempts(ctx context.Context, instanceID string) (int, error) {
	var n int
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_execution_attempt WHERE instance_id=? AND status IN ('CREATED','DISPATCHING','ACKNOWLEDGED','RUNNING')`, instanceID).Scan(&n)
	return n, err
}

// SweepSuspect ONLINE 超过 suspectAfter 无心跳 → SUSPECT。
func (q *Queries) SweepSuspect(ctx context.Context, suspectAfter time.Duration) (int64, error) {
	res, err := q.db.ExecContext(ctx, `UPDATE job_instance SET status='SUSPECT'
		WHERE status='ONLINE' AND last_heartbeat_at < NOW(3) - INTERVAL ? MICROSECOND`, suspectAfter.Microseconds())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SweepOffline SUSPECT/DRAINING/REGISTERING 超过 offlineAfter 未恢复 → OFFLINE。
// 起算点 = max(最后心跳, 断线时间, 连接时间)。
func (q *Queries) SweepOffline(ctx context.Context, offlineAfter time.Duration) (int64, error) {
	res, err := q.db.ExecContext(ctx, `UPDATE job_instance SET status='OFFLINE', disconnected_at=COALESCE(disconnected_at, NOW(3)), connection_id=NULL
		WHERE status IN ('SUSPECT','DRAINING','REGISTERING')
		  AND GREATEST(COALESCE(last_heartbeat_at, '1970-01-01'), COALESCE(disconnected_at, '1970-01-01'), COALESCE(connected_at, '1970-01-01'))
		      < NOW(3) - INTERVAL ? MICROSECOND`, offlineAfter.Microseconds())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RecoverNode 节点启动：本节点名下仍为在线类状态的实例（上次进程未清理）标记为 SUSPECT/OFFLINE。
func (q *Queries) RecoverNode(ctx context.Context, nodeID string) (int64, error) {
	res, err := q.db.ExecContext(ctx, `UPDATE job_instance SET status=IF(status IN ('DRAINING','REGISTERING'),'OFFLINE','SUSPECT'),
		disconnected_at=COALESCE(disconnected_at, NOW(3)), connection_id=NULL
		WHERE gateway_node_id=? AND status IN ('ONLINE','REGISTERING','DRAINING')`, nodeID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

type Instance struct {
	PK              int64      `json:"-"`
	InstanceID      string     `json:"instance_id"`
	ServiceID       int64      `json:"service_id"`
	ServiceCode     string     `json:"service_code"`
	Environment     string     `json:"environment"`
	Hostname        string     `json:"hostname"`
	IPAddress       *string    `json:"ip_address"`
	ReportedIP      *string    `json:"reported_ip"`
	PID             *int64     `json:"pid"`
	Language        string     `json:"language"`
	RuntimeVersion  *string    `json:"runtime_version"`
	SDKVersion      *string    `json:"sdk_version"`
	ProtocolVersion string     `json:"protocol_version"`
	AppVersion      *string    `json:"app_version"`
	GitCommit       *string    `json:"git_commit"`
	BuildTime       *time.Time `json:"build_time"`
	StartedAt       time.Time  `json:"started_at"`
	RegisteredAt    time.Time  `json:"registered_at"`
	ConnectedAt     *time.Time `json:"connected_at"`
	DisconnectedAt  *time.Time `json:"disconnected_at"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
	Status          string     `json:"status"`
	MaxConcurrency  int        `json:"max_concurrency"`
	RunningCount    int        `json:"running_count"`
	SessionEpoch    int64      `json:"session_epoch"`
	GatewayNodeID   *string    `json:"gateway_node_id"`
}

type InstanceFilter struct {
	ServiceID   int64
	Status      string
	InstanceID  string
	Page, Limit int
}

const instanceSelect = `SELECT i.id, i.instance_id, i.service_id, s.service_code, i.environment, i.hostname, i.ip_address, i.reported_ip, i.pid, i.language,
  i.runtime_version, i.sdk_version, i.protocol_version, i.app_version, i.git_commit, i.build_time, i.started_at, i.registered_at, i.connected_at,
  i.disconnected_at, i.last_heartbeat_at, i.status, i.max_concurrency, i.running_count, i.session_epoch, i.gateway_node_id
FROM job_instance i JOIN job_service s ON s.id=i.service_id`

func scanInstance(sc interface{ Scan(...any) error }) (*Instance, error) {
	var x Instance
	var ip, rip, rv, sdk, av, gc, node sql.NullString
	var pid sql.NullInt64
	var bt, ca, da, hb sql.NullTime
	if err := sc.Scan(&x.PK, &x.InstanceID, &x.ServiceID, &x.ServiceCode, &x.Environment, &x.Hostname, &ip, &rip, &pid, &x.Language, &rv, &sdk,
		&x.ProtocolVersion, &av, &gc, &bt, &x.StartedAt, &x.RegisteredAt, &ca, &da, &hb, &x.Status, &x.MaxConcurrency, &x.RunningCount, &x.SessionEpoch, &node); err != nil {
		return nil, err
	}
	x.IPAddress, x.ReportedIP, x.RuntimeVersion, x.SDKVersion, x.AppVersion, x.GitCommit, x.GatewayNodeID = ptrStr(ip), ptrStr(rip), ptrStr(rv), ptrStr(sdk), ptrStr(av), ptrStr(gc), ptrStr(node)
	if pid.Valid {
		x.PID = &pid.Int64
	}
	x.BuildTime, x.ConnectedAt, x.DisconnectedAt, x.LastHeartbeatAt = ptrTime(bt), ptrTime(ca), ptrTime(da), ptrTime(hb)
	x.StartedAt, x.RegisteredAt = x.StartedAt.UTC(), x.RegisteredAt.UTC()
	return &x, nil
}

func (q *Queries) ListInstances(ctx context.Context, f InstanceFilter) ([]*Instance, int, error) {
	where, args := " WHERE 1=1", []any{}
	if f.ServiceID > 0 {
		where += " AND i.service_id=?"
		args = append(args, f.ServiceID)
	}
	if f.Status != "" {
		where += " AND i.status=?"
		args = append(args, f.Status)
	}
	if f.InstanceID != "" {
		where += " AND i.instance_id=?"
		args = append(args, f.InstanceID)
	}
	var total int
	if err := q.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM job_instance i"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := q.db.QueryContext(ctx, instanceSelect+where+" ORDER BY i.id DESC LIMIT ? OFFSET ?", append(args, f.Limit, (f.Page-1)*f.Limit)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*Instance{}
	for rows.Next() {
		x, err := scanInstance(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, x)
	}
	return out, total, rows.Err()
}
