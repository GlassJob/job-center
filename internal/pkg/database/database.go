package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type PoolOptions struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

func NormalizeDSN(dsn string, multiStatements bool) (string, error) {
	cfg, err := mysqldriver.ParseDSN(dsn)
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

// OpenPing 打开一个用于 migrate 的临时连接（不设池参数）。
func OpenPing(normalizedDSN string) (*sql.DB, error) {
	db, err := sql.Open("mysql", normalizedDSN)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// Open 返回 *gorm.DB；schema 仍由 golang-migrate 管理，不做 AutoMigrate。
func Open(ctx context.Context, dsn string, o PoolOptions) (*gorm.DB, error) {
	n, err := NormalizeDSN(dsn, false)
	if err != nil {
		return nil, err
	}
	gdb, err := gorm.Open(mysql.Open(n), &gorm.Config{
		SkipDefaultTransaction: true,
		PrepareStmt:            true,
		TranslateError:         true,
		Logger:                 logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, err
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, err
	}
	if o.MaxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(o.MaxOpenConns)
	}
	if o.MaxIdleConns > 0 {
		sqlDB.SetMaxIdleConns(o.MaxIdleConns)
	}
	if o.ConnMaxLifetime > 0 {
		sqlDB.SetConnMaxLifetime(o.ConnMaxLifetime)
	}
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	return gdb, nil
}
