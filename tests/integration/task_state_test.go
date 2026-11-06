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
	"strconv"
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

func cleanupStateTest(t *testing.T, db *sql.DB) {
	t.Helper()
	queries := []string{
		"DELETE l FROM job_execution_log l JOIN job_execution e ON l.execution_id=e.execution_id JOIN job_service s ON e.service_id=s.id WHERE s.service_code='state.svc'",
		"DELETE ev FROM job_execution_event ev JOIN job_execution e ON ev.execution_id=e.execution_id JOIN job_service s ON e.service_id=s.id WHERE s.service_code='state.svc'",
		"DELETE a FROM job_execution_attempt a JOIN job_service s ON a.service_id=s.id WHERE s.service_code='state.svc'",
		"DELETE e FROM job_execution e JOIN job_service s ON e.service_id=s.id WHERE s.service_code='state.svc'",
		"DELETE it FROM job_instance_task it JOIN job_definition d ON it.definition_id=d.id JOIN job_service s ON d.service_id=s.id WHERE s.service_code='state.svc'",
		"DELETE ts FROM job_task_state ts JOIN job_definition d ON ts.task_id=d.id JOIN job_service s ON d.service_id=s.id WHERE s.service_code='state.svc'",
		"DELETE sch FROM job_schedule sch JOIN job_definition d ON sch.task_id=d.id JOIN job_service s ON d.service_id=s.id WHERE s.service_code='state.svc'",
		"DELETE dv FROM job_definition_version dv JOIN job_definition d ON dv.definition_id=d.id JOIN job_service s ON d.service_id=s.id WHERE s.service_code='state.svc'",
		"DELETE d FROM job_definition d JOIN job_service s ON d.service_id=s.id WHERE s.service_code='state.svc'",
		"DELETE i FROM job_instance i JOIN job_service s ON i.service_id=s.id WHERE s.service_code='state.svc'",
		"DELETE c FROM job_service_credential c JOIN job_service s ON c.service_id=s.id WHERE s.service_code='state.svc'",
		"DELETE FROM job_service WHERE service_code='state.svc'",
		"DELETE FROM job_node_lease",
	}
	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			t.Log(q, err)
		}
	}
}

func readTaskExec(t *testing.T, ctx context.Context, c *websocket.Conn) protocol.TaskExecute {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		env := readEnv(t, ctx, c)
		if env.Type == protocol.TypeTaskExecute {
			var te protocol.TaskExecute
			if err := json.Unmarshal(env.Data, &te); err != nil {
				t.Fatal(err)
			}
			return te
		}
	}
	t.Fatal("did not receive task.execute")
	return protocol.TaskExecute{}
}

func readResultAck(t *testing.T, ctx context.Context, c *websocket.Conn) protocol.TaskResultAck {
	t.Helper()
	for {
		env := readEnv(t, ctx, c)
		if env.Type == protocol.TypeTaskResultAck {
			var ra protocol.TaskResultAck
			if err := json.Unmarshal(env.Data, &ra); err != nil {
				t.Fatal(err)
			}
			return ra
		}
	}
}

func TestTaskStateCheckpointAndDefaultParams(t *testing.T) {
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
	cfg.HTTP.ExecutorAddr = "127.0.0.1:28390"
	cfg.HTTP.AdminAddr = "127.0.0.1:28391"
	cfg.NodeID = "state-test-node"
	if cfg.Security.AdminClients == nil {
		cfg.Security.AdminClients = map[string]config.AdminClient{}
	}
	secret := "dev-admin-secret-16b"
	cfg.Security.AdminClients["glassadmin"] = config.AdminClient{Secret: secret, MaxPerms: []string{"*"}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := logger.New("error", "text", io.Discard)
	if _, err := database.MigrateUp(ctx, cfg.Database.DSN, migrations.FS, log); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(ctx, cfg.Database.DSN, database.PoolOptions{MaxOpenConns: 10, MaxIdleConns: 5, ConnMaxLifetime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cleanupStateTest(t, db)
	defer cleanupStateTest(t, db)

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
	leaseTTL := 30 * time.Second
	execSvc := appexec.New(store, ws.Hub(), leaseTTL, log)
	ws.SetExecution(execSvc)
	api.SetExecution(execSvc)

	sch := scheduler.New(store, scheduler.Options{
		NodeID: cfg.NodeID, TickInterval: 150 * time.Millisecond, LeaseTTL: 5 * time.Second,
		DefaultLocation: loc, BatchSize: 50,
	}, log)
	rt := appexec.NewRuntime(execSvc, appexec.RuntimeOptions{
		NodeID: cfg.NodeID, SweepInterval: 200 * time.Millisecond, LeaseTTL: leaseTTL,
		LostGrace: 500 * time.Millisecond, TimeoutGrace: time.Second,
		DispatchInterval: 150 * time.Millisecond, StartedAt: time.Now().UTC().Add(-2 * time.Minute),
	})
	go sch.Run(ctx)
	go rt.Run(ctx)

	execMux := http.NewServeMux()
	api.MountExecutor(execMux)
	ws.Mount(execMux)
	adminMux := http.NewServeMux()
	api.MountAdmin(adminMux)
	execSrv := &http.Server{Addr: cfg.HTTP.ExecutorAddr, Handler: execMux}
	adminSrv := &http.Server{Addr: cfg.HTTP.AdminAddr, Handler: adminMux}
	go execSrv.ListenAndServe()
	go adminSrv.ListenAndServe()
	defer execSrv.Close()
	defer adminSrv.Close()
	time.Sleep(150 * time.Millisecond)

	actor := catalog.Actor{Client: "glassadmin", Operator: security.Operator{ID: "1", Name: "alice"}}
	svc, err := cat.CreateService(ctx, actor, catalog.CreateServiceInput{ServiceCode: "state.svc", ServiceName: "State Svc"})
	if err != nil {
		t.Fatal(err)
	}
	cred, err := cat.CreateCredential(ctx, actor, svc.ID, catalog.CreateCredentialInput{AllowedTaskCodes: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}

	wsURL := "ws://" + cfg.HTTP.ExecutorAddr + "/v1/executor/ws"
	conn := dial(t, ctx, wsURL)
	defer conn.CloseNow()
	mustAuth(t, ctx, conn, cred.Token, "state.svc", "state-inst-1")

	schema := json.RawMessage(`{"type":"object","required":["batch_size"],"properties":{"batch_size":{"type":"integer","minimum":1},"override_cursor":{"type":"integer"}},"additionalProperties":false}`)
	ack := mustRegister(t, ctx, conn, "state.svc", "state-inst-1", "1.0.0", "abc1234", []protocol.TaskDecl{
		{
			TaskCode:       "demo.incremental_sync",
			TaskName:       "Incremental Sync",
			HandlerVersion: "1.0.0",
			InputSchema:    schema,
			TimeoutSeconds: 60,
			MaxConcurrency: 2,
		},
	})
	if !ack.OK || len(ack.AcceptedTasks) != 1 {
		t.Fatalf("register failed: %+v", ack)
	}

	var taskID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM job_definition WHERE service_id=? AND task_code='demo.incremental_sync'`, svc.ID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	base := "http://" + cfg.HTTP.AdminAddr

	// 1. Uninitialized state view via GET /state & GET /tasks/{id}
	t.Run("uninitialized_logical_view", func(t *testing.T) {
		code, body := signedJSON(t, http.MethodGet, fmt.Sprintf("%s/v1/admin/tasks/%d/state", base, taskID),
			nil, "", secret, []string{"job.task.view"}, false)
		if code != 200 {
			t.Fatalf("GET /state status=%d body=%s", code, body)
		}
		var resp struct {
			Data struct {
				State           map[string]any `json:"state"`
				Version         uint64         `json:"version"`
				UpdatedBy       *string        `json:"updated_by"`
				LastExecutionID *string        `json:"last_execution_id"`
				LastAttemptID   *string        `json:"last_attempt_id"`
				UpdatedAt       *string        `json:"updated_at"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Data.Version != 0 || len(resp.Data.State) != 0 || resp.Data.UpdatedBy != nil {
			t.Fatalf("unexpected initial state: %s", body)
		}

		// TaskDetail includes task_state
		code, body = signedJSON(t, http.MethodGet, fmt.Sprintf("%s/v1/admin/tasks/%d", base, taskID),
			nil, "", secret, []string{"job.task.view"}, false)
		if code != 200 {
			t.Fatalf("GET /tasks/{id} status=%d body=%s", code, body)
		}
		var td struct {
			Data struct {
				TaskState *struct {
					Version uint64         `json:"version"`
					State   map[string]any `json:"state"`
				} `json:"task_state"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(body), &td); err != nil {
			t.Fatal(err)
		}
		if td.Data.TaskState == nil || td.Data.TaskState.Version != 0 || len(td.Data.TaskState.State) != 0 {
			t.Fatalf("TaskDetail missing task_state: %s", body)
		}
	})

	// 2. Execute #1: receives state={}, state_version=0; checkpoint + lease fence + completed(next_state) + idempotent replay
	t.Run("ws_checkpoint_lease_fence_and_completed_next_state", func(t *testing.T) {
		code, body := signedJSON(t, http.MethodPost, fmt.Sprintf("%s/v1/admin/tasks/%d/executions", base, taskID),
			map[string]string{"Idempotency-Key": "state-idem-1"}, `{"params":{"batch_size":50}}`, secret, []string{"job.task.execute"}, false)
		if code != 202 {
			t.Fatalf("trigger status=%d body=%s", code, body)
		}

		te := readTaskExec(t, ctx, conn)
		if te.StateVersion != 0 || string(te.State) != "{}" {
			t.Fatalf("expected initial state={} state_version=0, got state=%s ver=%d", string(te.State), te.StateVersion)
		}

		send(t, ctx, conn, protocol.TypeTaskExecuteAck, protocol.TaskExecuteAck{
			AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, Accepted: true,
		})
		if ra := readResultAck(t, ctx, conn); ra.Status != "OK" {
			t.Fatalf("execute_ack failed: %+v", ra)
		}
		send(t, ctx, conn, protocol.TypeTaskStarted, protocol.TaskStarted{
			AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, StartedAt: protocol.FormatTime(time.Now()),
		})
		if ra := readResultAck(t, ctx, conn); ra.Status != "OK" {
			t.Fatalf("started ack: %+v", ra)
		}

		// Valid checkpoint -> version becomes 1 + CHECKPOINT event recorded
		send(t, ctx, conn, protocol.TypeTaskCheckpoint, protocol.TaskCheckpoint{
			AttemptID:  te.AttemptID,
			LeaseToken: te.LeaseToken,
			State:      json.RawMessage(`{"cursor":50,"page":1}`),
		})
		if ra := readResultAck(t, ctx, conn); ra.Status != "OK" {
			t.Fatalf("checkpoint ack failed: %+v", ra)
		}

		var cpEvents int
		_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_execution_event WHERE execution_id=? AND event_type='CHECKPOINT'`, te.ExecutionID).Scan(&cpEvents)
		if cpEvents != 1 {
			t.Fatalf("expected 1 CHECKPOINT event, got %d", cpEvents)
		}

		// Invalid lease_token on checkpoint -> REJECTED(LEASE_INVALID) + STALE_RESULT_REJECTED
		send(t, ctx, conn, protocol.TypeTaskCheckpoint, protocol.TaskCheckpoint{
			AttemptID:  te.AttemptID,
			LeaseToken: "00000000000000000000000000000000",
			State:      json.RawMessage(`{"cursor":9999}`),
		})
		raBad := readResultAck(t, ctx, conn)
		if raBad.Status != "REJECTED" || raBad.Code != protocol.ErrLeaseInvalid || raBad.ErrorCode != protocol.ErrLeaseInvalid {
			t.Fatalf("expected REJECTED LEASE_INVALID, got %+v", raBad)
		}
		var staleEvents int
		_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_execution_event WHERE execution_id=? AND event_type='STALE_RESULT_REJECTED'`, te.ExecutionID).Scan(&staleEvents)
		if staleEvents != 1 {
			t.Fatalf("expected 1 STALE_RESULT_REJECTED event, got %d", staleEvents)
		}

		// Complete with next_state -> version becomes 2
		compMsg := protocol.TaskCompleted{
			AttemptID:  te.AttemptID,
			LeaseToken: te.LeaseToken,
			FinishedAt: protocol.FormatTime(time.Now()),
			Result:     json.RawMessage(`{"synced":100}`),
			NextState:  json.RawMessage(`{"cursor":100,"page":2}`),
		}
		send(t, ctx, conn, protocol.TypeTaskCompleted, compMsg)
		if ra := readResultAck(t, ctx, conn); ra.Status != "OK" {
			t.Fatalf("completed ack failed: %+v", ra)
		}

		// Replay identical task.completed -> idempotent OK, version stays 2!
		send(t, ctx, conn, protocol.TypeTaskCompleted, compMsg)
		if ra := readResultAck(t, ctx, conn); ra.Status != "OK" {
			t.Fatalf("replayed completed ack failed: %+v", ra)
		}

		ts, err := store.Q().GetTaskState(ctx, taskID)
		if err != nil {
			t.Fatal(err)
		}
		var stMap map[string]any
		_ = json.Unmarshal(ts.State, &stMap)
		if ts.Version != 2 || stMap["cursor"].(float64) != 100 || stMap["page"].(float64) != 2 {
			t.Fatalf("expected version=2 cursor=100 page=2, got ver=%d state=%s", ts.Version, string(ts.State))
		}
		if ts.UpdatedBy == nil || *ts.UpdatedBy != "attempt:"+te.AttemptID {
			t.Fatalf("unexpected updated_by=%v", ts.UpdatedBy)
		}
	})

	// 3. Execute #2: receives state={"cursor":100,"page":2}, state_version=2; test cancel_state & expired lease fence; test completed without next_state
	t.Run("lease_fence_cancel_and_expired_plus_null_next_state", func(t *testing.T) {
		code, body := signedJSON(t, http.MethodPost, fmt.Sprintf("%s/v1/admin/tasks/%d/executions", base, taskID),
			map[string]string{"Idempotency-Key": "state-idem-2"}, `{"params":{"batch_size":25}}`, secret, []string{"job.task.execute"}, false)
		if code != 202 {
			t.Fatalf("trigger #2 status=%d body=%s", code, body)
		}
		te := readTaskExec(t, ctx, conn)
		var teSt map[string]any
		_ = json.Unmarshal(te.State, &teSt)
		if te.StateVersion != 2 || teSt["cursor"].(float64) != 100 || teSt["page"].(float64) != 2 {
			t.Fatalf("expected state_version=2 cursor=100 page=2, got ver=%d state=%s", te.StateVersion, string(te.State))
		}
		send(t, ctx, conn, protocol.TypeTaskExecuteAck, protocol.TaskExecuteAck{
			AttemptID: te.AttemptID, LeaseToken: te.LeaseToken, Accepted: true,
		})
		if ra := readResultAck(t, ctx, conn); ra.Status != "OK" {
			t.Fatalf("execute_ack failed: %+v", ra)
		}

		// Complete with next_state omitted -> state & version remain 2!
		send(t, ctx, conn, protocol.TypeTaskCompleted, protocol.TaskCompleted{
			AttemptID:  te.AttemptID,
			LeaseToken: te.LeaseToken,
			FinishedAt: protocol.FormatTime(time.Now()),
		})
		if ra := readResultAck(t, ctx, conn); ra.Status != "OK" {
			t.Fatalf("completed without next_state failed: %+v", ra)
		}
		ts, _ := store.Q().GetTaskState(ctx, taskID)
		if ts.Version != 2 {
			t.Fatalf("omitted next_state must not advance version, got %d", ts.Version)
		}

		// Trigger #3 to test cancel_state != 'NONE' and expired lease rejection on checkpoint/completed
		code, body = signedJSON(t, http.MethodPost, fmt.Sprintf("%s/v1/admin/tasks/%d/executions", base, taskID),
			map[string]string{"Idempotency-Key": "state-idem-3"}, `{"params":{"batch_size":10}}`, secret, []string{"job.task.execute"}, false)
		if code != 202 {
			t.Fatalf("trigger #3 status=%d body=%s", code, body)
		}
		te3 := readTaskExec(t, ctx, conn)
		send(t, ctx, conn, protocol.TypeTaskExecuteAck, protocol.TaskExecuteAck{
			AttemptID: te3.AttemptID, LeaseToken: te3.LeaseToken, Accepted: true,
		})
		if ra := readResultAck(t, ctx, conn); ra.Status != "OK" {
			t.Fatalf("execute_ack #3 failed: %+v", ra)
		}

		// Mark cancel_state = 'REQUESTED' directly to verify checkpoint fence rejects when cancel_state != 'NONE'
		_, err := db.ExecContext(ctx, `UPDATE job_execution_attempt SET cancel_state='REQUESTED' WHERE attempt_id=?`, te3.AttemptID)
		if err != nil {
			t.Fatal(err)
		}
		send(t, ctx, conn, protocol.TypeTaskCheckpoint, protocol.TaskCheckpoint{
			AttemptID: te3.AttemptID, LeaseToken: te3.LeaseToken, State: json.RawMessage(`{"cursor":777}`),
		})
		if ra := readResultAck(t, ctx, conn); ra.Status != "REJECTED" || ra.Code != protocol.ErrLeaseInvalid {
			t.Fatalf("expected checkpoint rejection on cancel_state=REQUESTED, got %+v", ra)
		}

		// Force lease_expires_at in the past and reset cancel_state='NONE' to verify expired lease fence on both checkpoint & completed
		_, err = db.ExecContext(ctx, `UPDATE job_execution_attempt SET cancel_state='NONE', lease_expires_at = DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 10 SECOND) WHERE attempt_id=?`, te3.AttemptID)
		if err != nil {
			t.Fatal(err)
		}
		send(t, ctx, conn, protocol.TypeTaskCheckpoint, protocol.TaskCheckpoint{
			AttemptID: te3.AttemptID, LeaseToken: te3.LeaseToken, State: json.RawMessage(`{"cursor":888}`),
		})
		if ra := readResultAck(t, ctx, conn); ra.Status != "REJECTED" || ra.Code != protocol.ErrLeaseInvalid {
			t.Fatalf("expected checkpoint rejection on expired lease, got %+v", ra)
		}
		send(t, ctx, conn, protocol.TypeTaskCompleted, protocol.TaskCompleted{
			AttemptID: te3.AttemptID, LeaseToken: te3.LeaseToken, FinishedAt: protocol.FormatTime(time.Now()),
			NextState: json.RawMessage(`{"cursor":888}`),
		})
		if ra := readResultAck(t, ctx, conn); ra.Status != "REJECTED" || ra.Code != protocol.ErrLeaseInvalid {
			t.Fatalf("expected completed rejection on expired lease, got %+v", ra)
		}
		// Clean up execution #3 as CANCELLED so task concurrency is clean
		_, _ = db.ExecContext(ctx, `UPDATE job_execution_attempt SET status='CANCELLED' WHERE attempt_id=?`, te3.AttemptID)
		_, _ = db.ExecContext(ctx, `UPDATE job_execution SET status='CANCELLED' WHERE execution_id=?`, te3.ExecutionID)

		ts, _ = store.Q().GetTaskState(ctx, taskID)
		if ts.Version != 2 {
			t.Fatalf("rejected checkpoints/completions must not advance version, got %d", ts.Version)
		}
	})

	// 4. Admin PUT /state (optimistic lock 409 + update + audit) & DELETE /state (reset to {} with version+1 + audit)
	t.Run("admin_put_and_delete_state_optimistic_lock", func(t *testing.T) {
		// Wrong If-Match -> 409 VERSION_CONFLICT
		code, body := signedJSON(t, http.MethodPut, fmt.Sprintf("%s/v1/admin/tasks/%d/state", base, taskID),
			map[string]string{"If-Match": `"1"`}, `{"state":{"cursor":500}}`, secret, []string{"job.task.update"}, false)
		if code != 409 {
			t.Fatalf("expected 409 on stale If-Match, got %d %s", code, body)
		}

		// Matching quoted If-Match: "2" -> 200, version=3, updated_by="admin:1"
		code, body = signedJSON(t, http.MethodPut, fmt.Sprintf("%s/v1/admin/tasks/%d/state", base, taskID),
			map[string]string{"If-Match": `"2"`}, `{"state":{"cursor":500}}`, secret, []string{"job.task.update"}, false)
		if code != 200 {
			t.Fatalf("PUT /state failed: %d %s", code, body)
		}
		var putResp struct {
			Data struct {
				State     map[string]any `json:"state"`
				Version   uint64         `json:"version"`
				UpdatedBy string         `json:"updated_by"`
			} `json:"data"`
		}
		_ = json.Unmarshal([]byte(body), &putResp)
		if putResp.Data.Version != 3 || putResp.Data.UpdatedBy != "admin:1" || putResp.Data.State["cursor"].(float64) != 500 {
			t.Fatalf("unexpected PUT response: %s", body)
		}

		// DELETE /state with If-Match: "3" -> resets state={} AND increments version to 4 (avoids ABA!)
		code, body = signedJSON(t, http.MethodDelete, fmt.Sprintf("%s/v1/admin/tasks/%d/state", base, taskID),
			map[string]string{"If-Match": `"3"`}, "", secret, []string{"job.task.update"}, false)
		if code != 200 {
			t.Fatalf("DELETE /state failed: %d %s", code, body)
		}
		var delResp struct {
			Data struct {
				State     map[string]any `json:"state"`
				Version   uint64         `json:"version"`
				UpdatedBy string         `json:"updated_by"`
			} `json:"data"`
		}
		_ = json.Unmarshal([]byte(body), &delResp)
		if delResp.Data.Version != 4 || len(delResp.Data.State) != 0 || delResp.Data.UpdatedBy != "admin:1" {
			t.Fatalf("unexpected DELETE response: %s", body)
		}

		// Verify audit logs written for task.state.update and task.state.reset
		var updateAudits, resetAudits int
		_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_operation_audit WHERE resource_type='task' AND resource_id=? AND action='task.state.update' AND result='SUCCESS'`, strconv.FormatInt(taskID, 10)).Scan(&updateAudits)
		_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_operation_audit WHERE resource_type='task' AND resource_id=? AND action='task.state.reset' AND result='SUCCESS'`, strconv.FormatInt(taskID, 10)).Scan(&resetAudits)
		if updateAudits < 1 || resetAudits < 1 {
			t.Fatalf("missing audit records: update=%d reset=%d", updateAudits, resetAudits)
		}
	})

	// 5. Schedule default_params validation against input_schema + Cron trigger populates execution.params
	t.Run("schedule_default_params_validation_and_cron_fire", func(t *testing.T) {
		curSch, err := store.Q().GetSchedule(ctx, taskID)
		if err != nil {
			t.Fatal(err)
		}

		// Missing required batch_size when switching to CRON with invalid default_params -> 422 PARAMS_SCHEMA_INVALID
		code, body := signedJSON(t, http.MethodPut, fmt.Sprintf("%s/v1/admin/tasks/%d/schedule", base, taskID),
			map[string]string{"If-Match": fmt.Sprintf(`"%d"`, curSch.Version)},
			`{"schedule_type":"CRON","cron_expression":"*/1 * * * * *","default_params":{"override_cursor":10}}`,
			secret, []string{"job.schedule.update"}, false)
		if code != 422 {
			t.Fatalf("expected 422 PARAMS_SCHEMA_INVALID, got %d %s", code, body)
		}

		// Valid default_params -> 200 OK
		code, body = signedJSON(t, http.MethodPut, fmt.Sprintf("%s/v1/admin/tasks/%d/schedule", base, taskID),
			map[string]string{"If-Match": fmt.Sprintf(`"%d"`, curSch.Version)},
			`{"schedule_type":"CRON","cron_expression":"*/1 * * * * *","default_params":{"batch_size":200,"override_cursor":42}}`,
			secret, []string{"job.schedule.update"}, false)
		if code != 200 {
			t.Fatalf("PUT /schedule with valid default_params failed: %d %s", code, body)
		}
		var schResp struct {
			Data struct {
				Version       int64          `json:"version"`
				DefaultParams map[string]any `json:"default_params"`
			} `json:"data"`
		}
		_ = json.Unmarshal([]byte(body), &schResp)
		if schResp.Data.DefaultParams["batch_size"].(float64) != 200 {
			t.Fatalf("default_params not returned on schedule: %s", body)
		}

		// Enable task schedule so Cron fires within ~1.5s and passes default_params + state_version=4!
		code, body = signedJSON(t, http.MethodPut, fmt.Sprintf("%s/v1/admin/tasks/%d/enabled", base, taskID),
			map[string]string{"If-Match": fmt.Sprintf(`"%d"`, schResp.Data.Version)},
			`{"enabled":true}`, secret, []string{"job.task.update"}, false)
		if code != 200 {
			t.Fatalf("enable failed: %d %s", code, body)
		}

		teCron := readTaskExec(t, ctx, conn)
		if teCron.TriggerType != "CRON" {
			t.Fatalf("expected CRON trigger, got %s", teCron.TriggerType)
		}
		var gotParams map[string]any
		_ = json.Unmarshal(teCron.Params, &gotParams)
		if gotParams["batch_size"].(float64) != 200 || gotParams["override_cursor"].(float64) != 42 {
			t.Fatalf("expected Cron execution params from default_params, got %s", string(teCron.Params))
		}
		if teCron.StateVersion != 4 || string(teCron.State) != "{}" {
			t.Fatalf("expected reset state={} ver=4 on Cron execute, got ver=%d state=%s", teCron.StateVersion, string(teCron.State))
		}

		// Disable schedule & complete execution cleanly
		curSch2, _ := store.Q().GetSchedule(ctx, taskID)
		_, _ = signedJSON(t, http.MethodPut, fmt.Sprintf("%s/v1/admin/tasks/%d/enabled", base, taskID),
			map[string]string{"If-Match": strconv.FormatInt(curSch2.Version, 10)},
			`{"enabled":false}`, secret, []string{"job.task.update"}, false)

		send(t, ctx, conn, protocol.TypeTaskExecuteAck, protocol.TaskExecuteAck{
			AttemptID: teCron.AttemptID, LeaseToken: teCron.LeaseToken, Accepted: true,
		})
		_ = readResultAck(t, ctx, conn)
		send(t, ctx, conn, protocol.TypeTaskCompleted, protocol.TaskCompleted{
			AttemptID: teCron.AttemptID, LeaseToken: teCron.LeaseToken, FinishedAt: protocol.FormatTime(time.Now()),
		})
		_ = readResultAck(t, ctx, conn)
	})
}
