package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/GlassJob/job-center/internal/domain/sse"
	"github.com/GlassJob/job-center/internal/domain/version"
	"github.com/GlassJob/job-center/internal/infrastructure/security"
	repo "github.com/GlassJob/job-center/internal/repository/mysql"
)

func (s *Server) adminOverview(w http.ResponseWriter, r *http.Request) {
	now := time.Now().In(s.defaultLoc)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.defaultLoc).UTC()
	end := start.Add(24 * time.Hour)
	o, err := s.store.Q().Overview(r.Context(), start, end)
	if err != nil {
		writeAPIError(w, r, http.StatusInternalServerError, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return
	}
	mismatch, _ := s.store.Q().CountVersionMismatch(r.Context())
	tz := s.defaultTZ
	if tz == "" {
		tz = s.defaultLoc.String()
	}
	writeOK(w, r, http.StatusOK, map[string]any{
		"service_count": o.ServiceCount, "online_instance_count": o.OnlineInstanceCount,
		"task_count": o.TaskCount, "today_execution_count": o.TodayExecutionCount,
		"today_success_rate": o.TodaySuccessRate, "today_failed_count": o.TodayFailedCount,
		"running_count": o.RunningCount, "version_mismatch_instance_count": mismatch,
		"today_timezone": tz,
	})
}

func (s *Server) postSSETicket(w http.ResponseWriter, r *http.Request) {
	if s.tickets == nil {
		writeAPIError(w, r, http.StatusServiceUnavailable, "UNAVAILABLE", "sse tickets not wired", nil)
		return
	}
	op, _ := OperatorFrom(r.Context())
	var body struct {
		ExecutionID string `json:"execution_id"`
	}
	if err := decodeJSONBody(r, &body); err != nil || body.ExecutionID == "" {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "execution_id required", nil)
		return
	}
	if _, err := s.store.Q().GetExecution(r.Context(), body.ExecutionID); err != nil {
		if err == repo.ErrNotFound {
			writeAPIError(w, r, http.StatusNotFound, "NOT_FOUND", "execution not found", nil)
			return
		}
		writeAPIError(w, r, http.StatusInternalServerError, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return
	}
	raw, exp := s.tickets.Issue(body.ExecutionID, *op)
	url := fmt.Sprintf("/v1/admin/executions/%s/events?ticket=%s", body.ExecutionID, raw)
	writeOK(w, r, http.StatusCreated, map[string]any{
		"ticket": raw, "expires_at": exp.UTC().Format(time.RFC3339Nano), "url": url,
	})
}

func (s *Server) streamExecutionEvents(w http.ResponseWriter, r *http.Request) {
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
		// HMAC path already applied by middleware when ticket absent.
		if _, ok := OperatorFrom(r.Context()); !ok {
			writeAPIError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "hmac or ticket required", nil)
			return
		}
	}
	curRaw := r.Header.Get("Last-Event-ID")
	if curRaw == "" {
		curRaw = r.URL.Query().Get("cursor")
	}
	cur, err := sse.Parse(curRaw)
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "bad cursor", nil)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, r, http.StatusInternalServerError, "UNAVAILABLE", "streaming unsupported", nil)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	deadline := time.Now().Add(30 * time.Minute)
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	poll := time.NewTicker(500 * time.Millisecond)
	defer poll.Stop()
	progThrottle := sse.NewProgressThrottle(time.Second)

	writeEvent := func(event string, idStr string, data any) bool {
		b, _ := json.Marshal(data)
		if idStr != "" {
			fmt.Fprintf(w, "id: %s\n", idStr)
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		flusher.Flush()
		return true
	}

	emitProgress := func(prog *repo.ProgressSnap) {
		if prog == nil || prog.UpdatedAt == nil {
			return
		}
		key := prog.UpdatedAt.UTC().Format(time.RFC3339Nano)
		if prog.Percent != nil {
			key += fmt.Sprintf(":%.6f", *prog.Percent)
		}
		if prog.Message != nil {
			key += ":" + *prog.Message
		}
		if !progThrottle.Allow(time.Now(), prog.AttemptNumber, key) {
			return
		}
		writeEvent("progress", fmt.Sprintf("%d:%d:%d", cur.EventID, prog.AttemptNumber, cur.Seq), map[string]any{
			"attempt_id": prog.AttemptID, "attempt_number": prog.AttemptNumber,
			"percent": prog.Percent, "message": prog.Message, "updated_at": prog.UpdatedAt.UTC(),
		})
	}

	emitCatchUp := func() (terminal bool, err error) {
		evs, err := s.store.Q().ListEventsAfter(r.Context(), id, cur.EventID, 200)
		if err != nil {
			return false, err
		}
		for _, e := range evs {
			attemptN := cur.AttemptNumber
			payload := map[string]any{"event_type": e.EventType}
			if e.From != nil {
				payload["from"] = *e.From
			}
			if e.To != nil {
				payload["to"] = *e.To
			}
			if len(e.Payload) > 0 {
				var extra map[string]any
				if json.Unmarshal(e.Payload, &extra) == nil {
					for k, v := range extra {
						payload[k] = v
					}
				}
			}
			ex, _ := s.store.Q().GetExecution(r.Context(), id)
			if ex != nil {
				payload["execution_status"] = ex.Status
			}
			idStr := fmt.Sprintf("%d:%d:%d", e.ID, attemptN, cur.Seq)
			writeEvent("state", idStr, payload)
			cur.EventID = e.ID
		}
		logs, err := s.store.Q().ListLogsAfterCursor(r.Context(), id, cur.AttemptNumber, cur.Seq, 200)
		if err != nil {
			return false, err
		}
		for _, l := range logs {
			idStr := fmt.Sprintf("%d:%d:%d", cur.EventID, l.AttemptNumber, l.Seq)
			writeEvent("log", idStr, l)
			cur.AttemptNumber, cur.Seq = l.AttemptNumber, l.Seq
		}
		if prog, _ := s.store.Q().CurrentProgress(r.Context(), id); prog != nil {
			emitProgress(prog)
		}
		ex, err := s.store.Q().GetExecution(r.Context(), id)
		if err != nil {
			return false, err
		}
		switch ex.Status {
		case "SUCCEEDED", "FAILED", "TIMED_OUT", "CANCELLED", "SKIPPED":
			// 再拉一轮确保日志补齐
			more, _ := s.store.Q().ListLogsAfterCursor(r.Context(), id, cur.AttemptNumber, cur.Seq, 1)
			if len(more) == 0 {
				writeEvent("end", "", map[string]any{"execution_status": ex.Status})
				return true, nil
			}
		}
		return false, nil
	}

	if done, err := emitCatchUp(); err != nil {
		fmt.Fprintf(w, "event: error\ndata: {\"message\":%q}\n\n", err.Error())
		flusher.Flush()
		return
	} else if done {
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
			if time.Now().After(deadline) {
				writeEvent("end", "", map[string]any{"execution_status": "CONNECTION_EXPIRED"})
				return
			}
		case <-poll.C:
			done, err := emitCatchUp()
			if err != nil {
				fmt.Fprintf(w, "event: error\ndata: {\"message\":%q}\n\n", err.Error())
				flusher.Flush()
				return
			}
			if done {
				return
			}
		}
	}
}

// sseOrHMAC 票据直连时跳过 HMAC；否则走 AdminAuth。
func (s *Server) sseOrHMAC(perm string, h http.HandlerFunc) http.Handler {
	auth := s.AdminAuth(perm)(h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ticket") != "" {
			h.ServeHTTP(w, r)
			return
		}
		auth.ServeHTTP(w, r)
	})
}

func (s *Server) adminListVersions(w http.ResponseWriter, r *http.Request) {
	env := r.URL.Query().Get("environment")
	if env == "" {
		env = "dev"
	}
	var serviceID int64
	if v := r.URL.Query().Get("service_id"); v != "" {
		serviceID, _ = strconv.ParseInt(v, 10, 64)
	}
	refs, err := s.store.Q().ListServiceRefs(r.Context(), serviceID)
	if err != nil {
		writeAPIError(w, r, http.StatusInternalServerError, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return
	}
	out := make([]map[string]any, 0, len(refs))
	for _, ref := range refs {
		item, err := s.reconcileOne(r, ref.ID, ref.Code, env)
		if err != nil {
			writeAPIError(w, r, http.StatusInternalServerError, "STORAGE_UNAVAILABLE", err.Error(), nil)
			return
		}
		out = append(out, item)
	}
	writeOK(w, r, http.StatusOK, out)
}

func (s *Server) reconcileOne(r *http.Request, serviceID int64, code, env string) (map[string]any, error) {
	q := s.store.Q()
	expDep, err := q.GetExpectedDeployment(r.Context(), serviceID, env)
	if err != nil {
		return nil, err
	}
	last, err := q.GetLastDeployment(r.Context(), serviceID, env)
	if err != nil {
		return nil, err
	}
	live, err := q.ListLiveInstances(r.Context(), serviceID, env)
	if err != nil {
		return nil, err
	}
	missingN, err := q.CountUncoveredDefinitions(r.Context(), serviceID, env)
	if err != nil {
		return nil, err
	}
	var exp *version.Expected
	var expectedVersion, expectedCommit any
	if expDep != nil {
		exp = &version.Expected{Version: expDep.DeploymentVersion, GitCommit: expDep.GitCommit, DeployedAt: expDep.DeployedAt}
		expectedVersion, expectedCommit = expDep.DeploymentVersion, expDep.GitCommit
	} else {
		expectedVersion, expectedCommit = nil, nil
	}
	running := make([]version.Running, len(live))
	for i, inst := range live {
		running[i] = version.Running{AppVersion: inst.AppVersion, GitCommit: inst.GitCommit, StartedAt: inst.StartedAt}
	}
	state, flags := version.State(exp, running, missingN > 0 && exp != nil)
	for i := range live {
		live[i].MatchesExpected = flags[i]
	}
	return map[string]any{
		"service_id": serviceID, "service_code": code, "environment": env,
		"expected_version": expectedVersion, "expected_git_commit": expectedCommit,
		"last_deployment": last, "running": live, "state": state,
	}, nil
}

func (s *Server) adminListDeployments(w http.ResponseWriter, r *http.Request) {
	var serviceID int64
	if v := r.URL.Query().Get("service_id"); v != "" {
		serviceID, _ = strconv.ParseInt(v, 10, 64)
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.store.Q().ListDeployments(r.Context(), serviceID, r.URL.Query().Get("environment"), limit)
	if err != nil {
		writeAPIError(w, r, http.StatusInternalServerError, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return
	}
	writeOK(w, r, http.StatusOK, items)
}

func (s *Server) postPublicDeployment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ServiceCode       string  `json:"service_code"`
		Environment       string  `json:"environment"`
		DeploymentVersion string  `json:"deployment_version"`
		GitCommit         string  `json:"git_commit"`
		BuildID           string  `json:"build_id"`
		BuildURL          string  `json:"build_url"`
		DeploymentStatus  string  `json:"deployment_status"`
		SetExpected       *bool   `json:"set_expected"`
		DeployedAt        *string `json:"deployed_at"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", err.Error(), nil)
		return
	}
	if body.ServiceCode == "" || body.DeploymentVersion == "" || body.GitCommit == "" || body.BuildID == "" || body.DeploymentStatus == "" {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "required fields missing", nil)
		return
	}
	if body.Environment == "" {
		body.Environment = "dev"
	}
	setExp := true
	if body.SetExpected != nil {
		setExp = *body.SetExpected
	}
	sid, err := s.store.Q().GetServiceIDByCode(r.Context(), body.ServiceCode)
	if err != nil {
		if err == repo.ErrNotFound {
			writeAPIError(w, r, http.StatusNotFound, "NOT_FOUND", "service not found", nil)
			return
		}
		writeAPIError(w, r, http.StatusInternalServerError, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return
	}
	clientID := r.Header.Get(security.HeaderClient)
	var deployedAt *time.Time
	if body.DeployedAt != nil && *body.DeployedAt != "" {
		if t, e := time.Parse(time.RFC3339Nano, *body.DeployedAt); e == nil {
			deployedAt = &t
		} else if t, e := time.Parse(time.RFC3339, *body.DeployedAt); e == nil {
			deployedAt = &t
		}
	}
	var out *repo.Deployment
	err = s.store.InTx(r.Context(), func(q *repo.Queries) error {
		var e error
		out, e = q.UpsertDeployment(r.Context(), repo.DeploymentInput{
			ServiceID: sid, Environment: body.Environment, DeploymentVersion: body.DeploymentVersion,
			GitCommit: body.GitCommit, BuildID: body.BuildID, BuildURL: body.BuildURL,
			Status: body.DeploymentStatus, SetExpected: setExp, Source: "API", TriggeredBy: "api:" + clientID,
			Payload: body, DeployedAt: deployedAt,
		})
		return e
	})
	if err != nil {
		writeAPIError(w, r, http.StatusInternalServerError, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return
	}
	writeOK(w, r, http.StatusOK, out)
}

func (s *Server) postAdminDeployment(w http.ResponseWriter, r *http.Request) {
	op, _ := OperatorFrom(r.Context())
	serviceID, err := strconv.ParseInt(r.PathValue("service_id"), 10, 64)
	if err != nil || serviceID < 1 {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "bad service_id", nil)
		return
	}
	var body struct {
		Environment       string `json:"environment"`
		DeploymentVersion string `json:"deployment_version"`
		GitCommit         string `json:"git_commit"`
		BuildID           string `json:"build_id"`
		SetExpected       *bool  `json:"set_expected"`
		Remark            string `json:"remark"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", err.Error(), nil)
		return
	}
	if body.DeploymentVersion == "" || body.GitCommit == "" {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "deployment_version and git_commit required", nil)
		return
	}
	if body.Environment == "" {
		body.Environment = "dev"
	}
	if body.BuildID == "" {
		body.BuildID = "manual-" + time.Now().UTC().Format("20060102T150405")
	}
	setExp := true
	if body.SetExpected != nil {
		setExp = *body.SetExpected
	}
	if _, err := s.store.Q().GetService(r.Context(), serviceID); err != nil {
		if err == repo.ErrNotFound {
			writeAPIError(w, r, http.StatusNotFound, "NOT_FOUND", "service not found", nil)
			return
		}
		writeAPIError(w, r, http.StatusInternalServerError, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return
	}
	var out *repo.Deployment
	err = s.store.InTx(r.Context(), func(q *repo.Queries) error {
		var e error
		out, e = q.UpsertDeployment(r.Context(), repo.DeploymentInput{
			ServiceID: serviceID, Environment: body.Environment, DeploymentVersion: body.DeploymentVersion,
			GitCommit: body.GitCommit, BuildID: body.BuildID, Status: "SUCCEEDED", SetExpected: setExp,
			Source: "MANUAL", TriggeredBy: "admin:" + op.ID, Payload: body,
		})
		return e
	})
	if err != nil {
		writeAPIError(w, r, http.StatusInternalServerError, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return
	}
	_ = s.store.Q().InsertAudit(r.Context(), repo.Audit{
		OperatorSource: "glassadmin", OperatorID: op.ID, OperatorName: op.Name,
		Action: "deployment.register_manual", Permission: "job.deployment.manage",
		ResourceType: "deployment", ResourceID: strconv.FormatInt(out.ID, 10),
		RequestID: op.RequestID, ClientIP: op.ClientIP, Result: "OK", After: out,
	})
	writeOK(w, r, http.StatusOK, out)
}

func (s *Server) adminListAlerts(w http.ResponseWriter, r *http.Request) {
	if s.watcher == nil {
		writeOK(w, r, http.StatusOK, map[string]any{"list": []*repo.Alert{}, "evaluated_at": nil})
		return
	}
	items, at := s.watcher.Snapshot()
	if items == nil {
		items = []*repo.Alert{}
	}
	var atv any
	if !at.IsZero() {
		atv = at
	}
	writeOK(w, r, http.StatusOK, map[string]any{"list": items, "evaluated_at": atv})
}

