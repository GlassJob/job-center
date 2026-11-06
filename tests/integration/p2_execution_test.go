package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/GlassJob/job-center/internal/application/catalog"
	appexec "github.com/GlassJob/job-center/internal/application/execution"
	"github.com/GlassJob/job-center/internal/application/monitoring"
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
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestP2ManualExecutionLoop(t *testing.T) {
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
	cfg.HTTP.ExecutorAddr = "127.0.0.1:28180"
	cfg.HTTP.AdminAddr = "127.0.0.1:28181"
	cfg.NodeID = "p2-exec-test"
	if cfg.Security.AdminClients == nil {
		cfg.Security.AdminClients = map[string]config.AdminClient{}
	}
	cfg.Security.AdminClients["glassadmin"] = config.AdminClient{Secret: "dev-admin-secret-16b", MaxPerms: []string{"*"}}

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

	store := repo.New(db)
	val, err := protocol.DefaultValidator()
	if err != nil {
		t.Fatal(err)
	}
	reg := registration.New(store, cfg.Security.CredentialPepper, cfg.NodeID, time.Minute, log)
	loc, tz, _, _ := schedule.ResolveDefaultTimezone("", os.Getenv, "")
	cat := catalog.New(store, cfg.Security.CredentialPepper, loc, tz)
	_ = monitoring.New(store, monitoring.Options{SuspectAfter: time.Minute, OfflineAfter: 2 * time.Minute, SweepInterval: time.Minute, NodeID: cfg.NodeID}, log)

	ws := wstransport.NewServer(reg, cfg, val, log)
	api := httpapi.New(db, store, cat, cfg, loc, tz)
	execSvc := appexec.New(store, ws.Hub(), time.Minute, log)
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
	defer execSrv.Close()
	defer adminSrv.Close()
	time.Sleep(150 * time.Millisecond)

	actor := catalog.Actor{Client: "test", Operator: security.Operator{ID: "t", Name: "tester"}}
	svc, err := cat.CreateService(ctx, actor, catalog.CreateServiceInput{ServiceCode: "demo.svc", ServiceName: "Demo"})
	if err != nil {
		t.Fatal(err)
	}
	cred, err := cat.CreateCredential(ctx, actor, svc.ID, catalog.CreateCredentialInput{AllowedTaskCodes: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}

	baseWS := "ws://" + cfg.HTTP.ExecutorAddr + "/v1/executor/ws"
	c := dial(t, ctx, baseWS)
	defer c.Close(websocket.StatusNormalClosure, "")
	mustAuth(t, ctx, c, cred.Token, "demo.svc", "inst-p2-1")
	cron := "0 */5 * * * *"
	ack := mustRegister(t, ctx, c, "demo.svc", "inst-p2-1", "1.0.0", "deadbeef", []protocol.TaskDecl{{
		TaskCode: "demo.work", TaskName: "Work", HandlerVersion: "1.0.0", DefaultCron: &cron, TimeoutSeconds: 60, MaxConcurrency: 2,
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}})
	if !ack.OK || len(ack.AcceptedTasks) != 1 {
		t.Fatalf("register %+v", ack)
	}
	taskID := ack.AcceptedTasks[0].TaskID
	adminBase := "http://" + cfg.HTTP.AdminAddr
	secret := "dev-admin-secret-16b"

	t.Run("happy_path", func(t *testing.T) {
		code, body := signedJSON(t, http.MethodPost, adminBase+fmt.Sprintf("/v1/admin/tasks/%d/executions", taskID),
			map[string]string{"Idempotency-Key": "p2-happy-1"},
			`{"params":{"x":1},"reason":"test"}`, secret, []string{"job.task.execute"}, false)
		if code != 202 {
			t.Fatalf("trigger code=%d body=%s", code, body)
		}
		var env struct {
			Code string `json:"code"`
			Data struct {
				ExecutionID string `json:"execution_id"`
				Status      string `json:"status"`
				AttemptID   string `json:"attempt_id"`
				InstanceID  string `json:"instance_id"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(body), &env); err != nil {
			t.Fatal(err)
		}
		if env.Code != "OK" || env.Data.ExecutionID == "" || env.Data.AttemptID == "" || env.Data.InstanceID != "inst-p2-1" {
			t.Fatalf("%s", body)
		}

		// receive task.execute
		execMsg := waitType(t, ctx, c, protocol.TypeTaskExecute)
		var te protocol.TaskExecute
		if err := json.Unmarshal(execMsg.Data, &te); err != nil {
			t.Fatal(err)
		}
		if te.AttemptID != env.Data.AttemptID || te.LeaseToken == "" {
			t.Fatalf("execute %+v", te)
		}

		send(t, ctx, c, protocol.TypeTaskExecuteAck, protocol.TaskExecuteAck{AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, Accepted: true})
		ra := waitType(t, ctx, c, protocol.TypeTaskResultAck)
		assertResultOK(t, ra)

		now := protocol.FormatTime(time.Now().UTC())
		send(t, ctx, c, protocol.TypeTaskStarted, protocol.TaskStarted{AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, StartedAt: now})
		assertResultOK(t, waitType(t, ctx, c, protocol.TypeTaskResultAck))

		pct := 50.0
		send(t, ctx, c, protocol.TypeTaskProgress, protocol.TaskProgress{AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, Percent: &pct, Message: "half"})
		assertResultOK(t, waitType(t, ctx, c, protocol.TypeTaskResultAck))

		send(t, ctx, c, protocol.TypeTaskLog, protocol.TaskLog{
			AttemptID: te.AttemptID, LeaseToken: te.LeaseToken,
			Entries: []protocol.TaskLogEntry{{Seq: 1, Level: "INFO", Message: "hello", Ts: now}},
		})
		logAck := waitType(t, ctx, c, protocol.TypeTaskResultAck)
		assertResultOK(t, logAck)

		send(t, ctx, c, protocol.TypeTaskCompleted, protocol.TaskCompleted{
			AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, FinishedAt: protocol.FormatTime(time.Now().UTC()),
			Result: json.RawMessage(`{"ok":true}`),
		})
		assertResultOK(t, waitType(t, ctx, c, protocol.TypeTaskResultAck))

		code, body = signedJSON(t, http.MethodGet, adminBase+"/v1/admin/executions/"+env.Data.ExecutionID, nil, "", secret, []string{"job.execution.view"}, false)
		if code != 200 {
			t.Fatalf("detail %d %s", code, body)
		}
		var detail struct {
			Data struct {
				Status   string           `json:"status"`
				Attempts []map[string]any `json:"attempts"`
			} `json:"data"`
		}
		_ = json.Unmarshal([]byte(body), &detail)
		if detail.Data.Status != "SUCCEEDED" {
			t.Fatalf("exec status=%v body=%s", detail.Data.Status, body)
		}
		if len(detail.Data.Attempts) != 1 || detail.Data.Attempts[0]["status"] != "SUCCEEDED" {
			t.Fatalf("attempts=%v", detail.Data.Attempts)
		}

		code, body = signedJSON(t, http.MethodGet, adminBase+"/v1/admin/executions/"+env.Data.ExecutionID+"/logs", nil, "", secret, []string{"job.execution.log.view"}, false)
		if code != 200 {
			t.Fatalf("logs %d %s", code, body)
		}
		var logEnv struct {
			Code string         `json:"code"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(body), &logEnv); err != nil || logEnv.Code != "OK" {
			t.Fatalf("logs envelope: %v %s", err, body)
		}
		if _, ok := logEnv.Data["list"]; !ok {
			t.Fatalf("logs missing list: %s", body)
		}
		if _, hasItems := logEnv.Data["items"]; hasItems {
			t.Fatalf("logs must not return items: %s", body)
		}
	})

	t.Run("fail_and_cancel_unconfirmed", func(t *testing.T) {
		code, body := signedJSON(t, http.MethodPost, adminBase+fmt.Sprintf("/v1/admin/tasks/%d/executions", taskID),
			map[string]string{"Idempotency-Key": "p2-fail-1"},
			`{"params":{}}`, secret, []string{"job.task.execute"}, false)
		if code != 202 {
			t.Fatalf("trigger %d %s", code, body)
		}
		var env struct {
			Data struct {
				ExecutionID string `json:"execution_id"`
				AttemptID   string `json:"attempt_id"`
			} `json:"data"`
		}
		_ = json.Unmarshal([]byte(body), &env)
		teEnv := waitType(t, ctx, c, protocol.TypeTaskExecute)
		var te protocol.TaskExecute
		_ = json.Unmarshal(teEnv.Data, &te)

		send(t, ctx, c, protocol.TypeTaskExecuteAck, protocol.TaskExecuteAck{AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, Accepted: true})
		assertResultOK(t, waitType(t, ctx, c, protocol.TypeTaskResultAck))
		now := protocol.FormatTime(time.Now().UTC())
		send(t, ctx, c, protocol.TypeTaskStarted, protocol.TaskStarted{AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, StartedAt: now})
		assertResultOK(t, waitType(t, ctx, c, protocol.TypeTaskResultAck))

		code, body = signedJSON(t, http.MethodPost, adminBase+"/v1/admin/executions/"+env.Data.ExecutionID+"/cancel",
			nil, `{"reason":"stop-it","grace_seconds":5}`, secret, []string{"job.task.cancel"}, false)
		if code != 202 {
			t.Fatalf("cancel %d %s", code, body)
		}
		cancelEnv := waitType(t, ctx, c, protocol.TypeTaskCancel)
		var tc protocol.TaskCancel
		_ = json.Unmarshal(cancelEnv.Data, &tc)
		if tc.AttemptID != te.AttemptID {
			t.Fatalf("cancel msg %+v", tc)
		}
		send(t, ctx, c, protocol.TypeTaskCancelAck, protocol.TaskCancelAck{AttemptID: te.AttemptID, State: "CANCELLING"})

		// simulate Go not honoring cancel → failed with CANCEL_NOT_HONORED
		send(t, ctx, c, protocol.TypeTaskFailed, protocol.TaskFailed{
			AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, FinishedAt: protocol.FormatTime(time.Now().UTC()),
			Reason: "TIMEOUT", Error: protocol.TaskFailedError{Code: "CANCEL_NOT_HONORED", Message: "handler ignored cancel"},
		})
		assertResultOK(t, waitType(t, ctx, c, protocol.TypeTaskResultAck))

		code, body = signedJSON(t, http.MethodGet, adminBase+"/v1/admin/executions/"+env.Data.ExecutionID, nil, "", secret, []string{"job.execution.view"}, false)
		if code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		var detail struct {
			Data struct {
				Status   string           `json:"status"`
				Attempts []map[string]any `json:"attempts"`
			} `json:"data"`
		}
		_ = json.Unmarshal([]byte(body), &detail)
		if detail.Data.Status != "TIMED_OUT" {
			t.Fatalf("want TIMED_OUT got %v %s", detail.Data.Status, body)
		}
		if detail.Data.Attempts[0]["cancel_state"] != "CANCEL_UNCONFIRMED" {
			t.Fatalf("cancel_state=%v", detail.Data.Attempts[0]["cancel_state"])
		}
	})

	t.Run("cancelled_ok", func(t *testing.T) {
		code, body := signedJSON(t, http.MethodPost, adminBase+fmt.Sprintf("/v1/admin/tasks/%d/executions", taskID),
			map[string]string{"Idempotency-Key": "p2-cancel-ok"},
			`{"params":{}}`, secret, []string{"job.task.execute"}, false)
		if code != 202 {
			t.Fatalf("%d %s", code, body)
		}
		var env struct {
			Data struct {
				ExecutionID string `json:"execution_id"`
			} `json:"data"`
		}
		_ = json.Unmarshal([]byte(body), &env)
		teEnv := waitType(t, ctx, c, protocol.TypeTaskExecute)
		var te protocol.TaskExecute
		_ = json.Unmarshal(teEnv.Data, &te)
		send(t, ctx, c, protocol.TypeTaskExecuteAck, protocol.TaskExecuteAck{AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, Accepted: true})
		assertResultOK(t, waitType(t, ctx, c, protocol.TypeTaskResultAck))
		send(t, ctx, c, protocol.TypeTaskStarted, protocol.TaskStarted{AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, StartedAt: protocol.FormatTime(time.Now().UTC())})
		assertResultOK(t, waitType(t, ctx, c, protocol.TypeTaskResultAck))

		code, _ = signedJSON(t, http.MethodPost, adminBase+"/v1/admin/executions/"+env.Data.ExecutionID+"/cancel",
			nil, `{"reason":"user"}`, secret, []string{"job.task.cancel"}, false)
		if code != 202 {
			t.Fatalf("cancel %d", code)
		}
		_ = waitType(t, ctx, c, protocol.TypeTaskCancel)
		send(t, ctx, c, protocol.TypeTaskCancelAck, protocol.TaskCancelAck{AttemptID: te.AttemptID, State: "CANCELLING"})
		send(t, ctx, c, protocol.TypeTaskCancelled, protocol.TaskCancelled{
			AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, FinishedAt: protocol.FormatTime(time.Now().UTC()), StoppedBy: "COOPERATIVE",
		})
		assertResultOK(t, waitType(t, ctx, c, protocol.TypeTaskResultAck))

		code, body = signedJSON(t, http.MethodGet, adminBase+"/v1/admin/executions/"+env.Data.ExecutionID, nil, "", secret, []string{"job.execution.view"}, false)
		var detail struct {
			Data struct {
				Status string `json:"status"`
			} `json:"data"`
		}
		_ = json.Unmarshal([]byte(body), &detail)
		if detail.Data.Status != "CANCELLED" {
			t.Fatalf("status=%v body=%s", detail.Data.Status, body)
		}
	})

	t.Run("idempotent_replay", func(t *testing.T) {
		code1, body1 := signedJSON(t, http.MethodPost, adminBase+fmt.Sprintf("/v1/admin/tasks/%d/executions", taskID),
			map[string]string{"Idempotency-Key": "p2-idem"},
			`{"params":{"a":1},"allow_wait":true}`, secret, []string{"job.task.execute"}, false)
		code2, body2 := signedJSON(t, http.MethodPost, adminBase+fmt.Sprintf("/v1/admin/tasks/%d/executions", taskID),
			map[string]string{"Idempotency-Key": "p2-idem"},
			`{"params":{"a":1}}`, secret, []string{"job.task.execute"}, false)
		if code1 != 202 || code2 != 200 {
			t.Fatalf("c1=%d c2=%d b1=%s b2=%s", code1, code2, body1, body2)
		}
		// drain the execute for this trigger if any
		drainUntilIdle(t, ctx, c, 300*time.Millisecond)
	})
}

func waitType(t *testing.T, ctx context.Context, c *websocket.Conn, typ string) protocol.Envelope {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rctx, cancel := context.WithTimeout(ctx, time.Until(deadline))
		var env protocol.Envelope
		err := wsjson.Read(rctx, c, &env)
		cancel()
		if err != nil {
			t.Fatalf("wait %s: %v", typ, err)
		}
		if env.Type == typ {
			return env
		}
		if env.Type == protocol.TypeSystemError {
			t.Fatalf("got system.error while waiting %s: %s", typ, string(env.Data))
		}
		t.Logf("skip type=%s while waiting %s", env.Type, typ)
	}
	t.Fatalf("timeout waiting %s", typ)
	return protocol.Envelope{}
}

func assertResultOK(t *testing.T, env protocol.Envelope) {
	t.Helper()
	var ack protocol.TaskResultAck
	if err := json.Unmarshal(env.Data, &ack); err != nil {
		t.Fatal(err)
	}
	if ack.Status != "OK" {
		t.Fatalf("result_ack=%+v", ack)
	}
}

func drainUntilIdle(t *testing.T, ctx context.Context, c *websocket.Conn, wait time.Duration) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		rctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		var env protocol.Envelope
		err := wsjson.Read(rctx, c, &env)
		cancel()
		if err != nil {
			return
		}
		if env.Type == protocol.TypeTaskExecute {
			var te protocol.TaskExecute
			_ = json.Unmarshal(env.Data, &te)
			send(t, ctx, c, protocol.TypeTaskExecuteAck, protocol.TaskExecuteAck{AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, Accepted: true})
			_ = waitType(t, ctx, c, protocol.TypeTaskResultAck)
			send(t, ctx, c, protocol.TypeTaskCompleted, protocol.TaskCompleted{
				AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, FinishedAt: protocol.FormatTime(time.Now().UTC()), Result: json.RawMessage(`{}`),
			})
			_ = waitType(t, ctx, c, protocol.TypeTaskResultAck)
		}
	}
}

// silence unused import if strconv used elsewhere
var _ = strconv.Itoa
