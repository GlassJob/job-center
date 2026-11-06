package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	repo "github.com/GlassJob/job-center/internal/repository/mysql"
)

func (s *Server) adminListAudits(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	f := repo.AuditFilter{
		OperatorID:   r.URL.Query().Get("operator_id"),
		ResourceType: r.URL.Query().Get("resource_type"),
		Limit:        limit,
	}
	if v := r.URL.Query().Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			f.From = &t
		} else if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.From = &t
		} else {
			writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "bad from", nil)
			return
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			f.To = &t
		} else if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.To = &t
		} else {
			writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "bad to", nil)
			return
		}
	}
	if c := r.URL.Query().Get("cursor"); c != "" {
		id, err := strconv.ParseInt(c, 10, 64)
		if err != nil || id < 1 {
			writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "bad cursor", nil)
			return
		}
		f.CursorID = id
	}
	items, next, err := s.store.Q().ListAudits(r.Context(), f)
	if err != nil {
		writeAPIError(w, r, http.StatusInternalServerError, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return
	}
	if items == nil {
		items = []*repo.AuditRow{}
	}
	var nextCursor any
	if next > 0 {
		nextCursor = strconv.FormatInt(next, 10)
	}
	writeOK(w, r, http.StatusOK, map[string]any{"list": items, "next_cursor": nextCursor})
}

// metrics 暴露归档滞后与基础表行数（Prometheus text）；无需鉴权，仅管理端口可达。
func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	var b strings.Builder
	writeGauge := func(name, help string, labels string, v float64) {
		b.WriteString("# HELP " + name + " " + help + "\n")
		b.WriteString("# TYPE " + name + " gauge\n")
		if labels != "" {
			fmt.Fprintf(&b, "%s{%s} %g\n", name, labels, v)
		} else {
			fmt.Fprintf(&b, "%s %g\n", name, v)
		}
	}

	tables := []string{"job_execution_log", "job_execution", "job_execution_attempt", "job_execution_event", "job_operation_audit"}
	for _, table := range tables {
		var n int64
		_ = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n)
		writeGauge("job_center_table_rows", "Approximate row count", fmt.Sprintf("table=%q", table), float64(n))
		var dataLen float64
		_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(DATA_LENGTH,0)+COALESCE(INDEX_LENGTH,0) FROM information_schema.TABLES
			WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?`, table).Scan(&dataLen)
		writeGauge("job_center_table_bytes", "DATA_LENGTH+INDEX_LENGTH", fmt.Sprintf("table=%q", table), dataLen)

		// 归档滞后：保留期之前仍未 EXPORTED/SUCCEEDED 的自然日数
		retention := 365
		if table == "job_execution_log" && s.cfg != nil {
			if s.cfg.Archive.LogRetentionDays > 0 {
				retention = s.cfg.Archive.LogRetentionDays
			}
		} else if s.cfg != nil && s.cfg.Archive.ExecRetentionDays > 0 {
			retention = s.cfg.Archive.ExecRetentionDays
		}
		before := time.Now().UTC().AddDate(0, 0, -retention)
		lag, err := s.store.Q().CountUnarchivedDaysBefore(ctx, table, before)
		if err != nil {
			lag = -1
		}
		writeGauge("job_center_archive_lag_days", "Unarchived UTC days older than retention", fmt.Sprintf("table=%q", table), float64(lag))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
}
