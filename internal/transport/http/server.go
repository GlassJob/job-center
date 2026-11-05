package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/GlassJob/job-center/internal/application/catalog"
	appexec "github.com/GlassJob/job-center/internal/application/execution"
	"github.com/GlassJob/job-center/internal/config"
	"github.com/GlassJob/job-center/internal/infrastructure/security"
	repo "github.com/GlassJob/job-center/internal/repository/mysql"
)

type Server struct {
	db         *sql.DB
	store      *repo.Store
	catalog    *catalog.Service
	exec       *appexec.Service
	cfg        *config.Config
	nonces     *security.NonceCache
	defaultLoc *time.Location
	defaultTZ  string
}

func New(db *sql.DB, store *repo.Store, cat *catalog.Service, cfg *config.Config, defaultLoc *time.Location, defaultTZ string) *Server {
	ttl := 10 * time.Minute
	if cfg != nil && cfg.Security.NonceTTL.D() > 0 {
		ttl = cfg.Security.NonceTTL.D()
	}
	return &Server{
		db: db, store: store, catalog: cat, cfg: cfg,
		nonces: security.NewNonceCache(ttl),
		defaultLoc: defaultLoc, defaultTZ: defaultTZ,
	}
}

func (s *Server) SetExecution(exec *appexec.Service) { s.exec = exec }

func (s *Server) MountExecutor(mux *http.ServeMux) {
	mux.HandleFunc("/health/live", s.live)
	mux.HandleFunc("/health/ready", s.ready)
}

func (s *Server) MountAdmin(mux *http.ServeMux) {
	mux.HandleFunc("/health/live", s.live)
	mux.HandleFunc("/health/ready", s.ready)

	// 读接口：HMAC + X-JC-Operator 权限强制
	mux.Handle("GET /v1/admin/services", s.AdminAuth("job.service.view")(http.HandlerFunc(s.adminListServices)))
	mux.Handle("GET /v1/admin/services/{service_id}", s.AdminAuth("job.service.view")(http.HandlerFunc(s.adminGetService)))
	mux.Handle("GET /v1/admin/instances", s.AdminAuth("job.service.view")(http.HandlerFunc(s.adminListInstances)))
	mux.Handle("GET /v1/admin/tasks", s.AdminAuth("job.task.view")(http.HandlerFunc(s.adminListTasks)))
	mux.Handle("GET /v1/admin/tasks/{task_id}", s.AdminAuth("job.task.view")(http.HandlerFunc(s.adminGetTask)))

	// 写接口
	mux.Handle("POST /v1/admin/services", s.AdminAuth("job.credential.manage")(http.HandlerFunc(s.adminCreateService)))
	mux.Handle("PATCH /v1/admin/services/{service_id}", s.AdminAuth("job.credential.manage")(http.HandlerFunc(s.adminPatchService)))
	mux.Handle("GET /v1/admin/services/{service_id}/credentials", s.AdminAuth("job.credential.manage")(http.HandlerFunc(s.adminListCredentials)))
	mux.Handle("POST /v1/admin/services/{service_id}/credentials", s.AdminAuth("job.credential.manage")(http.HandlerFunc(s.adminCreateCredential)))
	mux.Handle("POST /v1/admin/credentials/{key_id}/revoke", s.AdminAuth("job.credential.manage")(http.HandlerFunc(s.adminRevokeCredential)))

	mux.Handle("PUT /v1/admin/tasks/{task_id}/enabled", s.AdminAuth("job.task.update")(http.HandlerFunc(s.putTaskEnabled)))
	mux.Handle("PUT /v1/admin/tasks/{task_id}/schedule", s.AdminAuth("job.schedule.update")(http.HandlerFunc(s.putTaskSchedule)))
	mux.Handle("POST /v1/admin/tasks/{task_id}/executions", s.AdminAuth("job.task.execute")(http.HandlerFunc(s.postTaskExecution)))
	mux.Handle("GET /v1/admin/executions", s.AdminAuth("job.execution.view")(http.HandlerFunc(s.listExecutions)))
	mux.Handle("GET /v1/admin/executions/{execution_id}", s.AdminAuth("job.execution.view")(http.HandlerFunc(s.getExecution)))
	mux.Handle("POST /v1/admin/executions/{execution_id}/cancel", s.AdminAuth("job.task.cancel")(http.HandlerFunc(s.postCancelExecution)))
	mux.Handle("GET /v1/admin/executions/{execution_id}/logs", s.AdminAuth("job.execution.log.view")(http.HandlerFunc(s.listExecutionLogs)))
}

func (s *Server) live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}





func pageLimit(r *http.Request) (int, int) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	return page, limit
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	if err == repo.ErrNotFound {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}
