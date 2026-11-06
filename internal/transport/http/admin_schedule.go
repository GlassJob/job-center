package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/GlassJob/job-center/internal/domain/schedule"
	"github.com/GlassJob/job-center/internal/domain/task"
	repo "github.com/GlassJob/job-center/internal/repository/mysql"
)

type enabledBody struct {
	Enabled bool `json:"enabled"`
}

func (s *Server) putTaskEnabled(w http.ResponseWriter, r *http.Request) {
	op, _ := OperatorFrom(r.Context())
	taskID, err := strconv.ParseInt(r.PathValue("task_id"), 10, 64)
	if err != nil || taskID < 1 {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "bad task_id", nil)
		return
	}
	ver, hasMatch, err := parseIntIfMatch(r.Header.Get("If-Match"))
	if err != nil || !hasMatch {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "If-Match version required", nil)
		return
	}
	var body enabledBody
	if err := decodeJSONBody(r, &body); err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", err.Error(), nil)
		return
	}
	cur, err := s.store.Q().GetSchedule(r.Context(), taskID)
	if err != nil {
		if err == repo.ErrNotFound {
			writeAPIError(w, r, http.StatusNotFound, "NOT_FOUND", "schedule not found", nil)
			return
		}
		writeAPIError(w, r, http.StatusInternalServerError, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return
	}
	var next *time.Time
	if body.Enabled {
		nf, err := s.nextFireFor(cur)
		if err != nil {
			writeAPIError(w, r, http.StatusUnprocessableEntity, "CRON_INVALID", err.Error(), nil)
			return
		}
		next = nf
	}
	by := op.ID
	n, err := s.store.Q().SetEnabledCAS(r.Context(), taskID, ver, body.Enabled, next, by)
	if err != nil {
		writeAPIError(w, r, http.StatusInternalServerError, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return
	}
	if n == 0 {
		writeAPIError(w, r, http.StatusConflict, "VERSION_CONFLICT", "If-Match does not match", nil)
		return
	}
	out, err := s.store.Q().GetSchedule(r.Context(), taskID)
	if err != nil {
		writeAPIError(w, r, http.StatusInternalServerError, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return
	}
	s.fillEffectiveTZ(out)
	_ = s.store.Q().InsertAudit(r.Context(), repo.Audit{
		OperatorSource: "ADMIN", OperatorID: op.ID, OperatorName: op.Name, Action: "task.enabled",
		Permission: "job.task.update", ResourceType: "task", ResourceID: strconv.FormatInt(taskID, 10),
		RequestID: op.RequestID, ClientIP: op.ClientIP, Result: "SUCCESS",
		Before: map[string]any{"version": ver}, After: map[string]any{"enabled": body.Enabled, "version": out.Version},
	})
	writeOK(w, r, http.StatusOK, out)
}

func (s *Server) putTaskSchedule(w http.ResponseWriter, r *http.Request) {
	op, _ := OperatorFrom(r.Context())
	taskID, err := strconv.ParseInt(r.PathValue("task_id"), 10, 64)
	if err != nil || taskID < 1 {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "bad task_id", nil)
		return
	}
	ver, hasMatch, err := parseIntIfMatch(r.Header.Get("If-Match"))
	if err != nil || !hasMatch {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "If-Match version required", nil)
		return
	}
	rawBody, err := io.ReadAll(io.LimitReader(r.Body, s.maxBody()))
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", err.Error(), nil)
		return
	}
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(rawBody, &rawFields); err != nil || rawFields == nil {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "JSON object body required", nil)
		return
	}
	var patch map[string]any
	if err := json.Unmarshal(rawBody, &patch); err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", err.Error(), nil)
		return
	}
	// reject enabled in schedule body
	if _, ok := patch["enabled"]; ok {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "enabled must use PUT .../enabled", nil)
		return
	}
	cur, err := s.store.Q().GetSchedule(r.Context(), taskID)
	if err != nil {
		if err == repo.ErrNotFound {
			writeAPIError(w, r, http.StatusNotFound, "NOT_FOUND", "schedule not found", nil)
			return
		}
		writeAPIError(w, r, http.StatusInternalServerError, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return
	}
	applySchedulePatch(cur, patch)
	if rawDP, hasDP := rawFields["default_params"]; hasDP {
		trimmed := bytes.TrimSpace(rawDP)
		if bytes.Equal(trimmed, []byte("null")) {
			cur.DefaultParams = nil
		} else {
			clean, verr := task.ValidateJSONObject(trimmed, task.MaxParamsBytes)
			if verr != nil {
				writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "default_params: "+verr.Error(), nil)
				return
			}
			cur.DefaultParams = clean
		}
	}
	if err := schedule.ValidateTimezone(cur.Timezone); err != nil {
		writeAPIError(w, r, http.StatusUnprocessableEntity, "TIMEZONE_INVALID", err.Error(), nil)
		return
	}
	if cur.ScheduleType == "CRON" {
		if cur.CronExpression == nil || *cur.CronExpression == "" {
			writeAPIError(w, r, http.StatusUnprocessableEntity, "CRON_INVALID", "cron_expression required for CRON", nil)
			return
		}
		if _, err := task.ValidateCron(*cur.CronExpression); err != nil {
			writeAPIError(w, r, http.StatusUnprocessableEntity, "CRON_INVALID", err.Error(), nil)
			return
		}
	}
	// 若任务声明了 input_schema，同步校验 default_params（或 CRON 下缺省空对象 {}）
	if detail, derr := s.store.Q().GetTask(r.Context(), taskID); derr == nil && detail != nil && len(detail.InputSchema) > 0 {
		if len(cur.DefaultParams) > 0 {
			if errs, verr := task.ValidateParamsAgainstSchema(detail.InputSchema, cur.DefaultParams); verr != nil {
				writeAPIError(w, r, http.StatusUnprocessableEntity, "PARAMS_SCHEMA_INVALID", verr.Error(), map[string]any{"errors": errs})
				return
			}
		} else if cur.ScheduleType == "CRON" {
			if errs, verr := task.ValidateParamsAgainstSchema(detail.InputSchema, []byte(`{}`)); verr != nil {
				writeAPIError(w, r, http.StatusUnprocessableEntity, "PARAMS_SCHEMA_INVALID", verr.Error(), map[string]any{"errors": errs})
				return
			}
		}
	}
	// 若当前 enabled，按新 cron 重算 next_fire_at
	if cur.Enabled {
		nf, err := s.nextFireFor(cur)
		if err != nil {
			writeAPIError(w, r, http.StatusUnprocessableEntity, "CRON_INVALID", err.Error(), nil)
			return
		}
		cur.NextFireAt = nf
	}
	n, err := s.store.Q().UpdateScheduleCAS(r.Context(), ver, cur, op.ID)
	if err != nil {
		writeAPIError(w, r, http.StatusInternalServerError, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return
	}
	if n == 0 {
		writeAPIError(w, r, http.StatusConflict, "VERSION_CONFLICT", "If-Match does not match", nil)
		return
	}
	out, err := s.store.Q().GetSchedule(r.Context(), taskID)
	if err != nil {
		writeAPIError(w, r, http.StatusInternalServerError, "STORAGE_UNAVAILABLE", err.Error(), nil)
		return
	}
	s.fillEffectiveTZ(out)
	_ = s.store.Q().InsertAudit(r.Context(), repo.Audit{
		OperatorSource: "ADMIN", OperatorID: op.ID, OperatorName: op.Name, Action: "task.schedule",
		Permission: "job.schedule.update", ResourceType: "task", ResourceID: strconv.FormatInt(taskID, 10),
		RequestID: op.RequestID, ClientIP: op.ClientIP, Result: "SUCCESS",
		Before: map[string]any{"version": ver}, After: map[string]any{"version": out.Version, "patch": patch},
	})
	writeOK(w, r, http.StatusOK, out)
}

func applySchedulePatch(s *repo.Schedule, patch map[string]any) {
	if v, ok := patch["schedule_type"].(string); ok {
		s.ScheduleType = v
	}
	if raw, ok := patch["cron_expression"]; ok {
		if raw == nil {
			s.CronExpression = nil
		} else if str, ok := raw.(string); ok {
			s.CronExpression = &str
		}
	}
	if raw, ok := patch["timezone"]; ok {
		if raw == nil {
			s.Timezone = nil
		} else if str, ok := raw.(string); ok {
			// openapi: 空串等同 NULL（服务器本地时区），不落库空字符串
			if str == "" {
				s.Timezone = nil
			} else {
				s.Timezone = &str
			}
		}
	}
	if v, ok := patch["manual_enabled"].(bool); ok {
		s.ManualEnabled = v
	}
	if v, ok := asInt(patch["timeout_seconds"]); ok {
		s.TimeoutSeconds = v
	}
	if v, ok := patch["concurrency_policy"].(string); ok {
		s.ConcurrencyPolicy = v
	}
	if v, ok := patch["concurrency_scope"].(string); ok {
		s.ConcurrencyScope = v
	}
	if v, ok := asInt(patch["max_concurrency"]); ok {
		s.MaxConcurrency = v
	}
	if v, ok := asInt(patch["queue_max"]); ok {
		s.QueueMax = v
	}
	if v, ok := patch["misfire_policy"].(string); ok {
		s.MisfirePolicy = v
	}
	if v, ok := asInt(patch["misfire_threshold_seconds"]); ok {
		s.MisfireThresholdSeconds = v
	}
	if v, ok := asInt(patch["catch_up_max"]); ok {
		s.CatchUpMax = v
	}
	if v, ok := patch["dispatch_strategy"].(string); ok {
		s.DispatchStrategy = v
	}
	if raw, ok := patch["retry_policy"]; ok && raw != nil {
		// re-marshal
		b, _ := json.Marshal(raw)
		s.RetryPolicy = b
	}
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	default:
		return 0, false
	}
}

func (s *Server) nextFireFor(sch *repo.Schedule) (*time.Time, error) {
	if sch.ScheduleType != "CRON" || sch.CronExpression == nil || *sch.CronExpression == "" {
		return nil, nil
	}
	cs, err := task.ValidateCron(*sch.CronExpression)
	if err != nil {
		return nil, err
	}
	loc := s.defaultLoc
	if sch.Timezone != nil && *sch.Timezone != "" {
		l, err := time.LoadLocation(*sch.Timezone)
		if err != nil {
			return nil, err
		}
		loc = l
	}
	if loc == nil {
		loc = time.Local
	}
	n := cs.Next(time.Now().In(loc)).UTC()
	return &n, nil
}

func (s *Server) fillEffectiveTZ(sch *repo.Schedule) {
	if sch.Timezone != nil && *sch.Timezone != "" {
		sch.EffectiveTimezone = *sch.Timezone
		return
	}
	if s.defaultTZ != "" {
		sch.EffectiveTimezone = s.defaultTZ
		return
	}
	sch.EffectiveTimezone = "Local"
}


func (s *Server) postCronPreview(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CronExpression string  `json:"cron_expression"`
		Timezone       *string `json:"timezone"`
		Count          int     `json:"count"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", err.Error(), nil)
		return
	}
	if body.CronExpression == "" {
		writeAPIError(w, r, http.StatusBadRequest, "PARAMS_INVALID", "cron_expression required", nil)
		return
	}
	if body.Count < 1 {
		body.Count = 5
	}
	if body.Count > 20 {
		body.Count = 20
	}
	// 空串 = null = 服务器本地时区
	var tz *string
	if body.Timezone != nil && *body.Timezone != "" {
		tz = body.Timezone
	}
	if err := schedule.ValidateTimezone(tz); err != nil {
		writeOK(w, r, http.StatusOK, map[string]any{"valid": false, "error": err.Error(), "next_fire_times": []string{}})
		return
	}
	cs, err := task.ValidateCron(body.CronExpression)
	if err != nil {
		writeOK(w, r, http.StatusOK, map[string]any{"valid": false, "error": err.Error(), "next_fire_times": []string{}})
		return
	}
	loc := s.defaultLoc
	if loc == nil {
		loc = time.Local
	}
	if tz != nil {
		l, err := time.LoadLocation(*tz)
		if err != nil {
			writeOK(w, r, http.StatusOK, map[string]any{"valid": false, "error": err.Error(), "next_fire_times": []string{}})
			return
		}
		loc = l
	}
	cur := time.Now().In(loc)
	out := make([]string, 0, body.Count)
	for i := 0; i < body.Count; i++ {
		n := cs.Next(cur)
		if n.IsZero() {
			break
		}
		out = append(out, n.UTC().Format(time.RFC3339Nano))
		cur = n
	}
	writeOK(w, r, http.StatusOK, map[string]any{"valid": true, "next_fire_times": out})
}

func cleanIfMatchToken(h string) string {
	s := strings.TrimSpace(h)
	if strings.HasPrefix(s, "W/") || strings.HasPrefix(s, "w/") {
		s = strings.TrimSpace(s[2:])
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	return strings.TrimSpace(s)
}

func parseIntIfMatch(h string) (int64, bool, error) {
	tok := cleanIfMatchToken(h)
	if tok == "" {
		return 0, false, nil
	}
	v, err := strconv.ParseInt(tok, 10, 64)
	return v, true, err
}

func parseUintIfMatch(h string) (*uint64, error) {
	tok := cleanIfMatchToken(h)
	if tok == "" || tok == "*" {
		return nil, nil
	}
	v, err := strconv.ParseUint(tok, 10, 64)
	if err != nil {
		return nil, err
	}
	return &v, nil
}
