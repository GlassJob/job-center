package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/GlassJob/job-center/internal/domain/task"
	"github.com/GlassJob/job-center/internal/infrastructure/security"
	repo "github.com/GlassJob/job-center/internal/repository/mysql"
)

func (s *Server) getTaskState(w http.ResponseWriter, r *http.Request) {
	taskID, err := strconv.ParseInt(r.PathValue("task_id"), 10, 64)
	if err != nil || taskID < 1 {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "bad task_id", nil)
		return
	}
	ts, err := s.store.Q().GetTaskStateStrict(r.Context(), taskID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeAPIError(w, r, http.StatusNotFound, "NOT_FOUND", "task not found", nil)
			return
		}
		writeAPIError(w, r, http.StatusInternalServerError, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, ts.Version))
	writeOK(w, r, http.StatusOK, ts)
}

func (s *Server) putTaskState(w http.ResponseWriter, r *http.Request) {
	op, _ := OperatorFrom(r.Context())
	taskID, err := strconv.ParseInt(r.PathValue("task_id"), 10, 64)
	if err != nil || taskID < 1 {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "bad task_id", nil)
		return
	}
	expectedVer, err := parseUintIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "invalid If-Match version", nil)
		return
	}
	var body struct {
		State json.RawMessage `json:"state"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", err.Error(), nil)
		return
	}
	cleanState, err := task.ValidateJSONObject(body.State, task.MaxStateBytes)
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "state: "+err.Error(), nil)
		return
	}

	opSource := r.Header.Get(security.HeaderClient)
	if opSource == "" {
		opSource = "ADMIN"
	}
	rid := strconv.FormatInt(taskID, 10)

	before, after, err := s.store.UpdateTaskStateAdminCAS(r.Context(), taskID, cleanState, expectedVer, op.ID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeAPIError(w, r, http.StatusNotFound, "NOT_FOUND", "task not found", nil)
			return
		}
		if errors.Is(err, repo.ErrVersionConflict) {
			var curVer uint64
			if before != nil {
				curVer = before.Version
			}
			_ = s.store.Q().InsertAudit(r.Context(), repo.Audit{
				OperatorSource: opSource, OperatorID: op.ID, OperatorName: op.Name,
				Action: "task.state.update", Permission: "job.task.update",
				ResourceType: "task", ResourceID: rid, RequestID: op.RequestID, ClientIP: op.ClientIP,
				Before: before, After: map[string]any{"state": cleanState, "if_match": expectedVer},
				Result: "FAILED", ErrorMessage: "version conflict",
			})
			writeAPIError(w, r, http.StatusConflict, "VERSION_CONFLICT", "If-Match version conflict", map[string]any{"current_version": curVer})
			return
		}
		writeAPIError(w, r, http.StatusInternalServerError, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return
	}

	_ = s.store.Q().InsertAudit(r.Context(), repo.Audit{
		OperatorSource: opSource, OperatorID: op.ID, OperatorName: op.Name,
		Action: "task.state.update", Permission: "job.task.update",
		ResourceType: "task", ResourceID: rid, RequestID: op.RequestID, ClientIP: op.ClientIP,
		Before: before, After: after, Result: "SUCCESS",
	})
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, after.Version))
	writeOK(w, r, http.StatusOK, after)
}

func (s *Server) deleteTaskState(w http.ResponseWriter, r *http.Request) {
	op, _ := OperatorFrom(r.Context())
	taskID, err := strconv.ParseInt(r.PathValue("task_id"), 10, 64)
	if err != nil || taskID < 1 {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "bad task_id", nil)
		return
	}
	expectedVer, err := parseUintIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "invalid If-Match version", nil)
		return
	}

	opSource := r.Header.Get(security.HeaderClient)
	if opSource == "" {
		opSource = "ADMIN"
	}
	rid := strconv.FormatInt(taskID, 10)

	before, after, err := s.store.ResetTaskStateAdminCAS(r.Context(), taskID, expectedVer, op.ID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeAPIError(w, r, http.StatusNotFound, "NOT_FOUND", "task not found", nil)
			return
		}
		if errors.Is(err, repo.ErrVersionConflict) {
			var curVer uint64
			if before != nil {
				curVer = before.Version
			}
			_ = s.store.Q().InsertAudit(r.Context(), repo.Audit{
				OperatorSource: opSource, OperatorID: op.ID, OperatorName: op.Name,
				Action: "task.state.reset", Permission: "job.task.update",
				ResourceType: "task", ResourceID: rid, RequestID: op.RequestID, ClientIP: op.ClientIP,
				Before: before, After: map[string]any{"if_match": expectedVer},
				Result: "FAILED", ErrorMessage: "version conflict",
			})
			writeAPIError(w, r, http.StatusConflict, "VERSION_CONFLICT", "If-Match version conflict", map[string]any{"current_version": curVer})
			return
		}
		writeAPIError(w, r, http.StatusInternalServerError, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return
	}

	_ = s.store.Q().InsertAudit(r.Context(), repo.Audit{
		OperatorSource: opSource, OperatorID: op.ID, OperatorName: op.Name,
		Action: "task.state.reset", Permission: "job.task.update",
		ResourceType: "task", ResourceID: rid, RequestID: op.RequestID, ClientIP: op.ClientIP,
		Before: before, After: after, Result: "SUCCESS",
	})
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, after.Version))
	writeOK(w, r, http.StatusOK, after)
}
