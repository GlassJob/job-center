package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/GlassJob/job-center/internal/application/apperr"
	"github.com/GlassJob/job-center/internal/application/catalog"
	"github.com/GlassJob/job-center/internal/infrastructure/security"
	repo "github.com/GlassJob/job-center/internal/repository/mysql"
)

func (s *Server) adminListServices(w http.ResponseWriter, r *http.Request) {
	page, limit := pageLimit(r)
	kw := r.URL.Query().Get("keyword")
	if kw == "" {
		kw = r.URL.Query().Get("q")
	}
	items, total, err := s.catalog.ListServices(r.Context(), kw, page, limit)
	if err != nil {
		writeCatalogErr(w, r, err)
		return
	}
	if items == nil {
		items = []*repo.Service{}
	}
	writeOK(w, r, http.StatusOK, map[string]any{"list": items, "total": total, "page": page, "limit": limit})
}

func (s *Server) adminCreateService(w http.ResponseWriter, r *http.Request) {
	op, _ := OperatorFrom(r.Context())
	var in catalog.CreateServiceInput
	if err := decodeJSONBody(r, &in); err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", err.Error(), nil)
		return
	}
	actor := catalog.Actor{Client: r.Header.Get(security.HeaderClient), Operator: *op}
	svc, err := s.catalog.CreateService(r.Context(), actor, in)
	if err != nil {
		writeCatalogErr(w, r, err)
		return
	}
	writeOK(w, r, http.StatusCreated, svc)
}

func (s *Server) adminGetService(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("service_id"), 10, 64)
	if err != nil || id < 1 {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "bad service_id", nil)
		return
	}
	svc, err := s.catalog.GetService(r.Context(), id)
	if err != nil {
		writeCatalogErr(w, r, err)
		return
	}
	writeOK(w, r, http.StatusOK, svc)
}

func (s *Server) adminPatchService(w http.ResponseWriter, r *http.Request) {
	op, _ := OperatorFrom(r.Context())
	id, err := strconv.ParseInt(r.PathValue("service_id"), 10, 64)
	if err != nil || id < 1 {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "bad service_id", nil)
		return
	}
	var in catalog.UpdateServiceInput
	if err := decodeJSONBody(r, &in); err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", err.Error(), nil)
		return
	}
	actor := catalog.Actor{Client: r.Header.Get(security.HeaderClient), Operator: *op}
	svc, err := s.catalog.UpdateService(r.Context(), actor, id, in)
	if err != nil {
		writeCatalogErr(w, r, err)
		return
	}
	writeOK(w, r, http.StatusOK, svc)
}

func (s *Server) adminListCredentials(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("service_id"), 10, 64)
	if err != nil || id < 1 {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "bad service_id", nil)
		return
	}
	items, err := s.catalog.ListCredentials(r.Context(), id)
	if err != nil {
		writeCatalogErr(w, r, err)
		return
	}
	if items == nil {
		items = []*repo.Credential{}
	}
	writeOK(w, r, http.StatusOK, items)
}

func (s *Server) adminCreateCredential(w http.ResponseWriter, r *http.Request) {
	op, _ := OperatorFrom(r.Context())
	id, err := strconv.ParseInt(r.PathValue("service_id"), 10, 64)
	if err != nil || id < 1 {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "bad service_id", nil)
		return
	}
	var in catalog.CreateCredentialInput
	_ = decodeJSONBody(r, &in)
	actor := catalog.Actor{Client: r.Header.Get(security.HeaderClient), Operator: *op}
	out, err := s.catalog.CreateCredential(r.Context(), actor, id, in)
	if err != nil {
		writeCatalogErr(w, r, err)
		return
	}
	writeOK(w, r, http.StatusCreated, out)
}

func (s *Server) adminRevokeCredential(w http.ResponseWriter, r *http.Request) {
	op, _ := OperatorFrom(r.Context())
	keyID := r.PathValue("key_id")
	if keyID == "" {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "bad key_id", nil)
		return
	}
	actor := catalog.Actor{Client: r.Header.Get(security.HeaderClient), Operator: *op}
	if err := s.catalog.RevokeCredential(r.Context(), actor, keyID); err != nil {
		writeCatalogErr(w, r, err)
		return
	}
	writeOK(w, r, http.StatusOK, nil)
}

func (s *Server) adminListInstances(w http.ResponseWriter, r *http.Request) {
	page, limit := pageLimit(r)
	var sid int64
	if v := r.URL.Query().Get("service_id"); v != "" {
		sid, _ = strconv.ParseInt(v, 10, 64)
	}
	items, total, err := s.catalog.ListInstances(r.Context(), repo.InstanceFilter{
		ServiceID: sid, Status: r.URL.Query().Get("status"), Page: page, Limit: limit,
	})
	if err != nil {
		writeCatalogErr(w, r, err)
		return
	}
	if items == nil {
		items = []*repo.Instance{}
	}
	writeOK(w, r, http.StatusOK, map[string]any{"list": items, "total": total, "page": page, "limit": limit})
}

func (s *Server) adminListTasks(w http.ResponseWriter, r *http.Request) {
	page, limit := pageLimit(r)
	var sid int64
	if v := r.URL.Query().Get("service_id"); v != "" {
		sid, _ = strconv.ParseInt(v, 10, 64)
	}
	var enabled *bool
	if v := r.URL.Query().Get("enabled"); v == "true" || v == "1" {
		b := true
		enabled = &b
	} else if v == "false" || v == "0" {
		b := false
		enabled = &b
	}
	items, total, err := s.catalog.ListTasks(r.Context(), repo.TaskFilter{
		ServiceID: sid, Keyword: r.URL.Query().Get("keyword"), Enabled: enabled, Page: page, Limit: limit,
	})
	if err != nil {
		writeCatalogErr(w, r, err)
		return
	}
	if items == nil {
		items = []*repo.Task{}
	}
	writeOK(w, r, http.StatusOK, map[string]any{"list": items, "total": total, "page": page, "limit": limit})
}

func (s *Server) adminGetTask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("task_id"), 10, 64)
	if err != nil || id < 1 {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "bad task_id", nil)
		return
	}
	task, err := s.catalog.GetTask(r.Context(), id)
	if err != nil {
		writeCatalogErr(w, r, err)
		return
	}
	writeOK(w, r, http.StatusOK, task)
}

func writeCatalogErr(w http.ResponseWriter, r *http.Request, err error) {
	ae := apperr.As(err)
	status := http.StatusInternalServerError
	switch ae.Kind {
	case apperr.KindInvalid:
		status = http.StatusBadRequest
	case apperr.KindNotFound:
		status = http.StatusNotFound
	case apperr.KindConflict:
		status = http.StatusConflict
	case apperr.KindForbidden:
		status = http.StatusForbidden
	case apperr.KindUnauthenticated:
		status = http.StatusUnauthorized
	case apperr.KindUnavailable:
		status = http.StatusServiceUnavailable
	}
	if errors.Is(err, repo.ErrNotFound) {
		status = http.StatusNotFound
	}
	writeAPIError(w, r, status, ae.Code, ae.Message, ae.Details)
}
