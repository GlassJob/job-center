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
	"testing"
	"time"

	"github.com/GlassJob/job-center/internal/application/catalog"
	appexec "github.com/GlassJob/job-center/internal/application/execution"
	"github.com/GlassJob/job-center/internal/application/registration"
	"github.com/GlassJob/job-center/internal/application/scheduler"
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

func TestP5SchedulerAndLease(t *testing.T) {
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
	cfg.HTTP.ExecutorAddr = "127.0.0.1:28190"
	cfg.HTTP.AdminAddr = "127.0.0.1:28191"
	cfg.NodeID = "p5-sched-test"
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
	cleanupP5(t, db)

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

	sch := scheduler.New(store, scheduler.Options{
		NodeID: cfg.NodeID, TickInterval: 200 * time.Millisecond, LeaseTTL: 5 * time.Second,
		DefaultLocation: loc, BatchSize: 50,
	}, log)
	rt := appexec.NewRuntime(execSvc, appexec.RuntimeOptions{
		NodeID: cfg.NodeID, SweepInterval: 200 * time.Millisecond, LeaseTTL: leaseTTL,
		LostGrace: 500 * time.Millisecond, TimeoutGrace: time.Second,
		DispatchInterval: 200 * time.Millisecond, StartedAt: time.Now().UTC().Add(-2 * time.Minute), // no startup pause
	})
	go sch.Run(ctx)
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
	svc, err := cat.CreateService(ctx, actor, catalog.CreateServiceInput{ServiceCode: "p5.svc", ServiceName: "P5"})
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
	mustAuth(t, ctx, c, cred.Token, "p5.svc", "inst-p5-1")
	cron := "0 */5 * * * *"
	ack := mustRegister(t, ctx, c, "p5.svc", "inst-p5-1", "1.0.0", "abc12345", []protocol.TaskDecl{{
		TaskCode: "p5.work", TaskName: "Work", HandlerVersion: "1.0.0", DefaultCron: &cron, TimeoutSeconds: 30, MaxConcurrency: 1,
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}})
	if !ack.OK || len(ack.AcceptedTasks) != 1 {
		t.Fatalf("register %+v", ack)
	}
	taskID := ack.AcceptedTasks[0].TaskID
	adminBase := "http://" + cfg.HTTP.AdminAddr
	secret := "dev-admin-secret-16b"

	t.Run("no_duplicate_schedule_tick", func(t *testing.T) {
		schedID, err := store.Q().GetScheduleID(ctx, taskID)
		if err != nil {
			t.Fatal(err)
		}
		fireAt := time.Now().UTC().Truncate(time.Second)
		// set due next_fire_at and enable; use every-second cron so Next advances cleanly
		_, err = db.ExecContext(ctx, `UPDATE job_schedule SET schedule_type='CRON', cron_expression='*/2 * * * * *', enabled=1,
			misfire_policy='FIRE_ONCE', misfire_threshold_seconds=60, next_fire_at=?, concurrency_policy='ALLOW', max_concurrency=10
			WHERE task_id=?`, fireAt, taskID)
		if err != nil {
			t.Fatal(err)
		}
		// double-tick manually
		if err := sch.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		if err := sch.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		n, err := store.Q().CountExecutionsByScheduleTick(ctx, schedID, fireAt)
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("want exactly 1 execution for tick, got %d", n)
		}
	})

	t.Run("restart_no_burst", func(t *testing.T) {
		cleanupP5Execs(t, db)
		past := time.Now().UTC().Add(-30 * time.Minute)
		_, err := db.ExecContext(ctx, `UPDATE job_schedule SET schedule_type='CRON', cron_expression='0 * * * * *', enabled=1,
			misfire_policy='SKIP', misfire_threshold_seconds=60, catch_up_max=10, next_fire_at=?, concurrency_policy='ALLOW', max_concurrency=50
			WHERE task_id=?`, past, taskID)
		if err != nil {
			t.Fatal(err)
		}
		schedID, _ := store.Q().GetScheduleID(ctx, taskID)
		if err := sch.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		total, err := store.Q().CountExecutionsForSchedule(ctx, schedID, "")
		if err != nil {
			t.Fatal(err)
		}
		skipped, _ := store.Q().CountExecutionsForSchedule(ctx, schedID, "SKIPPED")
		// SKIP: 1 audit SKIPPED, no pile of PENDING
		if total > 2 {
			t.Fatalf("burst? total=%d skipped=%d", total, skipped)
		}
		if skipped < 1 {
			t.Fatalf("expected MISFIRE_SKIP audit, skipped=%d total=%d", skipped, total)
		}
		var next sql.NullTime
		_ = db.QueryRowContext(ctx, `SELECT next_fire_at FROM job_schedule WHERE task_id=?`, taskID).Scan(&next)
		if !next.Valid || !next.Time.After(time.Now().UTC().Add(-time.Minute)) {
			t.Fatalf("next_fire_at not advanced: %v", next.Time)
		}
	})

	t.Run("stale_lease_rejected", func(t *testing.T) {
		cleanupP5Execs(t, db)
		code, body := signedJSON(t, http.MethodPost, adminBase+fmt.Sprintf("/v1/admin/tasks/%d/executions", taskID),
			map[string]string{"Idempotency-Key": "p5-stale"},
			`{"params":{}}`, secret, []string{"job.task.execute"}, false)
		if code != 202 {
			t.Fatalf("trigger %d %s", code, body)
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

		// wrong lease token
		send(t, ctx, c, protocol.TypeTaskCompleted, protocol.TaskCompleted{
			AttemptID: te.AttemptID, LeaseToken: "00000000000000000000000000000000",
			FinishedAt: protocol.FormatTime(time.Now().UTC()), Result: json.RawMessage(`{"ok":true}`),
		})
		ackEnv := waitType(t, ctx, c, protocol.TypeTaskResultAck)
		var ra protocol.TaskResultAck
		_ = json.Unmarshal(ackEnv.Data, &ra)
		if ra.Status != "REJECTED" {
			t.Fatalf("want REJECTED got %+v", ra)
		}

		// detail flat shape + still not SUCCEEDED
		code, body = signedJSON(t, http.MethodGet, adminBase+"/v1/admin/executions/"+env.Data.ExecutionID, nil, "", secret, []string{"job.execution.view"}, false)
		if code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		var detail map[string]any
		var wrap struct {
			Data map[string]any `json:"data"`
		}
		_ = json.Unmarshal([]byte(body), &wrap)
		detail = wrap.Data
		if _, ok := detail["execution"]; ok {
			t.Fatalf("nested execution key must not exist: %s", body)
		}
		if detail["execution_id"] != env.Data.ExecutionID {
			t.Fatalf("flat execution_id missing: %s", body)
		}
		if detail["status"] == "SUCCEEDED" {
			t.Fatalf("stale result must not succeed: %s", body)
		}
		if _, ok := detail["attempts"]; !ok {
			t.Fatalf("attempts missing: %s", body)
		}

		// cancel returns 202（GlassAdmin 代理可能归一成 200；Center 本身必须是 202）
		code, _ = signedJSON(t, http.MethodPost, adminBase+"/v1/admin/executions/"+env.Data.ExecutionID+"/cancel",
			nil, `{"reason":"cleanup"}`, secret, []string{"job.task.cancel"}, false)
		if code != 202 {
			t.Fatalf("cancel want 202 got %d", code)
		}
		// 丢掉 cancel 帧，避免阻塞后续用例；不依赖 executor 确认
		drainUntilIdle(t, ctx, c, 150*time.Millisecond)
		_, _ = db.ExecContext(ctx, `UPDATE job_execution SET status='CANCELLED', finished_at=UTC_TIMESTAMP(3), concurrency_slot=NULL WHERE execution_id=?`, env.Data.ExecutionID)
		_, _ = db.ExecContext(ctx, `UPDATE job_execution_attempt SET status='CANCELLED', cancel_state='STOPPED', finished_at=UTC_TIMESTAMP(3) WHERE execution_id=?`, env.Data.ExecutionID)
	})

	t.Run("lease_expiry_recovers", func(t *testing.T) {
		cleanupP5Execs(t, db)
		_, _ = db.ExecContext(ctx, `UPDATE job_instance SET status='ONLINE', running_count=0, max_concurrency=10 WHERE instance_id='inst-p5-1'`)
		// short lease already set; enable retry_on_lost
		_, err := db.ExecContext(ctx, `UPDATE job_schedule SET retry_policy=?, timeout_seconds=60, manual_enabled=1 WHERE task_id=?`,
			`{"max_attempts":3,"strategy":"FIXED","initial_delay_seconds":1,"retry_on_lost":true,"retry_on_timeout":false}`, taskID)
		if err != nil {
			t.Fatal(err)
		}
		// 新连接，避免前序 cancel 弄脏 hub 会话
		c2 := dial(t, ctx, baseWS)
		defer c2.Close(websocket.StatusNormalClosure, "")
		mustAuth(t, ctx, c2, cred.Token, "p5.svc", "inst-p5-2")
		ack2 := mustRegister(t, ctx, c2, "p5.svc", "inst-p5-2", "1.0.0", "abc12345", []protocol.TaskDecl{{
			TaskCode: "p5.work", TaskName: "Work", HandlerVersion: "1.0.0", DefaultCron: &cron, TimeoutSeconds: 30, MaxConcurrency: 2,
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}})
		if !ack2.OK {
			t.Fatalf("re-register %+v", ack2)
		}
		_, _ = db.ExecContext(ctx, `UPDATE job_instance SET status='ONLINE', running_count=0, max_concurrency=10 WHERE instance_id IN ('inst-p5-1','inst-p5-2')`)

		code, body := signedJSON(t, http.MethodPost, adminBase+fmt.Sprintf("/v1/admin/tasks/%d/executions", taskID),
			map[string]string{"Idempotency-Key": "p5-lost"},
			`{"params":{}}`, secret, []string{"job.task.execute"}, false)
		if code != 202 {
			t.Fatalf("trigger %d %s", code, body)
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

		// force lease expiry in DB
		_, err = db.ExecContext(ctx, `UPDATE job_execution_attempt SET lease_expires_at=DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 1 SECOND) WHERE attempt_id=?`, te.AttemptID)
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(8 * time.Second)
		var attempt2 string
		for time.Now().Before(deadline) {
			drainUntilIdle(t, ctx, c2, 50*time.Millisecond)
			var st string
			_ = db.QueryRowContext(ctx, `SELECT status FROM job_execution_attempt WHERE attempt_id=?`, te.AttemptID).Scan(&st)
			if st == "LOST" {
				// wait for retry dispatch
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
			t.Fatal("did not get retry attempt after lease expiry")
		}
		var st string
		_ = db.QueryRowContext(ctx, `SELECT status FROM job_execution WHERE execution_id=?`, env.Data.ExecutionID).Scan(&st)
		if st != "SUCCEEDED" {
			t.Fatalf("want SUCCEEDED after recover, got %s", st)
		}
		var n int
		_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_execution_attempt WHERE execution_id=?`, env.Data.ExecutionID).Scan(&n)
		if n < 2 {
			t.Fatalf("want >=2 attempts, got %d", n)
		}
	})
}

func cleanupP5(t *testing.T, db *sql.DB) {
	t.Helper()
	cleanupP5Execs(t, db)
	for _, q := range []string{
		"DELETE FROM job_instance_task", "DELETE FROM job_task_state WHERE task_id NOT IN (SELECT id FROM job_definition WHERE service_id IN (SELECT id FROM job_service WHERE service_code IN ('sdk.demo','sdk.demo.php')))", "DELETE FROM job_schedule WHERE task_id NOT IN (SELECT id FROM job_definition WHERE service_id IN (SELECT id FROM job_service WHERE service_code IN ('sdk.demo','sdk.demo.php')))", "DELETE FROM job_definition_version WHERE definition_id NOT IN (SELECT id FROM job_definition WHERE service_id IN (SELECT id FROM job_service WHERE service_code IN ('sdk.demo','sdk.demo.php')))",
		"DELETE FROM job_definition WHERE service_id NOT IN (SELECT id FROM job_service WHERE service_code IN ('sdk.demo','sdk.demo.php'))", "DELETE FROM job_instance", "DELETE FROM job_service_credential WHERE service_id NOT IN (SELECT id FROM job_service WHERE service_code IN ('sdk.demo','sdk.demo.php'))",
		"DELETE FROM job_service WHERE service_code IN ('p5.svc','demo.svc')", "DELETE FROM job_operation_audit",
		"DELETE FROM job_node_lease",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Log(q, err)
		}
	}
}

func cleanupP5Execs(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, q := range []string{
		"DELETE FROM job_execution_log", "DELETE FROM job_execution_event",
		"DELETE FROM job_execution_attempt", "DELETE FROM job_execution",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Log(q, err)
		}
	}
}
