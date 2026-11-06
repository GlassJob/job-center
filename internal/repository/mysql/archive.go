package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type ArchiveRun struct {
	ID       int64
	Table    string
	Day      time.Time
	Status   string
	FileURI  *string
	Checksum *string
	RowCount *int64
}

// ArchiveCandidateDays created_at < cutoff 的不同 UTC 自然日（升序）。
// skipStatuses：已处于这些状态的日不再返回。
func (q *Queries) ArchiveCandidateDays(ctx context.Context, table string, cutoff time.Time, limit int, skipStatuses []string) ([]time.Time, error) {
	skip := "''"
	args := []any{cutoff, table}
	for i, s := range skipStatuses {
		if i == 0 {
			skip = "?"
		} else {
			skip += ",?"
		}
		args = append(args, s)
	}
	args = append(args, limit)
	rows, err := q.db.QueryContext(ctx, fmt.Sprintf(`SELECT x.d FROM (SELECT DISTINCT DATE(created_at) d FROM %s WHERE created_at < ?) x
		LEFT JOIN job_archive_run r ON r.table_name=? AND r.archive_date=x.d
		WHERE r.status IS NULL OR r.status NOT IN (%s) ORDER BY x.d LIMIT ?`, table, skip), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []time.Time
	for rows.Next() {
		var d time.Time
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d.UTC())
	}
	return out, rows.Err()
}

func (q *Queries) GetArchiveRun(ctx context.Context, table string, day time.Time) (*ArchiveRun, error) {
	var r ArchiveRun
	var f, c sql.NullString
	var n sql.NullInt64
	err := q.db.QueryRowContext(ctx, `SELECT id, table_name, archive_date, status, file_uri, checksum, row_count FROM job_archive_run WHERE table_name=? AND archive_date=?`,
		table, day.Format("2006-01-02")).Scan(&r.ID, &r.Table, &r.Day, &r.Status, &f, &c, &n)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.FileURI, r.Checksum = ptrStr(f), ptrStr(c)
	if n.Valid {
		r.RowCount = &n.Int64
	}
	return &r, nil
}

func (q *Queries) StartArchiveRun(ctx context.Context, table string, day time.Time) error {
	_, err := q.db.ExecContext(ctx, `INSERT INTO job_archive_run(table_name, archive_date, status) VALUES (?,?,'RUNNING')
		ON DUPLICATE KEY UPDATE status=IF(status IN ('SUCCEEDED','EXPORTED'), status, 'RUNNING'), attempts=attempts+1, error_message=NULL`,
		table, day.Format("2006-01-02"))
	return err
}

func (q *Queries) MarkArchiveExported(ctx context.Context, table string, day time.Time, uri string, rowsN, bytes int64, sum string) error {
	_, err := q.db.ExecContext(ctx, `UPDATE job_archive_run SET status='EXPORTED', file_uri=?, row_count=?, file_bytes=?, checksum=?, exported_at=UTC_TIMESTAMP(3)
		WHERE table_name=? AND archive_date=?`, uri, rowsN, bytes, sum, table, day.Format("2006-01-02"))
	return err
}

func (q *Queries) MarkArchiveSucceeded(ctx context.Context, table string, day time.Time, deleted int64) error {
	_, err := q.db.ExecContext(ctx, `UPDATE job_archive_run SET status='SUCCEEDED', deleted_rows=?, finished_at=UTC_TIMESTAMP(3)
		WHERE table_name=? AND archive_date=? AND status='EXPORTED'`, deleted, table, day.Format("2006-01-02"))
	return err
}

func (q *Queries) MarkArchiveFailed(ctx context.Context, table string, day time.Time, msg string) error {
	if len(msg) > 1900 {
		msg = msg[:1900]
	}
	_, err := q.db.ExecContext(ctx, `UPDATE job_archive_run SET status='FAILED', error_message=?, finished_at=UTC_TIMESTAMP(3)
		WHERE table_name=? AND archive_date=? AND status='RUNNING'`, msg, table, day.Format("2006-01-02"))
	return err
}

// StreamDay 按主键顺序流式读取 [day, day+1) 的行（列名 → 文本值/NULL）。
func (q *Queries) StreamDay(ctx context.Context, table string, day time.Time, fn func(row map[string]any) error) error {
	rows, err := q.db.QueryContext(ctx, fmt.Sprintf(`SELECT * FROM %s WHERE created_at >= ? AND created_at < ? ORDER BY id`, table), day, day.AddDate(0, 0, 1))
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	vals := make([]sql.RawBytes, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			if vals[i] == nil {
				m[c] = nil
			} else {
				m[c] = string(vals[i])
			}
		}
		if err := fn(m); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (q *Queries) CountDay(ctx context.Context, table string, day time.Time) (int64, error) {
	var n int64
	err := q.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE created_at >= ? AND created_at < ?`, table), day, day.AddDate(0, 0, 1)).Scan(&n)
	return n, err
}

// DeleteDayBatch 分批删除 [day, day+1)，返回本批删除行数。
func (q *Queries) DeleteDayBatch(ctx context.Context, table string, day time.Time, batch int) (int64, error) {
	res, err := q.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE created_at >= ? AND created_at < ? ORDER BY id LIMIT %d`, table, batch), day, day.AddDate(0, 0, 1))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountUnarchivedDaysBefore created_at < before 仍有数据但未 EXPORTED/SUCCEEDED 的日数。
func (q *Queries) CountUnarchivedDaysBefore(ctx context.Context, table string, before time.Time) (int, error) {
	var n int
	err := q.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM (SELECT DISTINCT DATE(created_at) d FROM %s WHERE created_at < ?) x
		LEFT JOIN job_archive_run r ON r.table_name=? AND r.archive_date=x.d
		WHERE r.status IS NULL OR r.status NOT IN ('EXPORTED','SUCCEEDED')`, table), before, table).Scan(&n)
	return n, err
}

// LogPartitions 日志表现有分区（name → LESS THAN 描述）。
func (q *Queries) LogPartitions(ctx context.Context) ([]string, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT PARTITION_NAME FROM information_schema.PARTITIONS
		WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='job_execution_log' AND PARTITION_NAME IS NOT NULL ORDER BY PARTITION_ORDINAL_POSITION`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (q *Queries) Exec(ctx context.Context, stmt string, args ...any) (int64, error) {
	res, err := q.db.ExecContext(ctx, stmt, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

