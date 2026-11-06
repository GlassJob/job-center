package execution

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/GlassJob/job-center/internal/infrastructure/security"
	repo "github.com/GlassJob/job-center/internal/repository/mysql"
)

const HeaderEvent = "X-JC-Event"

type triggerContextMeta struct {
	ClientID    string `json:"client_id,omitempty"`
	CallbackURL string `json:"callback_url,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// ValidateCallbackURL 校验 callback_url：
// 1. 仅允许 http:// 或 https:// 且含非空 host；
// 2. 若配置了 allowedHosts 白名单，则 host 必须命中白名单（支持 "*"、精确匹配、"*.domain"）；
// 3. 若未配置白名单，执行防 SSRF 校验：拒绝 localhost 及解析到回环/私网/链路本地/未指定 IP 的地址。
func ValidateCallbackURL(raw string, allowedHosts []string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid callback_url: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return errors.New("callback_url scheme must be http or https")
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if host == "" {
		return errors.New("callback_url host is required")
	}
	if len(allowedHosts) > 0 {
		if !matchAllowedHost(host, allowedHosts) {
			return fmt.Errorf("callback_url host %q is not in callback_allowed_hosts", host)
		}
		return nil
	}
	return checkPublicHost(host)
}

func matchAllowedHost(host string, allowed []string) bool {
	for _, pat := range allowed {
		p := strings.ToLower(strings.TrimSpace(pat))
		if p == "" {
			continue
		}
		if p == "*" || p == host {
			return true
		}
		if strings.HasPrefix(p, "*.") {
			suffix := p[1:] // ".example.com"
			if strings.HasSuffix(host, suffix) && len(host) > len(suffix) {
				return true
			}
		}
	}
	return false
}

func checkPublicHost(host string) error {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return fmt.Errorf("callback_url host %q blocked by SSRF policy", host)
	}
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("callback_url ip %q blocked by SSRF policy", ip.String())
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return fmt.Errorf("callback_url host %q DNS lookup failed", host)
	}
	for _, addr := range ips {
		if isBlockedIP(addr.IP) {
			return fmt.Errorf("callback_url host %q resolves to internal IP %s", host, addr.IP.String())
		}
	}
	return nil
}

func isBlockedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// WebhookPayload 终态回调请求体。
type WebhookPayload struct {
	Event        string          `json:"event"`
	ExecutionID  string          `json:"execution_id"`
	ServiceCode  string          `json:"service_code,omitempty"`
	TaskCode     string          `json:"task_code"`
	TriggerType  string          `json:"trigger_type"`
	TriggeredBy  string          `json:"triggered_by"`
	Status       string          `json:"status"`
	StatusReason *string         `json:"status_reason,omitempty"`
	AttemptCount int             `json:"attempt_count"`
	Result       json.RawMessage `json:"result,omitempty"`
	ErrorCode    *string         `json:"error_code,omitempty"`
	ErrorMessage *string         `json:"error_message,omitempty"`
	StartedAt    *time.Time      `json:"started_at,omitempty"`
	FinishedAt   *time.Time      `json:"finished_at,omitempty"`
}

func (s *Service) finalizeExecutionAndNotify(ctx context.Context, executionID, status, reason string, finishedAt time.Time, result json.RawMessage, errCode, errMsg string) error {
	ok, err := s.store.Q().FinalizeExecution(ctx, executionID, status, reason, finishedAt, result, errCode, errMsg)
	if err != nil {
		return err
	}
	if ok {
		s.dispatchTerminalWebhookAsync(executionID)
	}
	return nil
}

func (s *Service) dispatchTerminalWebhookAsync(executionID string) {
	if s.store == nil {
		return
	}
	go s.deliverTerminalWebhook(executionID)
}

func (s *Service) deliverTerminalWebhook(executionID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	e, err := s.store.Q().GetExecution(ctx, executionID)
	var svcCode string
	if err == nil && e != nil {
		svcCode, _ = s.store.Q().GetServiceCodeByID(ctx, e.ServiceID)
	}
	cancel()
	if err != nil || e == nil || len(e.TriggerContext) == 0 {
		return
	}
	var meta triggerContextMeta
	if err := json.Unmarshal(e.TriggerContext, &meta); err != nil || strings.TrimSpace(meta.CallbackURL) == "" {
		return
	}
	clientID := meta.ClientID
	if clientID == "" && strings.HasPrefix(e.TriggeredBy, "api:") {
		clientID = strings.TrimPrefix(e.TriggeredBy, "api:")
	}
	var secret string
	if s.clientSecrets != nil && clientID != "" {
		secret = s.clientSecrets(clientID)
	}
	u, err := url.Parse(meta.CallbackURL)
	if err != nil {
		return
	}
	payload := WebhookPayload{
		Event:        "execution.finished",
		ExecutionID:  e.ExecutionID,
		ServiceCode:  svcCode,
		TaskCode:     e.TaskCode,
		TriggerType:  e.TriggerType,
		TriggeredBy:  e.TriggeredBy,
		Status:       e.Status,
		StatusReason: e.StatusReason,
		AttemptCount: e.AttemptCount,
		Result:       e.Result,
		ErrorCode:    e.ErrorCode,
		ErrorMessage: e.ErrorMessage,
		StartedAt:    e.StartedAt,
		FinishedAt:   e.FinishedAt,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}

	hc := s.webhookHTTP
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Second}
	}
	backoffs := s.webhookBackoffs
	if len(backoffs) == 0 {
		backoffs = []time.Duration{0, 200 * time.Millisecond, 600 * time.Millisecond, 1500 * time.Millisecond}
	}

	reqPath := u.EscapedPath()
	if reqPath == "" {
		reqPath = "/"
	}
	for attemptIdx, wait := range backoffs {
		if wait > 0 {
			time.Sleep(wait)
		}
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		nonce := webhookNonce()
		req, err := http.NewRequest(http.MethodPost, meta.CallbackURL, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(HeaderEvent, "execution.finished")
		if clientID != "" {
			req.Header.Set(security.HeaderClient, clientID)
		}
		req.Header.Set(security.HeaderTimestamp, ts)
		req.Header.Set(security.HeaderNonce, nonce)
		if secret != "" {
			if canon, err := security.Canonical(http.MethodPost, reqPath, u.RawQuery, body, ts, nonce, ""); err == nil {
				req.Header.Set(security.HeaderSignature, security.Sign(secret, canon))
			}
		}
		resp, err := hc.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return
			}
			s.log.Warn("webhook non-2xx response", "execution_id", executionID, "attempt", attemptIdx+1, "status", resp.StatusCode)
		} else {
			s.log.Warn("webhook delivery error", "execution_id", executionID, "attempt", attemptIdx+1, "err", err)
		}
	}
}

func webhookNonce() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// SetClientSecretLookup 配置按 client_id 获取 HMAC 密钥（用于终态 Webhook 签名）。
func (s *Service) SetClientSecretLookup(fn func(clientID string) string) {
	s.clientSecrets = fn
}

// SetWebhookRetryBackoffs 测试或自定义终态 Webhook 退避序列（首个元素通常为 0）。
func (s *Service) SetWebhookRetryBackoffs(backoffs []time.Duration) {
	s.webhookBackoffs = backoffs
}

var _ = repo.ErrNotFound
