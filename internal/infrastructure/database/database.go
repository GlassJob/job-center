// Package database 负责 MySQL 连接池与迁移。
package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"
)

type PoolOptions struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// NormalizeDSN 强制 UTC：parseTime=true、loc=UTC、time_zone='+00:00'（所有 DATETIME(3) 列存 UTC）。
func NormalizeDSN(dsn string, multiStatements bool) (string, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return "", fmt.Errorf("parse dsn: %w", err)
	}
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["time_zone"] = "'+00:00'"
	cfg.MultiStatements = multiStatements
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	return cfg.FormatDSN(), nil
}

// Open 打开连接池并 Ping。
func Open(ctx context.Context, dsn string, o PoolOptions) (*sql.DB, error) {
	n, err := NormalizeDSN(dsn, false)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("mysql", n)
	if err != nil {
		return nil, err
	}
	if o.MaxOpenConns > 0 {
		db.SetMaxOpenConns(o.MaxOpenConns)
	}
	if o.MaxIdleConns > 0 {
		db.SetMaxIdleConns(o.MaxIdleConns)
	}
	if o.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(o.ConnMaxLifetime)
	}
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	return db, nil
}
