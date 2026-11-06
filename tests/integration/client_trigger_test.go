package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GlassJob/job-center/internal/application/catalog"
	appexec "github.com/GlassJob/job-center/internal/application/execution"
	"github.com/GlassJob/job-center/internal/application/registration"
	"github.com/GlassJob/job-center/internal/config"
	"github.com/GlassJob/job-center/internal/domain/schedule"
	"github.com/GlassJob/job-center/internal/infrastructure/database"
	"github.com/GlassJob/job-center/internal/infrastructure/logger"
	"github.com/GlassJob/job-center/internal/infrastructure/security"
	"github.com/GlassJob/job-center/internal/protocol"
	repo "github.com/GlassJob/job-center/internal/repository/mysql"
	httpapi "github.com/GlassJob/job-center/internal/transport/http"
	wstransport "github.com/GlassJob/job-center/internal/transport/websocket"
	"github.com/GlassJob/job-center/migrations"
)

func TestClientTriggerIsolationSSEAndWebhook(t *testing.T) {
	cfgPath := os.Getenv("JC_TEST_CONFIG")
	if cfgPath == "" {
		cfgPath = filepath.Join("..", "..", "configs", "local.yaml")
	}
	if _, err := os.Stat(cfgPath); err != nil {
		t.Skip("no local.yaml")
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.HTTP.ExecutorAddr = "127.0.0.1:28580"
	cfg.HTTP.AdminAddr = "127.0.0.1:28581"
	cfg.NodeID = "client-trigger-test"
	if cfg.Security.AdminClients == nil {
		cfg.Security.AdminClients = map[string]config.AdminClient{}
	}
	erpSecret := "erp-client-secret-16bytes"
	crmSecret := "crm-client-secret-16bytes"
	cfg.Security.AdminClients["erp"] = config.AdminClient{
		Secret:               erpSecret,
		AllowedTasks:         []string{"demo.svc/file.import", "demo.svc/long.job"},
		CallbackAllowedHosts: []string{"127.0.0.1"},
	}
	cfg.Security.AdminClients["crm"] = config.AdminClient{
		Secret:       crmSecret,
		AllowedTasks: []string{"other.svc/*"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := logger.New("error", "text", io.Discard)
	if _, err := database.MigrateUp(ctx, cfg.Database.DSN, migrations.FS, log); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(ctx, cfg.Database.DSN, database.PoolOptions{MaxOpenConns: 8, MaxIdleConns: 4, ConnMaxLifetime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cleanup(t, db)
	defer cleanup(t, db)

	store := repo.New(db)
	val, err := protocol.DefaultValidator()
	if err != nil {
		t.Fatal(err)
	}
	reg := registration.New(store, cfg.Security.CredentialPepper, cfg.NodeID, time.Minute, log)
	loc, tz, _, _ := schedule.ResolveDefaultTimezone("", os.Getenv, "")
	cat := catalog.New(store, cfg.Security.CredentialPepper, loc, tz)

	ws := wstransport.NewServer(reg, cfg, val, log)
	api := httpapi.New(db, store, cat, cfg, loc, tz)
	execSvc := appexec.New(store, ws.Hub(), time.Minute, log)
	execSvc.SetWebhookRetryBackoffs([]time.Duration{0, 50 * time.Millisecond, 100 * time.Millisecond})
	ws.SetExecution(execSvc)
	api.SetExecution(execSvc)

	mux := http.NewServeMux()
	api.MountExecutor(mux)
	ws.Mount(mux)
	adminMux := http.NewServeMux()
	api.MountAdmin(adminMux)
	execSrv := &http.Server{Addr: cfg.HTTP.ExecutorAddr, Handler: mux}
	adminSrv := &http.Server{Addr: cfg.HTTP.AdminAddr, Handler: adminMux}
	go execSrv.ListenAndServe()
	go adminSrv.ListenAndServe()
	defer execSrv.Shutdown(context.Background())
	defer adminSrv.Shutdown(context.Background())
	time.Sleep(80 * time.Millisecond)

	// 创建服务与凭据，并连接模拟 Executor
	actor := catalog.Actor{Client: "test", Operator: security.Operator{ID: "1", Name: "tester"}}
	svc, err := cat.CreateService(ctx, actor, catalog.CreateServiceInput{ServiceCode: "demo.svc", ServiceName: "Demo Service"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := cat.CreateCredential(ctx, actor, svc.ID, catalog.CreateCredentialInput{AllowedTaskCodes: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	wsURL := "ws://" + cfg.HTTP.ExecutorAddr + "/v1/executor/ws"
	conn := dial(t, ctx, wsURL)
	defer conn.CloseNow()
	mustAuth(t, ctx, conn, created.Token, "demo.svc", "inst-import-1")
	mustRegister(t, ctx, conn, "demo.svc", "inst-import-1", "1.0.0", "abc1234", []protocol.TaskDecl{
		{
			TaskCode: "file.import", TaskName: "File Import", HandlerVersion: "1.0.0",
			TimeoutSeconds: 60, MaxConcurrency: 2,
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		{
			TaskCode: "long.job", TaskName: "Long Job", HandlerVersion: "1.0.0",
			TimeoutSeconds: 60, MaxConcurrency: 2,
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
	})

	// 启动模拟 Webhook 接收端（首次返回 500 验证退避重试，第二次验签并返回 200）
	var webhookHits atomic.Int32
	var webhookMu sync.Mutex
	var gotWebhook appexec.WebhookPayload
	var gotWebhookSigValid bool
	var gotWebhookEventHdr string

	whSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := webhookHits.Add(1)
		rawBody, _ := io.ReadAll(r.Body)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		ts := r.Header.Get(security.HeaderTimestamp)
		nonce := r.Header.Get(security.HeaderNonce)
		sig := r.Header.Get(security.HeaderSignature)
		canon, err := security.Canonical(r.Method, r.URL.Path, r.URL.RawQuery, rawBody, ts, nonce, "")
		valid := err == nil && security.VerifySignature(erpSecret, canon, sig)

		webhookMu.Lock()
		_ = json.Unmarshal(rawBody, &gotWebhook)
		gotWebhookSigValid = valid
		gotWebhookEventHdr = r.Header.Get(appexec.HeaderEvent)
		webhookMu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer whSrv.Close()

	base := "http://" + cfg.HTTP.AdminAddr

	// 1. 越权客户端触发 -> 403 PERMISSION_DENIED
	code, resp := clientSignedWithHeaders(t, http.MethodPost, base+"/v1/trigger/demo.svc/file.import",
		map[string]string{"Idempotency-Key": "idem-crm-deny"},
		`{"params":{"oss_key":"imports/a.xlsx"}}`, "crm", crmSecret)
	if code != http.StatusForbidden || !strings.Contains(resp, "PERMISSION_DENIED") {
		t.Fatalf("expected 403 for unauthorized trigger, got %d %s", code, resp)
	}

	// 2. 非白名单 callback_url 拒绝 -> 400 PARAMS_INVALID
	code, resp = clientSignedWithHeaders(t, http.MethodPost, base+"/v1/trigger/demo.svc/file.import",
		map[string]string{"Idempotency-Key": "idem-bad-cb"},
		`{"params":{"oss_key":"imports/a.xlsx"},"callback_url":"http://evil.internal.local/hook"}`, "erp", erpSecret)
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 for disallowed callback_url, got %d %s", code, resp)
	}

	// 3. 合法客户端触发（带 with_sse_ticket=true + callback_url）-> 202 + 首张 sse_ticket
	trigBody := fmt.Sprintf(`{"params":{"oss_key":"imports/20261106.xlsx"},"with_sse_ticket":true,"callback_url":%q}`, whSrv.URL+"/hook?src=jc")
	code, resp = clientSignedWithHeaders(t, http.MethodPost, base+"/v1/trigger/demo.svc/file.import",
		map[string]string{"Idempotency-Key": "idem-erp-1"}, trigBody, "erp", erpSecret)
	if code != http.StatusAccepted {
		t.Fatalf("expected 202 trigger, got %d %s", code, resp)
	}
	var trigEnv struct {
		Data struct {
			ExecutionID string `json:"execution_id"`
			Status      string `json:"status"`
			AttemptID   string `json:"attempt_id"`
			SSETicket   *struct {
				Ticket string `json:"ticket"`
				URL    string `json:"url"`
			} `json:"sse_ticket"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(resp), &trigEnv); err != nil {
		t.Fatal(err)
	}
	execID := trigEnv.Data.ExecutionID
	if execID == "" || trigEnv.Data.SSETicket == nil || trigEnv.Data.SSETicket.Ticket == "" {
		t.Fatalf("missing execution_id or sse_ticket: %s", resp)
	}
	if !strings.HasPrefix(trigEnv.Data.SSETicket.URL, "/v1/trigger/executions/"+execID+"/events?ticket=") {
		t.Fatalf("unexpected sse_ticket url: %s", trigEnv.Data.SSETicket.URL)
	}

	// 4. 越权客户端访问详情 / 日志 / 换票 / 取消 / 直连 SSE -> 全部 403 PERMISSION_DENIED
	for _, ep := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/v1/trigger/executions/" + execID, ""},
		{http.MethodGet, "/v1/trigger/executions/" + execID + "/logs", ""},
		{http.MethodPost, "/v1/trigger/executions/" + execID + "/sse-ticket", `{}`},
		{http.MethodPost, "/v1/trigger/executions/" + execID + "/cancel", `{"reason":"hack"}`},
		{http.MethodGet, "/v1/trigger/executions/" + execID + "/events", ""},
	} {
		c, b := clientSignedWithHeaders(t, ep.method, base+ep.path, nil, ep.body, "crm", crmSecret)
		if c != http.StatusForbidden {
			t.Fatalf("crm %s %s expected 403, got %d %s", ep.method, ep.path, c, b)
		}
	}

	// 5. 用首张 sse_ticket 连接外部别名路由 GET /v1/trigger/executions/{id}/events?ticket=...，同时模拟 Executor 上报进度/日志/完成
	sseDone := make(chan string, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodGet, base+trigEnv.Data.SSETicket.URL, nil)
		cli := &http.Client{Timeout: 8 * time.Second}
		r, err := cli.Do(req)
		if err != nil {
			sseDone <- "ERR:" + err.Error()
			return
		}
		defer r.Body.Close()
		raw, _ := io.ReadAll(r.Body)
		sseDone <- fmt.Sprintf("CODE=%d\n%s", r.StatusCode, string(raw))
	}()

	// Executor 侧接收 task.execute -> execute_ack -> started -> progress -> log -> completed(result)
	dispEnv := readEnv(t, ctx, conn)
	if dispEnv.Type != protocol.TypeTaskExecute {
		t.Fatalf("expected task.execute, got %s", dispEnv.Type)
	}
	var te protocol.TaskExecute
	_ = json.Unmarshal(dispEnv.Data, &te)

	send(t, ctx, conn, protocol.TypeTaskExecuteAck, protocol.TaskExecuteAck{
		AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, Accepted: true,
	})
	_ = readEnv(t, ctx, conn) // result_ack

	send(t, ctx, conn, protocol.TypeTaskStarted, protocol.TaskStarted{
		AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, StartedAt: protocol.FormatTime(time.Now().UTC()),
	})
	_ = readEnv(t, ctx, conn)

	pct := 55.0
	send(t, ctx, conn, protocol.TypeTaskProgress, protocol.TaskProgress{
		AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, Percent: &pct, Message: "parsed 550/1000 rows",
	})
	_ = readEnv(t, ctx, conn)

	send(t, ctx, conn, protocol.TypeTaskLog, protocol.TaskLog{
		AttemptID: te.AttemptID, LeaseToken: te.LeaseToken,
		Entries: []protocol.TaskLogEntry{{Seq: 1, Ts: protocol.FormatTime(time.Now().UTC()), Level: "INFO", Stream: "stdout", Message: "row 550 imported"}},
	})
	_ = readEnv(t, ctx, conn)

	// 稍等让 SSE 轮询抓取运行中进度与日志，再完成任务
	time.Sleep(450 * time.Millisecond)

	send(t, ctx, conn, protocol.TypeTaskCompleted, protocol.TaskCompleted{
		AttemptID: te.AttemptID, LeaseToken: te.LeaseToken,
		FinishedAt: protocol.FormatTime(time.Now().UTC()),
		Result:     json.RawMessage(`{"imported_rows":1000,"failed_rows":2,"error_report_key":"reports/err.xlsx"}`),
	})
	_ = readEnv(t, ctx, conn)

	sseOut := <-sseDone
	if !strings.HasPrefix(sseOut, "CODE=200\n") || !strings.Contains(sseOut, "event: log") || !strings.Contains(sseOut, "event: end") || !strings.Contains(sseOut, `"imported_rows":1000`) {
		t.Fatalf("unexpected SSE stream output:\n%s", sseOut)
	}

	// 单次票据二次使用 -> 401
	reqReplay, _ := http.NewRequest(http.MethodGet, base+trigEnv.Data.SSETicket.URL, nil)
	respReplay, err := (&http.Client{Timeout: 3 * time.Second}).Do(reqReplay)
	if err != nil {
		t.Fatal(err)
	}
	_ = respReplay.Body.Close()
	if respReplay.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replayed ticket expected 401, got %d", respReplay.StatusCode)
	}

	// 6. 合法客户端调 POST /v1/trigger/executions/{id}/sse-ticket 换新票，并可复用到 /v1/admin/executions/{id}/events?ticket=...
	code, resp = clientSignedWithHeaders(t, http.MethodPost, base+"/v1/trigger/executions/"+execID+"/sse-ticket", nil, `{}`, "erp", erpSecret)
	if code != http.StatusCreated {
		t.Fatalf("sse-ticket expected 201, got %d %s", code, resp)
	}
	var tkEnv struct {
		Data struct {
			Ticket string `json:"ticket"`
		} `json:"data"`
	}
	_ = json.Unmarshal([]byte(resp), &tkEnv)
	adminSSEReq, _ := http.NewRequest(http.MethodGet, base+"/v1/admin/executions/"+execID+"/events?ticket="+tkEnv.Data.Ticket, nil)
	adminSSEResp, err := (&http.Client{Timeout: 3 * time.Second}).Do(adminSSEReq)
	if err != nil {
		t.Fatal(err)
	}
	adminSSEBody, _ := io.ReadAll(adminSSEResp.Body)
	_ = adminSSEResp.Body.Close()
	if adminSSEResp.StatusCode != http.StatusOK || !strings.Contains(string(adminSSEBody), "event: end") {
		t.Fatalf("admin SSE reuse failed: %d %s", adminSSEResp.StatusCode, string(adminSSEBody))
	}

	// 7. 合法客户端查询详情与增量日志
	code, resp = clientSignedWithHeaders(t, http.MethodGet, base+"/v1/trigger/executions/"+execID, nil, "", "erp", erpSecret)
	if code != http.StatusOK || !strings.Contains(resp, `"status":"SUCCEEDED"`) || !strings.Contains(resp, `"imported_rows":1000`) {
		t.Fatalf("get detail failed: %d %s", code, resp)
	}
	code, resp = clientSignedWithHeaders(t, http.MethodGet, base+"/v1/trigger/executions/"+execID+"/logs?after_seq=0&limit=50", nil, "", "erp", erpSecret)
	if code != http.StatusOK || !strings.Contains(resp, "row 550 imported") {
		t.Fatalf("get logs failed: %d %s", code, resp)
	}

	// 8. 验证终态 Webhook（经历一次 500 后退避重试成功投递，且 HMAC 签名校验通过）
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if webhookHits.Load() >= 2 {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	webhookMu.Lock()
	whHits := webhookHits.Load()
	whValid := gotWebhookSigValid
	whEvt := gotWebhookEventHdr
	whPayload := gotWebhook
	webhookMu.Unlock()
	if whHits != 2 || !whValid || whEvt != "execution.finished" || whPayload.ExecutionID != execID || whPayload.Status != "SUCCEEDED" {
		t.Fatalf("webhook mismatch: hits=%d valid=%v hdr=%q payload=%+v", whHits, whValid, whEvt, whPayload)
	}

	// 9. 验证客户端取消接口 POST /v1/trigger/executions/{id}/cancel -> 202
	code, resp = clientSignedWithHeaders(t, http.MethodPost, base+"/v1/trigger/demo.svc/long.job",
		map[string]string{"Idempotency-Key": "idem-erp-cancel"}, `{"params":{}}`, "erp", erpSecret)
	if code != http.StatusAccepted {
		t.Fatalf("trigger long.job got %d %s", code, resp)
	}
	var trig2 struct {
		Data struct {
			ExecutionID string `json:"execution_id"`
		} `json:"data"`
	}
	_ = json.Unmarshal([]byte(resp), &trig2)
	disp2 := readEnv(t, ctx, conn)
	if disp2.Type != protocol.TypeTaskExecute {
		t.Fatalf("expected second task.execute, got %s", disp2.Type)
	}
	code, resp = clientSignedWithHeaders(t, http.MethodPost, base+"/v1/trigger/executions/"+trig2.Data.ExecutionID+"/cancel",
		nil, `{"reason":"user aborted","grace_seconds":10}`, "erp", erpSecret)
	if code != http.StatusAccepted || !strings.Contains(resp, `"cancel_state"`) {
		t.Fatalf("client cancel expected 202, got %d %s", code, resp)
	}
}

var clientNonceSeq atomic.Uint64

func clientSignedWithHeaders(t *testing.T, method, urlStr string, extraHeaders map[string]string, body, client, secret string) (int, string) {
	t.Helper()
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := fmt.Sprintf("%x-%d", time.Now().UnixNano(), clientNonceSeq.Add(1))
	u, err := http.NewRequest(method, urlStr, nil)
	if err != nil {
		t.Fatal(err)
	}
	canon, err := security.Canonical(method, u.URL.Path, u.URL.RawQuery, []byte(body), ts, nonce, "")
	if err != nil {
		t.Fatal(err)
	}
	sig := security.Sign(secret, canon)
	req, err := http.NewRequest(method, urlStr, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set(security.HeaderClient, client)
	req.Header.Set(security.HeaderTimestamp, ts)
	req.Header.Set(security.HeaderNonce, nonce)
	req.Header.Set(security.HeaderSignature, sig)
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
