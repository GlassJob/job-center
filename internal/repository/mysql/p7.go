package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ---------- overview ----------

type OverviewCounts struct {
	ServiceCount        int     `json:"service_count"`
	OnlineInstanceCount int     `json:"online_instance_count"`
	TaskCount           int     `json:"task_count"`
	TodayExecutionCount int     `json:"today_execution_count"`
	TodaySuccessRate    float64 `json:"today_success_rate"`
	TodayFailedCount    int     `json:"today_failed_count"`
	RunningCount        int     `json:"running_count"`
}

// Overview 统计；[from,to) 为"今日"的 UTC 区间。
func (q *Queries) Overview(ctx context.Context, from, to time.Time) (*OverviewCounts, error) {
	var o OverviewCounts
	var succ, finished int
	err := q.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM job_service),
		(SELECT COUNT(*) FROM job_instance WHERE status='ONLINE'),
		(SELECT COUNT(*) FROM job_definition),
		(SELECT COUNT(*) FROM job_execution WHERE created_at>=? AND created_at<?),
		(SELECT COUNT(*) FROM job_execution WHERE created_at>=? AND created_at<? AND status IN ('FAILED','TIMED_OUT')),
		(SELECT COUNT(*) FROM job_execution WHERE created_at>=? AND created_at<? AND status='SUCCEEDED'),
		(SELECT COUNT(*) FROM job_execution WHERE created_at>=? AND created_at<? AND status IN ('SUCCEEDED','FAILED','TIMED_OUT','CANCELLED')),
		(SELECT COUNT(*) FROM job_execution WHERE status='RUNNING')`,
		from, to, from, to, from, to, from, to).Scan(&o.ServiceCount, &o.OnlineInstanceCount, &o.TaskCount,
		&o.TodayExecutionCount, &o.TodayFailedCount, &succ, &finished, &o.RunningCount)
	if err != nil {
		return nil, err
	}
	if finished > 0 {
		o.TodaySuccessRate = float64(succ) / float64(finished)
	}
	return &o, nil
}

// ---------- SSE sources ----------

type ExecEvent struct {
	ID         int64
	AttemptID  *string
	EventType  string
	From, To   *string
	Payload    json.RawMessage
	CreatedAt  time.Time
}

func (q *Queries) ListEventsAfter(ctx context.Context, executionID string, afterID int64, limit int) ([]*ExecEvent, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT id, attempt_id, event_type, from_status, to_status, payload, created_at
		FROM job_execution_event WHERE execution_id=? AND id>? ORDER BY id LIMIT ?`, executionID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ExecEvent
	for rows.Next() {
		var e ExecEvent
		var a, f, t sql.NullString
		var p []byte
		if err := rows.Scan(&e.ID, &a, &e.EventType, &f, &t, &p, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.AttemptID, e.From, e.To = ptrStr(a), ptrStr(f), ptrStr(t)
		if len(p) > 0 {
			e.Payload = json.RawMessage(p)
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

type CursorLog struct {
	AttemptID     string          `json:"attempt_id"`
	AttemptNumber int             `json:"attempt_number"`
	Seq           int64           `json:"seq"`
	Level         string          `json:"level"`
	Stream        *string         `json:"stream,omitempty"`
	Message       string          `json:"message"`
	Fields        json.RawMessage `json:"fields,omitempty"`
	Truncated     bool            `json:"truncated"`
	LoggedAt      time.Time       `json:"logged_at"`
}

// ListLogsAfterCursor 按 (attempt_number, seq) > (n, s) 读取日志。
func (q *Queries) ListLogsAfterCursor(ctx context.Context, executionID string, attemptNumber int, seq int64, limit int) ([]*CursorLog, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT l.attempt_id, a.attempt_number, l.sequence, l.level, l.stream, l.message, l.fields, l.truncated, l.logged_at
		FROM job_execution_log l JOIN job_execution_attempt a ON a.attempt_id=l.attempt_id
		WHERE l.execution_id=? AND (a.attempt_number>? OR (a.attempt_number=? AND l.sequence>?))
		ORDER BY a.attempt_number, l.sequence LIMIT ?`, executionID, attemptNumber, attemptNumber, seq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CursorLog
	for rows.Next() {
		var l CursorLog
		var st sql.NullString
		var f []byte
		if err := rows.Scan(&l.AttemptID, &l.AttemptNumber, &l.Seq, &l.Level, &st, &l.Message, &f, &l.Truncated, &l.LoggedAt); err != nil {
			return nil, err
		}
		l.Stream = ptrStr(st)
		if len(f) > 0 {
			l.Fields = json.RawMessage(f)
		}
		l.LoggedAt = l.LoggedAt.UTC()
		out = append(out, &l)
	}
	return out, rows.Err()
}

type ProgressSnap struct {
	AttemptID     string
	AttemptNumber int
	Percent       *float64
	Message       *string
	UpdatedAt     *time.Time
}

func (q *Queries) CurrentProgress(ctx context.Context, executionID string) (*ProgressSnap, error) {
	var p ProgressSnap
	var pct sql.NullFloat64
	var msg sql.NullString
	var up sql.NullTime
	err := q.db.QueryRowContext(ctx, `SELECT a.attempt_id, a.attempt_number, a.progress_percent, a.progress_message, a.progress_updated_at
		FROM job_execution e JOIN job_execution_attempt a ON a.attempt_id=e.current_attempt_id WHERE e.execution_id=?`, executionID).
		Scan(&p.AttemptID, &p.AttemptNumber, &pct, &msg, &up)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if pct.Valid {
		p.Percent = &pct.Float64
	}
	p.Message, p.UpdatedAt = ptrStr(msg), ptrTime(up)
	return &p, nil
}

// ListLogsFiltered 增量读取，level 为最低级别；多取 1 条判断 has_more。
func (q *Queries) ListLogsFiltered(ctx context.Context, executionID, attemptID, minLevel string, afterSeq int64, limit int) ([]map[string]any, int64, bool, error) {
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	levels := levelsFrom(minLevel)
	ph := strings.TrimRight(strings.Repeat("?,", len(levels)), ",")
	args := []any{executionID, attemptID, attemptID, afterSeq}
	for _, l := range levels {
		args = append(args, l)
	}
	args = append(args, limit+1)
	rows, err := q.db.QueryContext(ctx, `SELECT attempt_id, sequence, level, stream, message, fields, truncated, logged_at FROM job_execution_log
		WHERE execution_id=? AND (?='' OR attempt_id=?) AND sequence>? AND level IN (`+ph+`) ORDER BY sequence, id LIMIT ?`, args...)
	if err != nil {
		return nil, afterSeq, false, err
	}
	defer rows.Close()
	var items []map[string]any
	next := afterSeq
	for rows.Next() {
		var aid, level, msg string
		var seq int64
		var stream sql.NullString
		var fields []byte
		var tr bool
		var logged time.Time
		if err := rows.Scan(&aid, &seq, &level, &stream, &msg, &fields, &tr, &logged); err != nil {
			return nil, next, false, err
		}
		item := map[string]any{"attempt_id": aid, "seq": seq, "level": level, "stream": ptrStr(stream), "message": msg,
			"fields": nil, "truncated": tr, "logged_at": logged.UTC()}
		if len(fields) > 0 {
			item["fields"] = json.RawMessage(fields)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, next, false, err
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	if len(items) > 0 {
		next = items[len(items)-1]["seq"].(int64)
	}
	return items, next, more, nil
}

func levelsFrom(min string) []string {
	all := []string{"DEBUG", "INFO", "WARN", "ERROR"}
	for i, l := range all {
		if strings.EqualFold(l, min) {
			return all[i:]
		}
	}
	return all
}

// ---------- deployments ----------

type Deployment struct {
	ID                int64      `json:"id"`
	ServiceID         int64      `json:"service_id"`
	ServiceCode       string     `json:"service_code"`
	Environment       string     `json:"environment"`
	DeploymentVersion string     `json:"deployment_version"`
	GitCommit         string     `json:"git_commit"`
	BuildID           string     `json:"build_id"`
	BuildURL          *string    `json:"build_url"`
	DeploymentStatus  string     `json:"deployment_status"`
	IsExpected        bool       `json:"is_expected"`
	Source            string     `json:"source"`
	TriggeredBy       *string    `json:"triggered_by"`
	DeployedAt        *time.Time `json:"deployed_at"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type DeploymentInput struct {
	ServiceID         int64
	Environment       string
	DeploymentVersion string
	GitCommit         string
	BuildID           string
	BuildURL          string
	Status            string
	SetExpected       bool
	Source            string
	TriggeredBy       string
	Payload           any
	DeployedAt        *time.Time
}

const deploySelect = `SELECT d.id, d.service_id, s.service_code, d.environment, d.deployment_version, d.git_commit, d.build_id, d.build_url,
	d.deployment_status, d.expected_key IS NOT NULL, d.source, d.triggered_by, d.deployed_at, d.created_at, d.updated_at
	FROM job_deployment d JOIN job_service s ON s.id=d.service_id`

func scanDeployment(sc interface{ Scan(...any) error }) (*Deployment, error) {
	var d Deployment
	var url, by sql.NullString
	var at sql.NullTime
	if err := sc.Scan(&d.ID, &d.ServiceID, &d.ServiceCode, &d.Environment, &d.DeploymentVersion, &d.GitCommit, &d.BuildID, &url,
		&d.DeploymentStatus, &d.IsExpected, &d.Source, &by, &at, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, err
	}
	d.BuildURL, d.TriggeredBy, d.DeployedAt = ptrStr(url), ptrStr(by), ptrTime(at)
	return &d, nil
}

func (q *Queries) GetServiceIDByCode(ctx context.Context, code string) (int64, error) {
	var id int64
	err := q.db.QueryRowContext(ctx, `SELECT id FROM job_service WHERE service_code=?`, code).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

func (q *Queries) GetServiceCodeByID(ctx context.Context, id int64) (string, error) {
	var code string
	err := q.db.QueryRowContext(ctx, `SELECT service_code FROM job_service WHERE id=?`, id).Scan(&code)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return code, err
}

// UpsertDeployment 按 (service_id, environment, build_id) 幂等；SUCCEEDED+set_expected 时切换预期版本。须在事务内调用。
func (q *Queries) UpsertDeployment(ctx context.Context, in DeploymentInput) (*Deployment, error) {
	var payload any
	if in.Payload != nil {
		b, _ := json.Marshal(in.Payload)
		payload = string(b)
	}
	if in.DeployedAt == nil && in.Status == "SUCCEEDED" {
		now := time.Now().UTC()
		in.DeployedAt = &now
	}
	_, err := q.db.ExecContext(ctx, `INSERT INTO job_deployment(service_id, environment, deployment_version, git_commit, build_id, build_url,
		deployment_status, source, triggered_by, payload, deployed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON DUPLICATE KEY UPDATE deployment_version=VALUES(deployment_version), git_commit=VALUES(git_commit), build_url=VALUES(build_url),
		deployment_status=VALUES(deployment_status), payload=VALUES(payload), deployed_at=COALESCE(VALUES(deployed_at), deployed_at)`,
		in.ServiceID, in.Environment, in.DeploymentVersion, in.GitCommit, in.BuildID, nullStr(in.BuildURL), in.Status, in.Source,
		nullStr(in.TriggeredBy), payload, in.DeployedAt)
	if err != nil {
		return nil, err
	}
	var id int64
	if err := q.db.QueryRowContext(ctx, `SELECT id FROM job_deployment WHERE service_id=? AND environment=? AND build_id=? FOR UPDATE`,
		in.ServiceID, in.Environment, in.BuildID).Scan(&id); err != nil {
		return nil, err
	}
	if in.Status == "SUCCEEDED" && in.SetExpected {
		key := fmt.Sprintf("%d:%s", in.ServiceID, in.Environment)
		if _, err := q.db.ExecContext(ctx, `UPDATE job_deployment SET expected_key=NULL WHERE expected_key=? AND id<>?`, key, id); err != nil {
			return nil, err
		}
		if _, err := q.db.ExecContext(ctx, `UPDATE job_deployment SET expected_key=? WHERE id=?`, key, id); err != nil {
			return nil, err
		}
	}
	return scanDeployment(q.db.QueryRowContext(ctx, deploySelect+` WHERE d.id=?`, id))
}

func (q *Queries) ListDeployments(ctx context.Context, serviceID int64, env string, limit int) ([]*Deployment, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	rows, err := q.db.QueryContext(ctx, deploySelect+` WHERE (?=0 OR d.service_id=?) AND (?='' OR d.environment=?) ORDER BY d.id DESC LIMIT ?`,
		serviceID, serviceID, env, env, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Deployment{}
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (q *Queries) GetExpectedDeployment(ctx context.Context, serviceID int64, env string) (*Deployment, error) {
	d, err := scanDeployment(q.db.QueryRowContext(ctx, deploySelect+` WHERE d.expected_key=?`, fmt.Sprintf("%d:%s", serviceID, env)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return d, err
}

func (q *Queries) GetLastDeployment(ctx context.Context, serviceID int64, env string) (*Deployment, error) {
	d, err := scanDeployment(q.db.QueryRowContext(ctx, deploySelect+` WHERE d.service_id=? AND d.environment=? ORDER BY d.id DESC LIMIT 1`, serviceID, env))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return d, err
}

type RunningInstance struct {
	InstanceID      string     `json:"instance_id"`
	AppVersion      string     `json:"app_version"`
	GitCommit       string     `json:"git_commit"`
	Status          string     `json:"status"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
	StartedAt       time.Time  `json:"started_at"`
	MatchesExpected bool       `json:"matches_expected"`
}

func (q *Queries) ListLiveInstances(ctx context.Context, serviceID int64, env string) ([]*RunningInstance, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT instance_id, COALESCE(app_version,''), COALESCE(git_commit,''), status, last_heartbeat_at, started_at
		FROM job_instance WHERE service_id=? AND environment=? AND status IN ('ONLINE','SUSPECT','DRAINING') ORDER BY started_at`, serviceID, env)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*RunningInstance{}
	for rows.Next() {
		var r RunningInstance
		var hb sql.NullTime
		if err := rows.Scan(&r.InstanceID, &r.AppVersion, &r.GitCommit, &r.Status, &hb, &r.StartedAt); err != nil {
			return nil, err
		}
		r.LastHeartbeatAt = ptrTime(hb)
		out = append(out, &r)
	}
	return out, rows.Err()
}

// CountUncoveredDefinitions 服务内已定义但没有任何在线实例注册的任务数。
func (q *Queries) CountUncoveredDefinitions(ctx context.Context, serviceID int64, env string) (int, error) {
	var n int
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_definition d WHERE d.service_id=? AND NOT EXISTS (
		SELECT 1 FROM job_instance_task it JOIN job_instance i ON i.id=it.instance_pk
		WHERE it.definition_id=d.id AND i.environment=? AND i.status IN ('ONLINE','SUSPECT','DRAINING'))`, serviceID, env).Scan(&n)
	return n, err
}

type ServiceRef struct {
	ID   int64
	Code string
}

func (q *Queries) ListServiceRefs(ctx context.Context, serviceID int64) ([]ServiceRef, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT id, service_code FROM job_service WHERE (?=0 OR id=?) ORDER BY id`, serviceID, serviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceRef
	for rows.Next() {
		var s ServiceRef
		if err := rows.Scan(&s.ID, &s.Code); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ---------- monitoring alerts ----------

type Alert struct {
	Type        string     `json:"type"`
	Severity    string     `json:"severity"`
	ServiceID   int64      `json:"service_id,omitempty"`
	ServiceCode string     `json:"service_code,omitempty"`
	TaskID      int64      `json:"task_id,omitempty"`
	TaskCode    string     `json:"task_code,omitempty"`
	InstanceID  string     `json:"instance_id,omitempty"`
	ExecutionID string     `json:"execution_id,omitempty"`
	Since       *time.Time `json:"since,omitempty"`
	Message     string     `json:"message"`
}

func (q *Queries) collect(ctx context.Context, query string, args []any, fn func(sc *sql.Rows) (*Alert, error)) ([]*Alert, error) {
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Alert
	for rows.Next() {
		a, err := fn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// InstanceAlerts 近 window 内转 OFFLINE 的实例 + 有启用调度却无在线实例的服务。
func (q *Queries) InstanceAlerts(ctx context.Context, window time.Duration) ([]*Alert, error) {
	a1, err := q.collect(ctx, `SELECT i.instance_id, i.service_id, s.service_code, i.disconnected_at FROM job_instance i JOIN job_service s ON s.id=i.service_id
		WHERE i.status='OFFLINE' AND i.disconnected_at >= UTC_TIMESTAMP(3) - INTERVAL ? MICROSECOND ORDER BY i.disconnected_at DESC LIMIT 200`,
		[]any{window.Microseconds()}, func(r *sql.Rows) (*Alert, error) {
			a := &Alert{Type: "INSTANCE_OFFLINE", Severity: "WARN"}
			var at sql.NullTime
			if err := r.Scan(&a.InstanceID, &a.ServiceID, &a.ServiceCode, &at); err != nil {
				return nil, err
			}
			a.Since = ptrTime(at)
			a.Message = "instance went OFFLINE"
			return a, nil
		})
	if err != nil {
		return nil, err
	}
	a2, err := q.collect(ctx, `SELECT s.id, s.service_code, (SELECT MAX(COALESCE(i.disconnected_at, i.last_heartbeat_at)) FROM job_instance i WHERE i.service_id=s.id)
		FROM job_service s WHERE s.status='ACTIVE'
		AND EXISTS (SELECT 1 FROM job_definition d JOIN job_schedule sc ON sc.task_id=d.id WHERE d.service_id=s.id AND sc.enabled=1)
		AND NOT EXISTS (SELECT 1 FROM job_instance i WHERE i.service_id=s.id AND i.status='ONLINE')`, nil,
		func(r *sql.Rows) (*Alert, error) {
			a := &Alert{Type: "SERVICE_NO_ONLINE_INSTANCE", Severity: "CRITICAL"}
			var at sql.NullTime
			if err := r.Scan(&a.ServiceID, &a.ServiceCode, &at); err != nil {
				return nil, err
			}
			a.Since = ptrTime(at)
			a.Message = "service has enabled schedules but no ONLINE instance"
			return a, nil
		})
	return append(a1, a2...), err
}

// OverdueAlerts 任务逾期：调度停滞（next_fire_at 过期超 grace 未推进）、待派发超 grace、运行超过 deadline。
func (q *Queries) OverdueAlerts(ctx context.Context, grace time.Duration) ([]*Alert, error) {
	g := grace.Microseconds()
	a1, err := q.collect(ctx, `SELECT d.id, d.task_code, d.service_id, s.service_code, sc.next_fire_at FROM job_schedule sc
		JOIN job_definition d ON d.id=sc.task_id JOIN job_service s ON s.id=d.service_id
		WHERE sc.enabled=1 AND sc.schedule_type='CRON' AND sc.next_fire_at < UTC_TIMESTAMP(3) - INTERVAL ? MICROSECOND LIMIT 200`,
		[]any{g}, func(r *sql.Rows) (*Alert, error) {
			a := &Alert{Type: "SCHEDULE_OVERDUE", Severity: "WARN"}
			var at time.Time
			if err := r.Scan(&a.TaskID, &a.TaskCode, &a.ServiceID, &a.ServiceCode, &at); err != nil {
				return nil, err
			}
			a.Since, a.Message = &at, "cron next_fire_at passed but not fired"
			return a, nil
		})
	if err != nil {
		return nil, err
	}
	a2, err := q.collect(ctx, `SELECT e.execution_id, e.task_id, e.task_code, e.service_id, s.service_code, e.status,
		COALESCE(IF(e.status='RUNNING', e.deadline_at, COALESCE(e.next_attempt_at, e.created_at)), e.created_at)
		FROM job_execution e JOIN job_service s ON s.id=e.service_id
		WHERE (e.status='PENDING' AND COALESCE(e.next_attempt_at, e.created_at) < UTC_TIMESTAMP(3) - INTERVAL ? MICROSECOND)
		   OR (e.status='RUNNING' AND e.deadline_at IS NOT NULL AND e.deadline_at < UTC_TIMESTAMP(3))
		ORDER BY e.id LIMIT 200`, []any{g}, func(r *sql.Rows) (*Alert, error) {
		a := &Alert{Severity: "WARN"}
		var st string
		var at time.Time
		if err := r.Scan(&a.ExecutionID, &a.TaskID, &a.TaskCode, &a.ServiceID, &a.ServiceCode, &st, &at); err != nil {
			return nil, err
		}
		a.Since = &at
		if st == "RUNNING" {
			a.Type, a.Message = "EXECUTION_PAST_DEADLINE", "execution running past deadline"
		} else {
			a.Type, a.Message = "EXECUTION_DISPATCH_OVERDUE", "execution pending dispatch beyond grace"
		}
		return a, nil
	})
	return append(a1, a2...), err
}

// CountVersionMismatch 在线实例与 expected 发布不一致的数量（overview.version_mismatch_instance_count）。
func (q *Queries) CountVersionMismatch(ctx context.Context) (int, error) {
	var n int
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_instance i
		JOIN job_deployment d ON d.expected_key = CONCAT(i.service_id, ':', i.environment)
		WHERE i.status IN ('ONLINE','SUSPECT','DRAINING')
		AND NOT (
			(COALESCE(i.git_commit,'') <> '' AND COALESCE(d.git_commit,'') <> '' AND (
				i.git_commit = d.git_commit
				OR (CHAR_LENGTH(i.git_commit) >= 7 AND d.git_commit LIKE CONCAT(i.git_commit, '%'))
				OR (CHAR_LENGTH(d.git_commit) >= 7 AND i.git_commit LIKE CONCAT(d.git_commit, '%'))
			))
			OR ((COALESCE(i.git_commit,'') = '' OR COALESCE(d.git_commit,'') = '') AND COALESCE(i.app_version,'') = d.deployment_version)
		)`).Scan(&n)
	return n, err
}
