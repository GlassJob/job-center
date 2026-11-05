package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type Service struct {
	ID                  int64     `json:"id"`
	ServiceCode         string    `json:"service_code"`
	ServiceName         string    `json:"service_name"`
	Description         *string   `json:"description"`
	Status              string    `json:"status"`
	Languages           []string  `json:"languages"`
	InstanceCount       int       `json:"instance_count"`
	OnlineInstanceCount int       `json:"online_instance_count"`
	TaskCount           int       `json:"task_count"`
	RunningVersions     []string  `json:"running_versions"`
	CreatedAt           time.Time `json:"created_at"`
}

func (q *Queries) CreateService(ctx context.Context, code, name, desc string) (int64, error) {
	res, err := q.db.ExecContext(ctx, "INSERT INTO job_service(service_code, service_name, description) VALUES (?,?,?)", code, name, nullStr(desc))
	if err != nil {
		if IsDuplicate(err) {
			return 0, ErrDuplicate
		}
		return 0, err
	}
	return res.LastInsertId()
}

const serviceSelect = `SELECT s.id, s.service_code, s.service_name, s.description, s.status, s.created_at,
  (SELECT COUNT(*) FROM job_instance i WHERE i.service_id=s.id AND i.status<>'OFFLINE'),
  (SELECT COUNT(*) FROM job_instance i WHERE i.service_id=s.id AND i.status='ONLINE'),
  (SELECT COUNT(*) FROM job_definition d WHERE d.service_id=s.id AND d.status<>'DISABLED'),
  (SELECT COALESCE(GROUP_CONCAT(DISTINCT i.language ORDER BY i.language),'') FROM job_instance i WHERE i.service_id=s.id AND i.status<>'OFFLINE'),
  (SELECT COALESCE(GROUP_CONCAT(DISTINCT i.app_version ORDER BY i.app_version),'') FROM job_instance i WHERE i.service_id=s.id AND i.status IN ('ONLINE','SUSPECT','DRAINING') AND i.app_version IS NOT NULL)
FROM job_service s`

func scanService(sc interface{ Scan(...any) error }) (*Service, error) {
	var s Service
	var desc sql.NullString
	var langs, vers string
	if err := sc.Scan(&s.ID, &s.ServiceCode, &s.ServiceName, &desc, &s.Status, &s.CreatedAt, &s.InstanceCount, &s.OnlineInstanceCount, &s.TaskCount, &langs, &vers); err != nil {
		return nil, err
	}
	s.Description = ptrStr(desc)
	s.Languages, s.RunningVersions = splitList(langs), splitList(vers)
	s.CreatedAt = s.CreatedAt.UTC()
	return &s, nil
}

func splitList(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}

func (q *Queries) GetService(ctx context.Context, id int64) (*Service, error) {
	s, err := scanService(q.db.QueryRowContext(ctx, serviceSelect+" WHERE s.id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return s, err
}

func (q *Queries) ListServices(ctx context.Context, keyword string, page, limit int) ([]*Service, int, error) {
	where, args := "", []any{}
	if keyword != "" {
		where = " WHERE s.service_code LIKE ? OR s.service_name LIKE ?"
		k := "%" + escapeLike(keyword) + "%"
		args = append(args, k, k)
	}
	var total int
	if err := q.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM job_service s"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := q.db.QueryContext(ctx, serviceSelect+where+" ORDER BY s.id LIMIT ? OFFSET ?", append(args, limit, (page-1)*limit)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*Service{}
	for rows.Next() {
		s, err := scanService(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, s)
	}
	return out, total, rows.Err()
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// Credential 行（不含 secret_hash 以外的明文；明文永不落库）。
type Credential struct {
	ID               int64      `json:"-"`
	ServiceID        int64      `json:"service_id"`
	KeyID            string     `json:"key_id"`
	SecretHash       []byte     `json:"-"`
	SecretHint       string     `json:"secret_hint"`
	Status           string     `json:"status"`
	AllowedTaskCodes []string   `json:"allowed_task_codes"`
	ExpiresAt        *time.Time `json:"expires_at"`
	LastUsedAt       *time.Time `json:"last_used_at"`
	CreatedBy        string     `json:"-"`
	CreatedAt        time.Time  `json:"created_at"`
}

func (q *Queries) CreateCredential(ctx context.Context, c *Credential) error {
	var allowed any
	if c.AllowedTaskCodes != nil {
		b, _ := json.Marshal(c.AllowedTaskCodes)
		allowed = string(b)
	}
	_, err := q.db.ExecContext(ctx, `INSERT INTO job_service_credential(service_id, key_id, secret_hash, hash_algo, secret_hint, status, allowed_task_codes, expires_at, created_by)
		VALUES (?,?,?,?,?,'ACTIVE',?,?,?)`, c.ServiceID, c.KeyID, c.SecretHash, "hmac-sha256-v1", c.SecretHint, allowed, c.ExpiresAt, nullStr(c.CreatedBy))
	return err
}

// AuthCredential 认证所需信息（凭据 + 所属服务）。
type AuthCredential struct {
	Credential
	ServiceCode   string
	ServiceStatus string
	Expired       bool
}

func (q *Queries) GetCredentialForAuth(ctx context.Context, keyID string) (*AuthCredential, error) {
	var a AuthCredential
	var allowed sql.NullString
	var exp sql.NullTime
	err := q.db.QueryRowContext(ctx, `SELECT c.id, c.service_id, c.key_id, c.secret_hash, c.status, c.allowed_task_codes, c.expires_at,
		  c.expires_at IS NOT NULL AND c.expires_at <= NOW(3), s.service_code, s.status
		FROM job_service_credential c JOIN job_service s ON s.id=c.service_id WHERE c.key_id=?`, keyID).
		Scan(&a.ID, &a.ServiceID, &a.KeyID, &a.SecretHash, &a.Status, &allowed, &exp, &a.Expired, &a.ServiceCode, &a.ServiceStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.ExpiresAt = ptrTime(exp)
	if allowed.Valid {
		if err := json.Unmarshal([]byte(allowed.String), &a.AllowedTaskCodes); err != nil {
			return nil, err
		}
		if a.AllowedTaskCodes == nil {
			a.AllowedTaskCodes = []string{}
		}
	}
	return &a, nil
}

func (q *Queries) TouchCredential(ctx context.Context, id int64, ip string) error {
	_, err := q.db.ExecContext(ctx, "UPDATE job_service_credential SET last_used_at=NOW(3), last_used_ip=? WHERE id=?", nullStr(ip), id)
	return err
}

func (q *Queries) ListCredentials(ctx context.Context, serviceID int64) ([]*Credential, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT key_id, service_id, COALESCE(secret_hint,''), status, allowed_task_codes, expires_at, last_used_at, created_at
		FROM job_service_credential WHERE service_id=? ORDER BY id`, serviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Credential{}
	for rows.Next() {
		var c Credential
		var allowed sql.NullString
		var exp, used sql.NullTime
		if err := rows.Scan(&c.KeyID, &c.ServiceID, &c.SecretHint, &c.Status, &allowed, &exp, &used, &c.CreatedAt); err != nil {
			return nil, err
		}
		if allowed.Valid {
			_ = json.Unmarshal([]byte(allowed.String), &c.AllowedTaskCodes)
		}
		c.ExpiresAt, c.LastUsedAt = ptrTime(exp), ptrTime(used)
		out = append(out, &c)
	}
	return out, rows.Err()
}

// Audit 管理操作审计。
type Audit struct {
	OperatorSource, OperatorID, OperatorName, Action, Permission, ResourceType, ResourceID string
	RequestID, TraceID, ClientIP, Result, ErrorMessage                                     string
	Before, After                                                                          any
}

func (q *Queries) InsertAudit(ctx context.Context, a Audit) error {
	js := func(v any) any {
		if v == nil {
			return nil
		}
		b, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		return string(b)
	}
	_, err := q.db.ExecContext(ctx, `INSERT INTO job_operation_audit(operator_source, operator_id, operator_name, action, permission, resource_type, resource_id,
		request_id, trace_id, client_ip, before_data, after_data, result, error_message) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.OperatorSource, a.OperatorID, nullStr(a.OperatorName), a.Action, nullStr(a.Permission), a.ResourceType, nullStr(a.ResourceID),
		nullStr(a.RequestID), nullStr(a.TraceID), nullStr(a.ClientIP), js(a.Before), js(a.After), a.Result, nullStr(a.ErrorMessage))
	return err
}

func (q *Queries) UpdateService(ctx context.Context, id int64, name, desc, status *string) error {
	cur, err := q.GetService(ctx, id)
	if err != nil {
		return err
	}
	n, d, st := cur.ServiceName, "", cur.Status
	if cur.Description != nil {
		d = *cur.Description
	}
	if name != nil {
		n = *name
	}
	if desc != nil {
		d = *desc
	}
	if status != nil {
		st = *status
	}
	_, err = q.db.ExecContext(ctx, `UPDATE job_service SET service_name=?, description=?, status=? WHERE id=?`, n, nullStr(d), st, id)
	return err
}

func (q *Queries) RevokeCredential(ctx context.Context, keyID string) (int64, error) {
	res, err := q.db.ExecContext(ctx, `UPDATE job_service_credential SET status='REVOKED', revoked_at=UTC_TIMESTAMP(3) WHERE key_id=? AND status<>'REVOKED'`, keyID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (q *Queries) GetCredentialByKeyID(ctx context.Context, keyID string) (*Credential, error) {
	row := q.db.QueryRowContext(ctx, `SELECT key_id, service_id, COALESCE(secret_hint,''), status, allowed_task_codes, expires_at, last_used_at, created_at
		FROM job_service_credential WHERE key_id=?`, keyID)
	var c Credential
	var codes []byte
	var exp, last sql.NullTime
	if err := row.Scan(&c.KeyID, &c.ServiceID, &c.SecretHint, &c.Status, &codes, &exp, &last, &c.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if len(codes) > 0 {
		_ = json.Unmarshal(codes, &c.AllowedTaskCodes)
	}
	if exp.Valid {
		t := exp.Time.UTC()
		c.ExpiresAt = &t
	}
	if last.Valid {
		t := last.Time.UTC()
		c.LastUsedAt = &t
	}
	c.CreatedAt = c.CreatedAt.UTC()
	return &c, nil
}
