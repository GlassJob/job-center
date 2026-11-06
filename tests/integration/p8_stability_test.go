package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	"github.com/coder/websocket"
)

func TestP8Stability(t *testing.T) {
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
	cfg.HTTP.ExecutorAddr = "127.0.0.1:28290"
	cfg.HTTP.AdminAddr = "127.0.0.1:28291"
	cfg.NodeID = "p8-stability"
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
	_, _ = db.Exec(`UPDATE job_node_lease SET lease_until='1970-01-01 00:00:00.000' WHERE lease_name IN ('lease-sweeper','scheduler','archiver')`)

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
	leaseTTL := 3 * time.Second
	execSvc := appexec.New(store, ws.Hub(), leaseTTL, log)
	ws.SetExecution(execSvc)
	api.SetExecution(execSvc)

	rt := appexec.NewRuntime(execSvc, appexec.RuntimeOptions{
		NodeID: cfg.NodeID, SweepInterval: 200 * time.Millisecond, LeaseTTL: leaseTTL,
		LostGrace: 500 * time.Millisecond, TimeoutGrace: 500 * time.Millisecond,
		DispatchInterval: 200 * time.Millisecond, StartedAt: time.Now().UTC().Add(-2 * time.Minute),
	})
	go rt.Run(ctx)

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
	svc, err := cat.CreateService(ctx, actor, catalog.CreateServiceInput{ServiceCode: "p8.svc", ServiceName: "P8"})
	if err != nil {
		t.Fatal(err)
	}
	cred, err := cat.CreateCredential(ctx, actor, svc.ID, catalog.CreateCredentialInput{AllowedTaskCodes: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}

	baseWS := "ws://" + cfg.HTTP.ExecutorAddr + "/v1/executor/ws"
	adminBase := "http://" + cfg.HTTP.AdminAddr
	secret := "dev-admin-secret-16b"
	cron := "0 */5 * * * *"

	t.Run("disconnect_reconnect", func(t *testing.T) {
		c1 := dial(t, ctx, baseWS)
		mustAuth(t, ctx, c1, cred.Token, "p8.svc", "inst-p8-re")
		mustRegister(t, ctx, c1, "p8.svc", "inst-p8-re", "1.0.0", "aaaa1111", nil)
		_ = c1.Close(websocket.StatusGoingAway, "drop")
		c2 := dial(t, ctx, baseWS)
		defer c2.Close(websocket.StatusNormalClosure, "")
		resp := mustAuth(t, ctx, c2, cred.Token, "p8.svc", "inst-p8-re")
		if resp.SessionEpoch < 1 {
			t.Fatalf("epoch=%d", resp.SessionEpoch)
		}
		ack := mustRegister(t, ctx, c2, "p8.svc", "inst-p8-re", "1.0.1", "bbbb2222", nil)
		if !ack.OK {
			t.Fatalf("%+v", ack)
		}
		var st string
		_ = db.QueryRowContext(ctx, `SELECT status FROM job_instance WHERE instance_id='inst-p8-re'`).Scan(&st)
		if st != "ONLINE" {
			t.Fatalf("status=%s", st)
		}
	})

	c := dial(t, ctx, baseWS)
	defer c.Close(websocket.StatusNormalClosure, "")
	mustAuth(t, ctx, c, cred.Token, "p8.svc", "inst-p8-1")
	ack := mustRegister(t, ctx, c, "p8.svc", "inst-p8-1", "1.0.0", "cccc3333", []protocol.TaskDecl{{
		TaskCode: "p8.work", TaskName: "Work", HandlerVersion: "1.0.0", DefaultCron: &cron, TimeoutSeconds: 30, MaxConcurrency: 1,
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}})
	if !ack.OK || len(ack.AcceptedTasks) != 1 {
		t.Fatalf("register %+v", ack)
	}
	taskID := ack.AcceptedTasks[0].TaskID
	_, _ = db.ExecContext(ctx, `UPDATE job_instance SET status='ONLINE', running_count=0, max_concurrency=10 WHERE instance_id='inst-p8-1'`)

	t.Run("concurrent_claim", func(t *testing.T) {
		cleanupP8Execs(t, db)
		resetP8Instances(t, db, "inst-p8-1")
		_, err := db.ExecContext(ctx, `UPDATE job_schedule SET concurrency_policy='ALLOW', max_concurrency=4, manual_enabled=1, timeout_seconds=60 WHERE task_id=?`, taskID)
		if err != nil {
			t.Fatal(err)
		}
		execID := "01P8CONCLAIM00000000000000"
		_, err = db.ExecContext(ctx, `INSERT INTO job_execution(execution_id, task_id, service_id, task_code, trigger_type, triggered_by, params, status, handler_version, handler_major, schema_hash, timeout_seconds, retry_policy, max_attempts, attempt_count)
			VALUES (?,?,?,?,'MANUAL','admin:t','{}','PENDING','1.0.0',1,'deadbeef',60,'{"max_attempts":1}',1,0)`,
			execID, taskID, svc.ID, "p8.work")
		if err != nil {
			t.Fatal(err)
		}
		errCh := make(chan error, 2)
		for i := 0; i < 2; i++ {
			go func() {
				errCh <- rt.DispatchOnce(ctx)
			}()
		}
		for i := 0; i < 2; i++ {
			if err := <-errCh; err != nil {
				t.Logf("dispatch err (may be race loser): %v", err)
			}
		}
		var n int
		_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_execution_attempt WHERE execution_id=?`, execID).Scan(&n)
		if n != 1 {
			t.Fatalf("concurrent claim want 1 attempt, got %d", n)
		}
		teEnv := waitType(t, ctx, c, protocol.TypeTaskExecute)
		var te protocol.TaskExecute
		_ = json.Unmarshal(teEnv.Data, &te)
		send(t, ctx, c, protocol.TypeTaskExecuteAck, protocol.TaskExecuteAck{AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, Accepted: true})
		assertResultOK(t, waitType(t, ctx, c, protocol.TypeTaskResultAck))
		send(t, ctx, c, protocol.TypeTaskStarted, protocol.TaskStarted{AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, StartedAt: protocol.FormatTime(time.Now().UTC())})
		assertResultOK(t, waitType(t, ctx, c, protocol.TypeTaskResultAck))
		send(t, ctx, c, protocol.TypeTaskCompleted, protocol.TaskCompleted{
			AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, FinishedAt: protocol.FormatTime(time.Now().UTC()),
			Result: json.RawMessage(`{"claimed":true}`),
		})
		assertResultOK(t, waitType(t, ctx, c, protocol.TypeTaskResultAck))
	})

	t.Run("timeout", func(t *testing.T) {
		cleanupP8Execs(t, db)
		resetP8Instances(t, db, "inst-p8-1")
		cTO := dial(t, ctx, baseWS)
		defer cTO.Close(websocket.StatusNormalClosure, "")
		mustAuth(t, ctx, cTO, cred.Token, "p8.svc", "inst-p8-1")
		mustRegister(t, ctx, cTO, "p8.svc", "inst-p8-1", "1.0.0", "cccc3333", []protocol.TaskDecl{{
			TaskCode: "p8.work", TaskName: "Work", HandlerVersion: "1.0.0", DefaultCron: &cron, TimeoutSeconds: 30, MaxConcurrency: 2,
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}})
		resetP8Instances(t, db, "inst-p8-1")
		_, err := db.ExecContext(ctx, `UPDATE job_schedule SET timeout_seconds=1, concurrency_policy='ALLOW', max_concurrency=4, manual_enabled=1,
			retry_policy=? WHERE task_id=?`, `{"max_attempts":1,"strategy":"FIXED","initial_delay_seconds":1,"retry_on_timeout":false}`, taskID)
		if err != nil {
			t.Fatal(err)
		}
		code, body := signedJSON(t, http.MethodPost, adminBase+fmt.Sprintf("/v1/admin/tasks/%d/executions", taskID),
			map[string]string{"Idempotency-Key": "p8-timeout"}, `{"params":{}}`, secret, []string{"job.task.execute"}, false)
		if code != 202 {
			t.Fatalf("%d %s", code, body)
		}
		var env struct {
			Data struct {
				ExecutionID string `json:"execution_id"`
			} `json:"data"`
		}
		_ = json.Unmarshal([]byte(body), &env)
		teEnv := waitType(t, ctx, cTO, protocol.TypeTaskExecute)
		var te protocol.TaskExecute
		_ = json.Unmarshal(teEnv.Data, &te)
		send(t, ctx, cTO, protocol.TypeTaskExecuteAck, protocol.TaskExecuteAck{AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, Accepted: true})
		assertResultOK(t, waitType(t, ctx, cTO, protocol.TypeTaskResultAck))
		send(t, ctx, cTO, protocol.TypeTaskStarted, protocol.TaskStarted{AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, StartedAt: protocol.FormatTime(time.Now().UTC())})
		assertResultOK(t, waitType(t, ctx, cTO, protocol.TypeTaskResultAck))
		_, err = db.ExecContext(ctx, `UPDATE job_execution_attempt SET deadline_at=DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 5 SECOND), cancel_state='SENT', status='RUNNING' WHERE attempt_id=?`, te.AttemptID)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = db.ExecContext(ctx, `UPDATE job_node_lease SET lease_until='1970-01-01 00:00:00.000' WHERE lease_name='lease-sweeper'`)
		var st string
		deadline := time.Now().Add(6 * time.Second)
		for time.Now().Before(deadline) {
			drainUntilIdle(t, ctx, cTO, 50*time.Millisecond)
			_ = db.QueryRowContext(ctx, `SELECT status FROM job_execution WHERE execution_id=?`, env.Data.ExecutionID).Scan(&st)
			if st == "TIMED_OUT" {
				return
			}
			time.Sleep(150 * time.Millisecond)
		}
		t.Fatalf("want TIMED_OUT got %s", st)
	})

	t.Run("retry_on_fail", func(t *testing.T) {
		cleanupP8Execs(t, db)
		resetP8Instances(t, db, "inst-p8-1")
		cRetry := dial(t, ctx, baseWS)
		defer cRetry.Close(websocket.StatusNormalClosure, "")
		mustAuth(t, ctx, cRetry, cred.Token, "p8.svc", "inst-p8-1")
		mustRegister(t, ctx, cRetry, "p8.svc", "inst-p8-1", "1.0.0", "cccc3333", []protocol.TaskDecl{{
			TaskCode: "p8.work", TaskName: "Work", HandlerVersion: "1.0.0", DefaultCron: &cron, TimeoutSeconds: 30, MaxConcurrency: 2,
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}})
		resetP8Instances(t, db, "inst-p8-1")
		_, err := db.ExecContext(ctx, `UPDATE job_schedule SET timeout_seconds=60, concurrency_policy='ALLOW', manual_enabled=1,
			retry_policy=? WHERE task_id=?`, `{"max_attempts":2,"strategy":"FIXED","initial_delay_seconds":1,"retry_on_timeout":false}`, taskID)
		if err != nil {
			t.Fatal(err)
		}
		code, body := signedJSON(t, http.MethodPost, adminBase+fmt.Sprintf("/v1/admin/tasks/%d/executions", taskID),
			map[string]string{"Idempotency-Key": "p8-retry"}, `{"params":{}}`, secret, []string{"job.task.execute"}, false)
		if code != 202 {
			t.Fatalf("%d %s", code, body)
		}
		var env struct {
			Data struct {
				ExecutionID string `json:"execution_id"`
			} `json:"data"`
		}
		_ = json.Unmarshal([]byte(body), &env)
		teEnv := waitType(t, ctx, cRetry, protocol.TypeTaskExecute)
		var te protocol.TaskExecute
		_ = json.Unmarshal(teEnv.Data, &te)
		send(t, ctx, cRetry, protocol.TypeTaskExecuteAck, protocol.TaskExecuteAck{AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, Accepted: true})
		assertResultOK(t, waitType(t, ctx, cRetry, protocol.TypeTaskResultAck))
		send(t, ctx, cRetry, protocol.TypeTaskStarted, protocol.TaskStarted{AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, StartedAt: protocol.FormatTime(time.Now().UTC())})
		assertResultOK(t, waitType(t, ctx, cRetry, protocol.TypeTaskResultAck))
		send(t, ctx, cRetry, protocol.TypeTaskFailed, protocol.TaskFailed{
			AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, FinishedAt: protocol.FormatTime(time.Now().UTC()),
			Reason: "HANDLER_ERROR", Error: protocol.TaskFailedError{Code: "BOOM", Message: "fail once"},
		})
		assertResultOK(t, waitType(t, ctx, cRetry, protocol.TypeTaskResultAck))

		te2 := waitType(t, ctx, cRetry, protocol.TypeTaskExecute)
		var teMsg protocol.TaskExecute
		_ = json.Unmarshal(te2.Data, &teMsg)
		if teMsg.AttemptID == te.AttemptID {
			t.Fatal("expected new attempt")
		}
		send(t, ctx, cRetry, protocol.TypeTaskExecuteAck, protocol.TaskExecuteAck{AttemptID: teMsg.AttemptID, LeaseToken: teMsg.LeaseToken, Accepted: true})
		assertResultOK(t, waitType(t, ctx, cRetry, protocol.TypeTaskResultAck))
		send(t, ctx, cRetry, protocol.TypeTaskStarted, protocol.TaskStarted{AttemptID: teMsg.AttemptID, LeaseToken: teMsg.LeaseToken, StartedAt: protocol.FormatTime(time.Now().UTC())})
		assertResultOK(t, waitType(t, ctx, cRetry, protocol.TypeTaskResultAck))
		send(t, ctx, cRetry, protocol.TypeTaskCompleted, protocol.TaskCompleted{
			AttemptID: teMsg.AttemptID, LeaseToken: teMsg.LeaseToken, FinishedAt: protocol.FormatTime(time.Now().UTC()),
			Result: json.RawMessage(`{"retried":true}`),
		})
		assertResultOK(t, waitType(t, ctx, cRetry, protocol.TypeTaskResultAck))
		var st string
		_ = db.QueryRowContext(ctx, `SELECT status FROM job_execution WHERE execution_id=?`, env.Data.ExecutionID).Scan(&st)
		if st != "SUCCEEDED" {
			t.Fatalf("status=%s", st)
		}
		var n int
		_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_execution_attempt WHERE execution_id=?`, env.Data.ExecutionID).Scan(&n)
		if n < 2 {
			t.Fatalf("attempts=%d", n)
		}
	})

	t.Run("process_crash_lease_recovery", func(t *testing.T) {
		cleanupP8Execs(t, db)
		resetP8Instances(t, db, "inst-p8-1", "inst-p8-crash")
		_, err := db.ExecContext(ctx, `UPDATE job_schedule SET timeout_seconds=60, manual_enabled=1,
			retry_policy=? WHERE task_id=?`, `{"max_attempts":3,"strategy":"FIXED","initial_delay_seconds":1,"retry_on_lost":true}`, taskID)
		if err != nil {
			t.Fatal(err)
		}
		c2 := dial(t, ctx, baseWS)
		defer c2.Close(websocket.StatusNormalClosure, "")
		mustAuth(t, ctx, c2, cred.Token, "p8.svc", "inst-p8-crash")
		mustRegister(t, ctx, c2, "p8.svc", "inst-p8-crash", "1.0.0", "dddd4444", []protocol.TaskDecl{{
			TaskCode: "p8.work", TaskName: "Work", HandlerVersion: "1.0.0", DefaultCron: &cron, TimeoutSeconds: 30, MaxConcurrency: 2,
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}})
		_, _ = db.ExecContext(ctx, `UPDATE job_instance SET status='OFFLINE', running_count=0 WHERE instance_id='inst-p8-1'`)
		_, _ = db.ExecContext(ctx, `UPDATE job_instance SET status='ONLINE', running_count=0, max_concurrency=10 WHERE instance_id='inst-p8-crash'`)

		code, body := signedJSON(t, http.MethodPost, adminBase+fmt.Sprintf("/v1/admin/tasks/%d/executions", taskID),
			map[string]string{"Idempotency-Key": "p8-crash"}, `{"params":{}}`, secret, []string{"job.task.execute"}, false)
		if code != 202 {
			t.Fatalf("%d %s", code, body)
		}
		var env struct {
			Data struct {
				ExecutionID string `json:"execution_id"`
			} `json:"data"`
		}
		_ = json.Unmarshal([]byte(body), &env)
		teEnv := waitType(t, ctx, c2, protocol.TypeTaskExecute)
		var te protocol.TaskExecute
		_ = json.Unmarshal(teEnv.Data, &te)
		send(t, ctx, c2, protocol.TypeTaskExecuteAck, protocol.TaskExecuteAck{AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, Accepted: true})
		assertResultOK(t, waitType(t, ctx, c2, protocol.TypeTaskResultAck))
		send(t, ctx, c2, protocol.TypeTaskStarted, protocol.TaskStarted{AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, StartedAt: protocol.FormatTime(time.Now().UTC())})
		assertResultOK(t, waitType(t, ctx, c2, protocol.TypeTaskResultAck))

		// 模拟崩溃：连接仍在但租约过期（执行者不再续租）
		_, err = db.ExecContext(ctx, `UPDATE job_execution_attempt SET lease_expires_at=DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 SECOND) WHERE attempt_id=?`, te.AttemptID)
		if err != nil {
			t.Fatal(err)
		}

		var attempt2 string
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			var st string
			_ = db.QueryRowContext(ctx, `SELECT status FROM job_execution_attempt WHERE attempt_id=?`, te.AttemptID).Scan(&st)
			if st == "LOST" {
				te2 := waitType(t, ctx, c2, protocol.TypeTaskExecute)
				var teMsg protocol.TaskExecute
				_ = json.Unmarshal(te2.Data, &teMsg)
				if teMsg.AttemptID != te.AttemptID {
					attempt2 = teMsg.AttemptID
					send(t, ctx, c2, protocol.TypeTaskExecuteAck, protocol.TaskExecuteAck{AttemptID: teMsg.AttemptID, LeaseToken: teMsg.LeaseToken, Accepted: true})
					assertResultOK(t, waitType(t, ctx, c2, protocol.TypeTaskResultAck))
					send(t, ctx, c2, protocol.TypeTaskStarted, protocol.TaskStarted{AttemptID: teMsg.AttemptID, LeaseToken: teMsg.LeaseToken, StartedAt: protocol.FormatTime(time.Now().UTC())})
					assertResultOK(t, waitType(t, ctx, c2, protocol.TypeTaskResultAck))
					send(t, ctx, c2, protocol.TypeTaskCompleted, protocol.TaskCompleted{
						AttemptID: teMsg.AttemptID, LeaseToken: teMsg.LeaseToken, FinishedAt: protocol.FormatTime(time.Now().UTC()),
						Result: json.RawMessage(`{"recovered":true}`),
					})
					assertResultOK(t, waitType(t, ctx, c2, protocol.TypeTaskResultAck))
					break
				}
			}
			time.Sleep(200 * time.Millisecond)
		}
		if attempt2 == "" {
			t.Fatal("no retry after crash/LOST")
		}
		var st string
		_ = db.QueryRowContext(ctx, `SELECT status FROM job_execution WHERE execution_id=?`, env.Data.ExecutionID).Scan(&st)
		if st != "SUCCEEDED" {
			t.Fatalf("want SUCCEEDED got %s", st)
		}
		resetP8Instances(t, db, "inst-p8-1", "inst-p8-crash")
	})

	t.Run("db_failure_ready", func(t *testing.T) {
		db2, err := database.Open(ctx, cfg.Database.DSN, database.PoolOptions{MaxOpenConns: 2, MaxIdleConns: 1, ConnMaxLifetime: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		api2 := httpapi.New(db2, store, cat, cfg, loc, tz)
		mux2 := http.NewServeMux()
		api2.MountAdmin(mux2)
		srv2 := &http.Server{Addr: "127.0.0.1:28292", Handler: mux2}
		go srv2.ListenAndServe()
		defer srv2.Close()
		time.Sleep(100 * time.Millisecond)
		resp, err := http.Get("http://127.0.0.1:28292/health/ready")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("ready before close: %d", resp.StatusCode)
		}
		_ = db2.Close()
		resp2, err := http.Get("http://127.0.0.1:28292/health/ready")
		if err != nil {
			t.Fatal(err)
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != 503 {
			b, _ := io.ReadAll(resp2.Body)
			t.Fatalf("want 503 after db close, got %d %s", resp2.StatusCode, b)
		}
	})

	t.Run("audits_and_metrics", func(t *testing.T) {
		code, body := signedJSON(t, http.MethodGet, adminBase+"/v1/admin/audits?limit=10", nil, "", secret, []string{"job.credential.manage"}, false)
		if code != 200 {
			t.Fatalf("audits %d %s", code, body)
		}
		var env struct {
			Code string `json:"code"`
			Data struct {
				List []map[string]any `json:"list"`
			} `json:"data"`
		}
		_ = json.Unmarshal([]byte(body), &env)
		if env.Code != "OK" {
			t.Fatalf("%s", body)
		}
		resp, err := http.Get(adminBase + "/metrics")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("metrics %d %s", resp.StatusCode, b)
		}
		s := string(b)
		if !containsAll(s, "job_center_archive_lag_days", "job_center_table_rows") {
			t.Fatalf("metrics missing series: %s", s)
		}
	})
}


func resetP8Instances(t *testing.T, db *sql.DB, ids ...string) {
	t.Helper()
	for _, id := range ids {
		_, _ = db.Exec(`UPDATE job_instance SET status='ONLINE', running_count=0, max_concurrency=10 WHERE instance_id=?`, id)
	}
}

func cleanupP8Execs(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, q := range []string{
		"DELETE FROM job_execution_log", "DELETE FROM job_execution_attempt", "DELETE FROM job_execution_event", "DELETE FROM job_execution",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}
