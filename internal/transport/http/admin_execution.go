package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/GlassJob/job-center/internal/application/apperr"
	appexec "github.com/GlassJob/job-center/internal/application/execution"
	"github.com/GlassJob/job-center/internal/infrastructure/security"
	repo "github.com/GlassJob/job-center/internal/repository/mysql"
)

type triggerBody struct {
	Params        json.RawMessage `json:"params"`
	Reason        string          `json:"reason"`
	AllowWait     bool            `json:"allow_wait"`
	WithSSETicket bool            `json:"with_sse_ticket"`
	CallbackURL   string          `json:"callback_url"`
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
	f := repo.ExecutionFilter{
		Status:      q.Get("status"),
		TriggerType: q.Get("trigger_type"),
	}
	if v := q.Get("task_id"); v != "" {
		f.TaskID, _ = strconv.ParseInt(v, 10, 64)
	}
	if v := q.Get("service_id"); v != "" {
		f.ServiceID, _ = strconv.ParseInt(v, 10, 64)
	}
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			f.From = &t
		} else if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.From = &t
		} else {
			writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "bad from", nil)
			return
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			f.To = &t
		} else if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.To = &t
		} else {
			writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "bad to", nil)
			return
		}
	}
	if c := q.Get("cursor"); c != "" {
		cid, err := strconv.ParseInt(c, 10, 64)
		if err != nil || cid < 1 {
			writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "bad cursor", nil)
			return
		}
		f.CursorID = cid
	}
	items, next, err := s.store.Q().ListExecutionsFiltered(r.Context(), f)
	if err != nil {
		writeAPIError(w, r, http.StatusInternalServerError, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return
	}
	if items == nil {
		items = []*repo.Execution{}
	}
	var nextCursor any
	if next > 0 {
		nextCursor = strconv.FormatInt(next, 10)
	}
	writeOK(w, r, http.StatusOK, map[string]any{"list": items, "next_cursor": nextCursor})
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
	writeOK(w, r, http.StatusOK, formatExecutionDetail(e, attempts))
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
	items, next, more, err := s.store.Q().ListLogsFiltered(r.Context(), id, q.Get("attempt_id"), q.Get("level"), after, limit)
	if err != nil {
		writeAPIError(w, r, http.StatusInternalServerError, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return
	}
	if items == nil {
		items = []map[string]any{}
	}
	writeOK(w, r, http.StatusOK, map[string]any{"list": items, "next_after_seq": next, "has_more": more})
}

func writeExecErr(w http.ResponseWriter, r *http.Request, status int, err error) {
	ae := apperr.As(err)
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	writeAPIError(w, r, status, ae.Code, ae.Message, ae.Details)
}

func clientTaskAllowed(allowed []string, serviceCode, taskCode string) bool {
	fullSlash := serviceCode + "/" + taskCode
	fullColon := serviceCode + ":" + taskCode
	for _, pat := range allowed {
		pat = strings.TrimSpace(pat)
		if pat == "*" || pat == fullSlash || pat == fullColon || pat == serviceCode+"/*" || pat == serviceCode+":*" {
			return true
		}
	}
	return false
}

func (s *Server) postAPITrigger(w http.ResponseWriter, r *http.Request) {
	if s.exec == nil {
		writeAPIError(w, r, http.StatusServiceUnavailable, "UNAVAILABLE", "execution service not wired", nil)
		return
	}
	serviceCode := r.PathValue("service_code")
	taskCode := r.PathValue("task_code")
	if serviceCode == "" || taskCode == "" {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "service_code and task_code required", nil)
		return
	}
	clientID := r.Header.Get(security.HeaderClient)
	ac := s.cfg.Security.AdminClients[clientID]
	if !clientTaskAllowed(ac.AllowedTasks, serviceCode, taskCode) {
		writeAPIError(w, r, http.StatusForbidden, "PERMISSION_DENIED", "client is not authorized for "+serviceCode+"/"+taskCode, nil)
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
	if strings.TrimSpace(body.CallbackURL) != "" {
		allowedHosts := ac.CallbackAllowedHosts
		if len(allowedHosts) == 0 && s.cfg != nil {
			allowedHosts = s.cfg.Security.CallbackAllowedHosts
		}
		if err := appexec.ValidateCallbackURL(body.CallbackURL, allowedHosts); err != nil {
			writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", err.Error(), nil)
			return
		}
	}
	sid, err := s.store.Q().GetServiceIDByCode(r.Context(), serviceCode)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeAPIError(w, r, http.StatusNotFound, "NOT_FOUND", "service not found", nil)
			return
		}
		writeAPIError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return
	}
	taskID, err := s.store.Q().GetTaskIDByCode(r.Context(), sid, taskCode)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			writeAPIError(w, r, http.StatusNotFound, "NOT_FOUND", "task not found", nil)
			return
		}
		writeAPIError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return
	}
	out, code, err := s.exec.APITrigger(r.Context(), appexec.TriggerInput{
		TaskID: taskID, Params: body.Params, Reason: body.Reason, AllowWait: body.AllowWait,
		CallbackURL: body.CallbackURL, IdempotencyKey: key, Client: clientID,
	})
	if err != nil {
		writeExecErr(w, r, code, err)
		return
	}
	if body.WithSSETicket && s.tickets != nil {
		out.SSETicket = s.issueClientTicketBrief(out.ExecutionID, clientID)
	}
	writeOK(w, r, code, out)
}
