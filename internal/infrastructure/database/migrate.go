package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"sort"
	"strings"
)

var migName = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+)\.sql$`)

type Migration struct {
	Version, Name, SQL, Checksum string
}

// LoadMigrations 读取 FS 中的 NNNN_name.sql，按版本排序。
func LoadMigrations(fsys fs.FS) ([]Migration, error) {
	ents, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var out []Migration
	for _, e := range ents {
		m := migName.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		b, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		out = append(out, Migration{Version: m[1], Name: m[2], SQL: string(b), Checksum: hex.EncodeToString(sum[:])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// MigrateUp 在独立连接（multiStatements）上依次执行未应用的迁移；用 GET_LOCK 防止多节点并发。
// 迁移文件本身只含 CREATE ... IF NOT EXISTS 等幂等语句，中途失败可重跑。
func MigrateUp(ctx context.Context, dsn string, fsys fs.FS, log *slog.Logger) ([]string, error) {
	migs, err := LoadMigrations(fsys)
	if err != nil {
		return nil, err
	}
	n, err := NormalizeDSN(dsn, true)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("mysql", n)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK('job_center_migrate', 60)").Scan(&got); err != nil || !got.Valid || got.Int64 != 1 {
		return nil, fmt.Errorf("acquire migration lock: %v", err)
	}
	defer conn.ExecContext(context.Background(), "DO RELEASE_LOCK('job_center_migrate')")

	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return nil, err
	}
	var done []string
	for _, m := range migs {
		if _, ok := applied[m.Version]; ok {
			continue
		}
		log.Info("applying migration", "version", m.Version, "name", m.Name)
		if _, err := conn.ExecContext(ctx, m.SQL); err != nil {
			return done, fmt.Errorf("migration %s_%s: %w", m.Version, m.Name, err)
		}
		if _, err := conn.ExecContext(ctx,
			"INSERT INTO job_schema_migration(version, description, checksum) VALUES (?,?,?) ON DUPLICATE KEY UPDATE checksum=VALUES(checksum)",
			m.Version, m.Name, m.Checksum); err != nil {
			return done, err
		}
		done = append(done, m.Version)
	}
	return done, nil
}

type queryer interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
}

func appliedVersions(ctx context.Context, q queryer) (map[string]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT version, COALESCE(checksum,'') FROM job_schema_migration")
	if err != nil {
		var me interface{ Error() string }
		if errors.As(err, &me) && strings.Contains(err.Error(), "1146") { // table doesn't exist
			return map[string]string{}, nil
		}
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var v, c string
		if err := rows.Scan(&v, &c); err != nil {
			return nil, err
		}
		out[v] = c
	}
	return out, rows.Err()
}

// CheckSchema 确认全部内嵌迁移已应用（serve 启动时调用；不自动迁移，见 docs/database.md §6）。
func CheckSchema(ctx context.Context, db *sql.DB, fsys fs.FS) error {
	migs, err := LoadMigrations(fsys)
	if err != nil {
		return err
	}
	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return err
	}
	var missing []string
	for _, m := range migs {
		if c, ok := applied[m.Version]; !ok {
			missing = append(missing, m.Version)
		} else if c != "" && c != m.Checksum {
			return fmt.Errorf("migration %s checksum mismatch (db=%s… file=%s…)", m.Version, c[:12], m.Checksum[:12])
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("database schema not up to date, missing migrations %v: run `job-center migrate up`", missing)
	}
	return nil
}
