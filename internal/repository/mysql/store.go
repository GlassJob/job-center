// Package mysql 是 Job Center 的 MySQL 持久化实现（Repository）。
// 约定：所有时间比较使用数据库 NOW(3)（UTC），不使用应用服务器时钟。
package mysql

import (
	"context"
	"database/sql"
	"errors"
	"time"

	driver "github.com/go-sql-driver/mysql"
)

var (
	ErrNotFound  = errors.New("not found")
	ErrDuplicate       = errors.New("duplicate key")
	ErrVersionConflict = errors.New("version conflict")
)

// DBTX 由 *sql.DB 与 *sql.Tx 实现。
type DBTX interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

type Store struct{ db *sql.DB }

func New(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) DB() *sql.DB { return s.db }

// Q 返回非事务查询器。
func (s *Store) Q() *Queries { return &Queries{db: s.db} }

// InTx 在事务中执行 fn；遇到死锁/锁等待超时自动重试（最多 3 次）。
func (s *Store) InTx(ctx context.Context, fn func(q *Queries) error) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = s.inTxOnce(ctx, fn)
		if err == nil || !IsRetryable(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 20 * time.Millisecond):
		}
	}
	return err
}

func (s *Store) inTxOnce(ctx context.Context, fn func(q *Queries) error) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(&Queries{db: tx}); err != nil {
		return err
	}
	return tx.Commit()
}

type Queries struct{ db DBTX }

func mysqlCode(err error) uint16 {
	var me *driver.MySQLError
	if errors.As(err, &me) {
		return me.Number
	}
	return 0
}

// IsRetryable 死锁(1213)、锁等待超时(1205)。
func IsRetryable(err error) bool { c := mysqlCode(err); return c == 1213 || c == 1205 }

// IsDuplicate 唯一键冲突(1062)。
func IsDuplicate(err error) bool { return mysqlCode(err) == 1062 || errors.Is(err, ErrDuplicate) }

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func ptrStr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	return &ns.String
}

func ptrTime(nt sql.NullTime) *time.Time {
	if !nt.Valid {
		return nil
	}
	t := nt.Time.UTC()
	return &t
}
