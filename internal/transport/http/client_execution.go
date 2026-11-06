package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	appexec "github.com/GlassJob/job-center/internal/application/execution"
	"github.com/GlassJob/job-center/internal/infrastructure/security"
	repo "github.com/GlassJob/job-center/internal/repository/mysql"
)

func formatExecutionDetail(e *repo.Execution, attempts []*repo.Attempt) map[string]any {
	if attempts == nil {
		attempts = []*repo.Attempt{}
	}
	return map[string]any{
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
}

// clientExecutionAllowed 归属与权限校验规则：
// 仅当满足以下任一条件时放行，否则返回 403 PERMISSION_DENIED：
// 1. 该 execution 由当前 client_id 通过 API 触发（trigger_type="API" 且 triggered_by="api:"+client_id 或 client_id）；
// 2. 该 execution 所属的 service_code/task_code 在当前 client_id 的 allowed_tasks 白名单内。
func clientExecutionAllowed(clientID string, allowed []string, serviceCode string, e *repo.Execution) bool {
	if clientID == "" || e == nil {
		return false
	}
	if e.TriggerType == "API" && (e.TriggeredBy == "api:"+clientID || e.TriggeredBy == clientID) {
		return true
	}
	return clientTaskAllowed(allowed, serviceCode, e.TaskCode)
}

func (s *Server) authorizeClientExecution(w http.ResponseWriter, r *http.Request, executionID string) (*repo.Execution, bool) {
	if s.store == nil {
		writeAPIError(w, r, http.StatusServiceUnavailable, "UNAVAILABLE", "storage not wired", nil)
		return nil, false
	}
	e, err := s.store.Q().GetExecution(r.Context(), executionID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeAPIError(w, r, http.StatusNotFound, "NOT_FOUND", "execution not found", nil)
			return nil, false
		}
		writeAPIError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return nil, false
	}
	serviceCode, err := s.store.Q().GetServiceCodeByID(r.Context(), e.ServiceID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeAPIError(w, r, http.StatusNotFound, "NOT_FOUND", "service not found", nil)
			return nil, false
		}
		writeAPIError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return nil, false
	}
	clientID := r.Header.Get(security.HeaderClient)
	ac := s.cfg.Security.AdminClients[clientID]
	if !clientExecutionAllowed(clientID, ac.AllowedTasks, serviceCode, e) {
		writeAPIError(w, r, http.StatusForbidden, "PERMISSION_DENIED", "client is not authorized for execution "+executionID, nil)
		return nil, false
	}
	return e, true
}

func (s *Server) issueClientTicketBrief(executionID, clientID string) *appexec.SSETicketBrief {
	op := security.Operator{
		ID:    "api:" + clientID,
		Name:  clientID,
		Perms: []string{"job.execution.log.view"},
	}
	raw, exp := s.tickets.Issue(executionID, op)
	return &appexec.SSETicketBrief{
		Ticket:    raw,
		ExpiresAt: exp.UTC().Format(time.RFC3339Nano),
		URL:       fmt.Sprintf("/v1/trigger/executions/%s/events?ticket=%s", executionID, raw),
	}
}

func (s *Server) getClientExecution(w http.ResponseWriter, r *http.Request) {
	if s.exec == nil {
		writeAPIError(w, r, http.StatusServiceUnavailable, "UNAVAILABLE", "execution service not wired", nil)
		return
	}
	id := r.PathValue("execution_id")
	e, ok := s.authorizeClientExecution(w, r, id)
	if !ok {
		return
	}
	attempts, err := s.store.Q().ListAttempts(r.Context(), id)
	if err != nil {
		writeAPIError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return
	}
	writeOK(w, r, http.StatusOK, formatExecutionDetail(e, attempts))
}

func (s *Server) postClientSSETicket(w http.ResponseWriter, r *http.Request) {
	if s.tickets == nil {
		writeAPIError(w, r, http.StatusServiceUnavailable, "UNAVAILABLE", "sse tickets not wired", nil)
		return
	}
	id := r.PathValue("execution_id")
	if _, ok := s.authorizeClientExecution(w, r, id); !ok {
		return
	}
	clientID := r.Header.Get(security.HeaderClient)
	brief := s.issueClientTicketBrief(id, clientID)
	writeOK(w, r, http.StatusCreated, map[string]any{
		"ticket":     brief.Ticket,
		"expires_at": brief.ExpiresAt,
		"url":        brief.URL,
	})
}

func (s *Server) sseOrClientHMAC(h http.HandlerFunc) http.Handler {
	auth := s.ClientAuth()(h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ticket") != "" {
			h.ServeHTTP(w, r)
			return
		}
		auth.ServeHTTP(w, r)
	})
}

func (s *Server) streamClientExecutionEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("execution_id")
	ticket := r.URL.Query().Get("ticket")
	if ticket != "" {
		if s.tickets == nil {
			writeAPIError(w, r, http.StatusServiceUnavailable, "UNAVAILABLE", "sse tickets not wired", nil)
			return
		}
		if _, err := s.tickets.Redeem(ticket, id); err != nil {
			writeAPIError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "invalid ticket", nil)
			return
		}
	} else {
		if _, ok := s.authorizeClientExecution(w, r, id); !ok {
			return
		}
	}
	s.serveExecutionEventStream(w, r, id)
}

func (s *Server) listClientExecutionLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("execution_id")
	if _, ok := s.authorizeClientExecution(w, r, id); !ok {
		return
	}
	s.listExecutionLogs(w, r)
}

func (s *Server) postClientCancelExecution(w http.ResponseWriter, r *http.Request) {
	if s.exec == nil {
		writeAPIError(w, r, http.StatusServiceUnavailable, "UNAVAILABLE", "execution service not wired", nil)
		return
	}
	id := r.PathValue("execution_id")
	if _, ok := s.authorizeClientExecution(w, r, id); !ok {
		return
	}
	clientID := r.Header.Get(security.HeaderClient)
	var body cancelBody
	_ = decodeJSONBody(r, &body)
	out, code, err := s.exec.CancelBy(r.Context(), id, body.Reason, body.GraceSeconds, "API", clientID)
	if err != nil {
		writeExecErr(w, r, code, err)
		return
	}
	writeOK(w, r, code, out)
}
