package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/GlassJob/job-center/internal/application/catalog"
	repo "github.com/GlassJob/job-center/internal/repository/mysql"
)

type Server struct {
	db      *sql.DB
	store   *repo.Store
	catalog *catalog.Service
}

func New(db *sql.DB, store *repo.Store, cat *catalog.Service) *Server {
	return &Server{db: db, store: store, catalog: cat}
}

func (s *Server) MountExecutor(mux *http.ServeMux) {
	mux.HandleFunc("/health/live", s.live)
	mux.HandleFunc("/health/ready", s.ready)
}

func (s *Server) MountAdmin(mux *http.ServeMux) {
	mux.HandleFunc("/health/live", s.live)
	mux.HandleFunc("/health/ready", s.ready)
	mux.HandleFunc("GET /v1/services", s.listServices)
	mux.HandleFunc("GET /v1/services/{id}", s.getService)
	mux.HandleFunc("GET /v1/services/{id}/instances", s.listInstances)
	mux.HandleFunc("GET /v1/tasks", s.listTasks)
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

func (s *Server) listServices(w http.ResponseWriter, r *http.Request) {
	page, limit := pageLimit(r)
	items, total, err := s.store.Q().ListServices(r.Context(), r.URL.Query().Get("q"), page, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "page": page, "limit": limit})
}

func (s *Server) getService(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	svc, err := s.store.Q().GetService(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, svc)
}

func (s *Server) listInstances(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	page, limit := pageLimit(r)
	items, total, err := s.store.Q().ListInstances(r.Context(), repo.InstanceFilter{
		ServiceID: id, Status: r.URL.Query().Get("status"), Page: page, Limit: limit,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "page": page, "limit": limit})
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	page, limit := pageLimit(r)
	var sid int64
	if v := r.URL.Query().Get("service_id"); v != "" {
		sid, _ = strconv.ParseInt(v, 10, 64)
	}
	items, total, err := s.store.Q().ListTasks(r.Context(), repo.TaskFilter{ServiceID: sid, Page: page, Limit: limit})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "page": page, "limit": limit})
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
