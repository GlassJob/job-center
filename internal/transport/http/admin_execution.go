package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/GlassJob/job-center/internal/application/apperr"
	appexec "github.com/GlassJob/job-center/internal/application/execution"
	repo "github.com/GlassJob/job-center/internal/repository/mysql"
)

type triggerBody struct {
	Params    json.RawMessage `json:"params"`
	Reason    string          `json:"reason"`
	AllowWait bool            `json:"allow_wait"`
}

type cancelBody struct {
	Reason       string `json:"reason"`
	GraceSeconds int    `json:"grace_seconds"`
}

func (s *Server) postTaskExecution(w http.ResponseWriter, r *http.Request) {
	if s.exec == nil {
		writeAPIError(w, r, http.StatusServiceUnavailable, "UNAVAILABLE", "execution service not wired", nil)
		return
	}
	op, _ := OperatorFrom(r.Context())
	taskID, err := strconv.ParseInt(r.PathValue("task_id"), 10, 64)
	if err != nil || taskID < 1 {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "bad task_id", nil)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	var body triggerBody
	if err := decodeJSONBody(r, &body); err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", err.Error(), nil)
		return
	}
	if len(body.Params) == 0 {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "params required", nil)
		return
	}
	out, code, err := s.exec.ManualTrigger(r.Context(), appexec.TriggerInput{
		TaskID: taskID, Params: body.Params, Reason: body.Reason, AllowWait: body.AllowWait,
		IdempotencyKey: key, Operator: *op, Client: "ADMIN",
	})
	if err != nil {
		writeExecErr(w, r, code, err)
		return
	}
	writeOK(w, r, code, out)
}

func (s *Server) listExecutions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var taskID, serviceID int64
	if v := q.Get("task_id"); v != "" {
		taskID, _ = strconv.ParseInt(v, 10, 64)
	}
	if v := q.Get("service_id"); v != "" {
		serviceID, _ = strconv.ParseInt(v, 10, 64)
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	items, err := s.store.Q().ListExecutions(r.Context(), taskID, serviceID, q.Get("status"), limit)
	if err != nil {
		writeAPIError(w, r, http.StatusInternalServerError, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return
	}
	if items == nil {
		items = []*repo.Execution{}
	}
	writeOK(w, r, http.StatusOK, map[string]any{"list": items, "next_cursor": nil})
}

func (s *Server) getExecution(w http.ResponseWriter, r *http.Request) {
	if s.exec == nil {
		writeAPIError(w, r, http.StatusServiceUnavailable, "UNAVAILABLE", "execution service not wired", nil)
		return
	}
	id := r.PathValue("execution_id")
	e, attempts, err := s.exec.GetDetail(r.Context(), id)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeAPIError(w, r, http.StatusNotFound, "NOT_FOUND", "execution not found", nil)
			return
		}
		writeAPIError(w, r, http.StatusInternalServerError, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return
	}
	if attempts == nil {
		attempts = []*repo.Attempt{}
	}
	// openapi ExecutionDetail = Execution fields + attempts（扁平，非 {execution,attempts}）
	out := map[string]any{
		"execution_id": e.ExecutionID, "task_id": e.TaskID, "service_id": e.ServiceID, "task_code": e.TaskCode,
		"trigger_type": e.TriggerType, "triggered_by": e.TriggeredBy, "params": e.Params,
		"idempotency_key": e.IdempotencyKey, "status": e.Status, "status_reason": e.StatusReason,
		"handler_version": e.HandlerVersion, "handler_major": e.HandlerMajor, "schema_hash": e.SchemaHash,
		"timeout_seconds": e.TimeoutSeconds, "retry_policy": e.RetryPolicy, "max_attempts": e.MaxAttempts,
		"attempt_count": e.AttemptCount, "current_attempt_id": e.CurrentAttemptID,
		"started_at": e.StartedAt, "finished_at": e.FinishedAt, "result": e.Result,
		"error_code": e.ErrorCode, "error_message": e.ErrorMessage,
		"created_at": e.CreatedAt, "updated_at": e.UpdatedAt,
		"attempts": attempts,
	}
	writeOK(w, r, http.StatusOK, out)
}

func (s *Server) postCancelExecution(w http.ResponseWriter, r *http.Request) {
	if s.exec == nil {
		writeAPIError(w, r, http.StatusServiceUnavailable, "UNAVAILABLE", "execution service not wired", nil)
		return
	}
	op, _ := OperatorFrom(r.Context())
	id := r.PathValue("execution_id")
	var body cancelBody
	_ = decodeJSONBody(r, &body)
	out, code, err := s.exec.Cancel(r.Context(), id, body.Reason, body.GraceSeconds, *op)
	if err != nil {
		writeExecErr(w, r, code, err)
		return
	}
	writeOK(w, r, code, out)
}

func (s *Server) listExecutionLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("execution_id")
	q := r.URL.Query()
	after, _ := strconv.ParseInt(q.Get("after_seq"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	items, next, err := s.store.Q().ListLogs(r.Context(), id, q.Get("attempt_id"), after, limit)
	if err != nil {
		writeAPIError(w, r, http.StatusInternalServerError, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return
	}
	if items == nil {
		items = []map[string]any{}
	}
	writeOK(w, r, http.StatusOK, map[string]any{"list": items, "next_after_seq": next, "has_more": len(items) > 0 && len(items) == limit})
}

func writeExecErr(w http.ResponseWriter, r *http.Request, status int, err error) {
	ae := apperr.As(err)
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	writeAPIError(w, r, status, ae.Code, ae.Message, ae.Details)
}
