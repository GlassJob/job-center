// Package archive 每日归档（database.md §7）：导出 JSONL.gz → 回读校验 → EXPORTED → 才删除 → SUCCEEDED。
package archive

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	repo "github.com/GlassJob/job-center/internal/repository/mysql"
)

const (
	ModeDelete    = "DELETE"    // 分批 DELETE
	ModePartition = "PARTITION" // 整月归档后 DROP PARTITION
	ModeKeep      = "KEEP"      // 仅归档不删（审计）
)

type Table struct {
	Name          string
	RetentionDays int
	Mode          string
}

type Options struct {
	Dir             string
	Tables          []Table
	DailyAt         string // HH:MM，服务器本地时区
	Location        *time.Location
	NodeID          string
	MaxDaysPerRun   int
	DeleteBatch     int
	FuturePartition int
}

// Sink 归档落地接口；V1 只实现本地文件。
type Sink interface {
	// Write 写入并原子落盘，返回 URI、字节数、SHA-256。
	Write(table string, day time.Time, write func(w io.Writer) (int64, error)) (uri string, bytes int64, sum string, rows int64, err error)
	// Verify 回读计数。
	Verify(uri string) (int64, string, error)
}

type LocalSink struct{ Dir string }

func (s LocalSink) path(table string, day time.Time) string {
	return filepath.Join(s.Dir, table, day.Format("2006"), day.Format("01"), fmt.Sprintf("%s-%s.jsonl.gz", table, day.Format("2006-01-02")))
}

func (s LocalSink) Write(table string, day time.Time, write func(w io.Writer) (int64, error)) (string, int64, string, int64, error) {
	final := s.path(table, day)
	if err := os.MkdirAll(filepath.Dir(final), 0o750); err != nil {
		return "", 0, "", 0, err
	}
	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return "", 0, "", 0, err
	}
	h := sha256.New()
	gz := gzip.NewWriter(io.MultiWriter(f, h))
	n, werr := write(gz)
	if werr == nil {
		werr = gz.Close()
	}
	if werr == nil {
		werr = f.Sync()
	}
	cerr := f.Close()
	if werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return "", 0, "", 0, werr
	}
	if err := os.Rename(tmp, final); err != nil {
		return "", 0, "", 0, err
	}
	st, err := os.Stat(final)
	if err != nil {
		return "", 0, "", 0, err
	}
	return final, st.Size(), hex.EncodeToString(h.Sum(nil)), n, nil
}

func (s LocalSink) Verify(uri string) (int64, string, error) {
	f, err := os.Open(uri)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	gz, err := gzip.NewReader(io.TeeReader(f, h))
	if err != nil {
		return 0, "", err
	}
	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	var n int64
	for sc.Scan() {
		if !json.Valid(sc.Bytes()) {
			return n, "", fmt.Errorf("invalid json line %d", n+1)
		}
		n++
	}
	if err := sc.Err(); err != nil {
		return n, "", err
	}
	_, _ = io.Copy(io.Discard, f)
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

type Archiver struct {
	store *repo.Store
	sink  Sink
	opt   Options
	log   *slog.Logger
	now   func() time.Time
}

func DefaultTables(logDays, execDays int) []Table {
	return []Table{
		{"job_execution_log", logDays, ModePartition},
		{"job_execution_event", execDays, ModeDelete},
		{"job_execution_attempt", execDays, ModeDelete},
		{"job_execution", execDays, ModeDelete},
		{"job_operation_audit", execDays, ModeKeep},
	}
}

func New(store *repo.Store, sink Sink, opt Options, log *slog.Logger) *Archiver {
	if opt.MaxDaysPerRun <= 0 {
		opt.MaxDaysPerRun = 31
	}
	if opt.DeleteBatch <= 0 {
		opt.DeleteBatch = 5000
	}
	if opt.FuturePartition <= 0 {
		opt.FuturePartition = 3
	}
	if opt.Location == nil {
		opt.Location = time.Local
	}
	if opt.DailyAt == "" {
		opt.DailyAt = "03:30"
	}
	return &Archiver{store: store, sink: sink, opt: opt, log: log, now: time.Now}
}

type Result struct {
	Table    string `json:"table"`
	Day      string `json:"day"`
	Status   string `json:"status"`
	Rows     int64  `json:"rows"`
	Deleted  int64  `json:"deleted"`
	FileURI  string `json:"file_uri,omitempty"`
	Error    string `json:"error,omitempty"`
}

// NextRun 下一个 DailyAt（本地时区）。
func (a *Archiver) NextRun(from time.Time) time.Time {
	var hh, mm int
	_, _ = fmt.Sscanf(a.opt.DailyAt, "%d:%d", &hh, &mm)
	l := from.In(a.opt.Location)
	t := time.Date(l.Year(), l.Month(), l.Day(), hh, mm, 0, 0, a.opt.Location)
	if !t.After(from) {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

func (a *Archiver) Run(ctx context.Context) {
	_ = a.store.Q().EnsureNodeLeaseRow(ctx, "archiver", a.opt.NodeID)
	for {
		next := a.NextRun(a.now())
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}
		held, err := a.store.Q().HoldNodeLease(ctx, "archiver", a.opt.NodeID, 2*time.Hour)
		if err != nil || !held {
			continue
		}
		if _, err := a.RunOnce(ctx); err != nil && ctx.Err() == nil {
			a.log.Error("archive run failed", "err", err)
		}
	}
}

// RunOnce 执行一轮归档（调用方负责租约）。单表单日失败只记 FAILED，不中断其它。
func (a *Archiver) RunOnce(ctx context.Context) ([]Result, error) {
	q := a.store.Q()
	if err := a.ensurePartitions(ctx); err != nil {
		a.log.Warn("ensure log partitions failed", "err", err)
	}
	var out []Result
	for _, t := range a.opt.Tables {
		cutoff := utcDay(a.now()).AddDate(0, 0, -t.RetentionDays)
		skip := []string{"SUCCEEDED"}
		if t.Mode != ModeDelete {
			skip = append(skip, "EXPORTED")
		}
		days, err := q.ArchiveCandidateDays(ctx, t.Name, cutoff, a.opt.MaxDaysPerRun, skip)
		if err != nil {
			return out, err
		}
		for _, d := range days {
			r := a.archiveDay(ctx, t, d)
			out = append(out, r)
		}
		if t.Mode == ModePartition {
			out = append(out, a.dropPartitions(ctx, t, cutoff)...)
		}
	}
	return out, nil
}

func utcDay(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

func (a *Archiver) archiveDay(ctx context.Context, t Table, day time.Time) Result {
	q := a.store.Q()
	res := Result{Table: t.Name, Day: day.Format("2006-01-02")}
	fail := func(err error) Result {
		_ = q.MarkArchiveFailed(ctx, t.Name, day, err.Error())
		res.Status, res.Error = "FAILED", err.Error()
		a.log.Error("archive day failed", "table", t.Name, "day", res.Day, "err", err)
		return res
	}
	run, err := q.GetArchiveRun(ctx, t.Name, day)
	if err != nil {
		res.Status, res.Error = "FAILED", err.Error()
		return res
	}
	if run != nil && run.Status == "SUCCEEDED" && t.Mode != ModeKeep {
		// 已删除过但又出现同日数据（迟到写入）：重置为重新导出到新文件名不安全，跳过并告警。
		a.log.Warn("archive day already SUCCEEDED but rows remain", "table", t.Name, "day", res.Day)
		res.Status = "SKIPPED"
		return res
	}
	if run == nil || run.Status != "EXPORTED" {
		if err := q.StartArchiveRun(ctx, t.Name, day); err != nil {
			return fail(err)
		}
		uri, bytes, sum, n, err := a.sink.Write(t.Name, day, func(w io.Writer) (int64, error) {
			enc := json.NewEncoder(w)
			var n int64
			err := q.StreamDay(ctx, t.Name, day, func(row map[string]any) error {
				n++
				return enc.Encode(row)
			})
			return n, err
		})
		if err != nil {
			return fail(fmt.Errorf("export: %w", err))
		}
		vn, vsum, err := a.sink.Verify(uri)
		if err != nil || vn != n || vsum != sum {
			if err == nil {
				err = fmt.Errorf("verify mismatch rows %d/%d sum %s/%s", vn, n, vsum, sum)
			}
			return fail(fmt.Errorf("verify: %w", err))
		}
		if err := q.MarkArchiveExported(ctx, t.Name, day, uri, n, bytes, sum); err != nil {
			return fail(err)
		}
		res.Rows, res.FileURI = n, uri
	} else {
		if run.RowCount != nil {
			res.Rows = *run.RowCount
		}
		if run.FileURI != nil {
			res.FileURI = *run.FileURI
		}
	}
	switch t.Mode {
	case ModeKeep:
		res.Status = "EXPORTED"
		_ = q.MarkArchiveSucceeded(ctx, t.Name, day, 0)
		res.Status = "SUCCEEDED"
	case ModeDelete:
		// 删除前再次确认行数没有增长超过导出量（防止导出后有迟到写入被误删）
		cur, err := q.CountDay(ctx, t.Name, day)
		if err != nil {
			return fail(err)
		}
		if cur > res.Rows {
			res.Status = "EXPORTED"
			res.Error = fmt.Sprintf("rows grew after export (%d > %d); delete deferred", cur, res.Rows)
			_, _ = q.Exec(ctx, `UPDATE job_archive_run SET status='FAILED', error_message=? WHERE table_name=? AND archive_date=?`, res.Error, t.Name, day.Format("2006-01-02"))
			return res
		}
		var deleted int64
		for {
			n, err := q.DeleteDayBatch(ctx, t.Name, day, a.opt.DeleteBatch)
			if err != nil {
				return fail(fmt.Errorf("delete: %w", err))
			}
			deleted += n
			if n < int64(a.opt.DeleteBatch) {
				break
			}
		}
		if err := q.MarkArchiveSucceeded(ctx, t.Name, day, deleted); err != nil {
			return fail(err)
		}
		res.Status, res.Deleted = "SUCCEEDED", deleted
	case ModePartition:
		res.Status = "EXPORTED" // 等整月 DROP PARTITION
	}
	return res
}

func partName(m time.Time) string { return "p" + m.Format("200601") }

// dropPartitions 对 cutoff 之前已整月结束、且其之前所有有数据的日均已导出的月份，DROP 对应分区。
func (a *Archiver) dropPartitions(ctx context.Context, t Table, cutoff time.Time) []Result {
	q := a.store.Q()
	parts, err := q.LogPartitions(ctx)
	if err != nil {
		return []Result{{Table: t.Name, Status: "FAILED", Error: err.Error()}}
	}
	var out []Result
	for _, p := range parts {
		if p == "pmax" || len(p) != 7 {
			continue
		}
		m, err := time.Parse("200601", p[1:])
		if err != nil {
			continue
		}
		end := m.AddDate(0, 1, 0)
		if end.After(cutoff) {
			break
		}
		n, err := q.CountUnarchivedDaysBefore(ctx, t.Name, end)
		if err != nil || n > 0 {
			break // 更早的月未全部导出时不得 DROP（分区可能包含更早数据）
		}
		if _, err := q.Exec(ctx, "ALTER TABLE job_execution_log DROP PARTITION "+p); err != nil {
			out = append(out, Result{Table: t.Name, Day: p, Status: "FAILED", Error: err.Error()})
			break
		}
		_, _ = q.Exec(ctx, `UPDATE job_archive_run SET status='SUCCEEDED', deleted_rows=row_count, finished_at=UTC_TIMESTAMP(3)
			WHERE table_name=? AND status='EXPORTED' AND archive_date < ?`, t.Name, end.Format("2006-01-02"))
		out = append(out, Result{Table: t.Name, Day: p, Status: "PARTITION_DROPPED"})
		a.log.Info("log partition dropped", "partition", p)
	}
	return out
}

// ensurePartitions 保证当前月之后 FuturePartition 个月的分区存在（从 pmax REORGANIZE）。
func (a *Archiver) ensurePartitions(ctx context.Context) error {
	q := a.store.Q()
	parts, err := q.LogPartitions(ctx)
	if err != nil || len(parts) == 0 {
		return err
	}
	have := map[string]bool{}
	var last time.Time
	for _, p := range parts {
		have[p] = true
		if m, err := time.Parse("200601", p[1:]); err == nil && len(p) == 7 && m.After(last) {
			last = m
		}
	}
	if !have["pmax"] {
		return nil
	}
	now := utcDay(a.now())
	cur := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i <= a.opt.FuturePartition; i++ {
		m := cur.AddDate(0, i, 0)
		if !m.After(last) || have[partName(m)] {
			continue
		}
		stmt := fmt.Sprintf("ALTER TABLE job_execution_log REORGANIZE PARTITION pmax INTO (PARTITION %s VALUES LESS THAN ('%s'), PARTITION pmax VALUES LESS THAN (MAXVALUE))",
			partName(m), m.AddDate(0, 1, 0).Format("2006-01-02 15:04:05"))
		if _, err := q.Exec(ctx, stmt); err != nil {
			return err
		}
		last = m
		a.log.Info("log partition added", "partition", partName(m))
	}
	return nil
}
