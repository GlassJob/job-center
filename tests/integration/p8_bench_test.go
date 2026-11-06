package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
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
	"github.com/GlassJob/job-center/internal/infrastructure/idgen"
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

type benchEnv struct {
	ctx     context.Context
	cancel  context.CancelFunc
	db      *sql.DB
	store   *repo.Store
	execSvc *appexec.Service
	rt      *appexec.Runtime
	wsURL   string
	svcID   int64
	svcCode string
	token   string
	taskID  int64
}

func setupBenchEnv(b *testing.B) *benchEnv {
	b.Helper()
	cfgPath := os.Getenv("JC_TEST_CONFIG")
	if cfgPath == "" {
		cfgPath = filepath.Join("..", "..", "configs", "local.yaml")
	}
	if _, err := os.Stat(cfgPath); err != nil {
		b.Skip("no local.yaml")
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		b.Fatal(err)
	}
	cfg.NodeID = fmt.Sprintf("bench-%d", time.Now().UnixNano())

	ctx, cancel := context.WithCancel(context.Background())
	log := logger.New("error", "text", io.Discard)
	if _, err := database.MigrateUp(ctx, cfg.Database.DSN, migrations.FS, log); err != nil {
		cancel()
		b.Fatal(err)
	}
	db, err := database.Open(ctx, cfg.Database.DSN, database.PoolOptions{
		MaxOpenConns: 24, MaxIdleConns: 12, ConnMaxLifetime: time.Minute,
	})
	if err != nil {
		cancel()
		b.Fatal(err)
	}

	store := repo.New(db)
	val, err := protocol.DefaultValidator()
	if err != nil {
		db.Close()
		cancel()
		b.Fatal(err)
	}
	reg := registration.New(store, cfg.Security.CredentialPepper, cfg.NodeID, time.Minute, log)
	loc, tz, _, _ := schedule.ResolveDefaultTimezone("", os.Getenv, "")
	cat := catalog.New(store, cfg.Security.CredentialPepper, loc, tz)

	ws := wstransport.NewServer(reg, cfg, val, log)
	api := httpapi.New(db, store, cat, cfg, loc, tz)
	execSvc := appexec.New(store, ws.Hub(), 30*time.Second, log)
	ws.SetExecution(execSvc)
	api.SetExecution(execSvc)

	rt := appexec.NewRuntime(execSvc, appexec.RuntimeOptions{
		NodeID: cfg.NodeID, SweepInterval: time.Minute, LeaseTTL: 30 * time.Second,
		LostGrace: time.Second, TimeoutGrace: time.Second, DispatchInterval: time.Minute,
		StartedAt: time.Now().UTC().Add(-2 * time.Minute),
	})

	mux := http.NewServeMux()
	api.MountExecutor(mux)
	ws.Mount(mux)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		db.Close()
		cancel()
		b.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)

	svcCode := fmt.Sprintf("bench.%d", time.Now().UnixNano()%1_000_000_000)
	actor := catalog.Actor{Client: "bench", Operator: security.Operator{ID: "bench", Name: "bench"}}
	svc, err := cat.CreateService(ctx, actor, catalog.CreateServiceInput{ServiceCode: svcCode, ServiceName: "Bench"})
	if err != nil {
		srv.Close()
		db.Close()
		cancel()
		b.Fatal(err)
	}
	cred, err := cat.CreateCredential(ctx, actor, svc.ID, catalog.CreateCredentialInput{AllowedTaskCodes: []string{"*"}})
	if err != nil {
		srv.Close()
		db.Close()
		cancel()
		b.Fatal(err)
	}

	wsURL := "ws://" + ln.Addr().String() + "/v1/executor/ws"
	c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{Subprotocols: []string{protocol.Subprotocol}})
	if err != nil {
		srv.Close()
		db.Close()
		cancel()
		b.Fatal(err)
	}
	benchAuthAndReg := func(conn *websocket.Conn, inst string) int64 {
		msg, _, _ := protocol.NewMessage(protocol.TypeAuthRequest, "", "", protocol.AuthRequest{
			ServiceCode: svcCode, Credential: cred.Token, InstanceID: inst,
			ProtocolVersions: []string{"1.0"}, SDK: protocol.SDKInfo{Language: "go", Version: "0.1.0"},
		})
		if err := conn.Write(ctx, websocket.MessageText, msg); err != nil {
			b.Fatal(err)
		}
		var env protocol.Envelope
		if err := wsjson.Read(ctx, conn, &env); err != nil {
			b.Fatal(err)
		}
		cron := "0 0 1 1 * *"
		rmsg, _, _ := protocol.NewMessage(protocol.TypeServiceRegister, "", "", protocol.RegisterData{
			ServiceCode: svcCode, ServiceName: "Bench", InstanceID: inst, Environment: "dev",
			Runtime:    protocol.RuntimeInfo{Language: "go", Version: "1.22"},
			Deployment: protocol.DeploymentInfo{Version: "1.0.0", GitCommit: "bench001"},
			Capacity: struct {
				MaxConcurrency int `json:"max_concurrency"`
			}{MaxConcurrency: 1000},
			Tasks: []protocol.TaskDecl{{
				TaskCode: "bench.work", TaskName: "BenchWork", HandlerVersion: "1.0.0",
				DefaultCron: &cron, TimeoutSeconds: 60, MaxConcurrency: 1000,
				InputSchema: json.RawMessage(`{"type":"object"}`),
			}},
			StartedAt: protocol.FormatTime(time.Now()),
		})
		if err := conn.Write(ctx, websocket.MessageText, rmsg); err != nil {
			b.Fatal(err)
		}
		if err := wsjson.Read(ctx, conn, &env); err != nil {
			b.Fatal(err)
		}
		var ack protocol.RegisterAck
		_ = json.Unmarshal(env.Data, &ack)
		if !ack.OK || len(ack.AcceptedTasks) == 0 {
			b.Fatalf("register ack: %+v", ack)
		}
		return ack.AcceptedTasks[0].TaskID
	}
	taskID := benchAuthAndReg(c, svcCode+"-w0")
	_, _ = db.ExecContext(ctx, `UPDATE job_schedule SET concurrency_policy='ALLOW', max_concurrency=1000, manual_enabled=1 WHERE task_id=?`, taskID)
	// drain incoming WS frames on w0 in background
	go func() {
		for {
			if _, _, err := c.Read(ctx); err != nil {
				return
			}
		}
	}()

	b.Cleanup(func() {
		_ = c.Close(websocket.StatusNormalClosure, "")
		_ = srv.Close()
		cleanCtx, cc := context.WithTimeout(context.Background(), 5*time.Second)
		defer cc()
		_, _ = db.ExecContext(cleanCtx, `DELETE l FROM job_execution_log l JOIN job_execution e ON l.execution_id=e.execution_id WHERE e.service_id=?`, svc.ID)
		_, _ = db.ExecContext(cleanCtx, `DELETE v FROM job_execution_event v JOIN job_execution e ON v.execution_id=e.execution_id WHERE e.service_id=?`, svc.ID)
		_, _ = db.ExecContext(cleanCtx, `DELETE a FROM job_execution_attempt a JOIN job_execution e ON a.execution_id=e.execution_id WHERE e.service_id=?`, svc.ID)
		_, _ = db.ExecContext(cleanCtx, `DELETE FROM job_execution WHERE service_id=?`, svc.ID)
		_, _ = db.ExecContext(cleanCtx, `DELETE it FROM job_instance_task it JOIN job_instance i ON it.instance_id=i.instance_id WHERE i.service_id=?`, svc.ID)
		_, _ = db.ExecContext(cleanCtx, `DELETE ts FROM job_task_state ts JOIN job_definition d ON ts.task_id=d.id WHERE d.service_id=?`, svc.ID)
		_, _ = db.ExecContext(cleanCtx, `DELETE s FROM job_schedule s JOIN job_definition d ON s.task_id=d.id WHERE d.service_id=?`, svc.ID)
		_, _ = db.ExecContext(cleanCtx, `DELETE FROM job_definition WHERE service_id=?`, svc.ID)
		_, _ = db.ExecContext(cleanCtx, `DELETE FROM job_instance WHERE service_id=?`, svc.ID)
		_, _ = db.ExecContext(cleanCtx, `DELETE FROM job_service_credential WHERE service_id=?`, svc.ID)
		_, _ = db.ExecContext(cleanCtx, `DELETE FROM job_audit_log WHERE target_id=? OR client_id='bench'`, fmt.Sprintf("%d", svc.ID))
		_, _ = db.ExecContext(cleanCtx, `DELETE FROM job_service WHERE id=?`, svc.ID)
		_ = db.Close()
		cancel()
	})

	return &benchEnv{
		ctx: ctx, cancel: cancel, db: db, store: store, execSvc: execSvc, rt: rt,
		wsURL: wsURL, svcID: svc.ID, svcCode: svcCode, token: cred.Token, taskID: taskID,
	}
}

// 18.4-1: WebSocket 并发连接与认证能力
func BenchmarkP8_WSConcurrentConnect(b *testing.B) {
	env := setupBenchEnv(b)
	var seq atomic.Int64
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			n := seq.Add(1)
			inst := fmt.Sprintf("%s-c-%d", env.svcCode, n)
			c, _, err := websocket.Dial(env.ctx, env.wsURL, &websocket.DialOptions{Subprotocols: []string{protocol.Subprotocol}})
			if err != nil {
				b.Fatal(err)
			}
			msg, _, _ := protocol.NewMessage(protocol.TypeAuthRequest, "", "", protocol.AuthRequest{
				ServiceCode: env.svcCode, Credential: env.token, InstanceID: inst,
				ProtocolVersions: []string{"1.0"}, SDK: protocol.SDKInfo{Language: "go", Version: "0.1.0"},
			})
			if err := c.Write(env.ctx, websocket.MessageText, msg); err != nil {
				b.Fatal(err)
			}
			var resp protocol.Envelope
			if err := wsjson.Read(env.ctx, c, &resp); err != nil {
				b.Fatal(err)
			}
			_ = c.Close(websocket.StatusNormalClosure, "")
		}
	})
}

// 18.4-2 & 18.4-3: 任务下发延迟 + 状态流转更新吞吐（Trigger -> Ack -> Started -> Completed）
func BenchmarkP8_DispatchAndStatusLifecycle(b *testing.B) {
	env := setupBenchEnv(b)
	op := security.Operator{ID: "bench", Name: "bench"}
	inst := env.svcCode + "-w0"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = env.db.ExecContext(env.ctx, `UPDATE job_instance SET running_count=0 WHERE instance_id=?`, inst)
		tr, code, err := env.execSvc.ManualTrigger(env.ctx, appexec.TriggerInput{
			TaskID: env.taskID, Params: json.RawMessage(`{}`),
			IdempotencyKey: fmt.Sprintf("bench-lc-%d", i), Operator: op, Client: "bench",
		})
		if err != nil || code != 202 {
			b.Fatalf("trigger %d %v", code, err)
		}
		att, err := env.store.Q().GetAttempt(env.ctx, tr.AttemptID)
		if err != nil || att.LeaseToken == nil {
			b.Fatalf("attempt %v", err)
		}
		tok := *att.LeaseToken
		now := protocol.FormatTime(time.Now().UTC())
		if ack, err := env.execSvc.OnExecuteAck(env.ctx, inst, protocol.TaskExecuteAck{AttemptID: tr.AttemptID, LeaseToken: tok, Accepted: true}); err != nil || ack.Status != "OK" {
			b.Fatalf("ack %+v %v", ack, err)
		}
		if ack, err := env.execSvc.OnStarted(env.ctx, inst, protocol.TaskStarted{AttemptID: tr.AttemptID, LeaseToken: tok, StartedAt: now}); err != nil || ack.Status != "OK" {
			b.Fatalf("started %+v %v", ack, err)
		}
		if ack, err := env.execSvc.OnCompleted(env.ctx, inst, protocol.TaskCompleted{AttemptID: tr.AttemptID, LeaseToken: tok, FinishedAt: now, Result: json.RawMessage(`{"ok":1}`)}); err != nil || ack.Status != "OK" {
			b.Fatalf("completed %+v %v", ack, err)
		}
	}
}

// 18.4-4: 日志批量写入吞吐（每批 20 条日志写入分区表）
func BenchmarkP8_LogWriteThroughput(b *testing.B) {
	env := setupBenchEnv(b)
	op := security.Operator{ID: "bench", Name: "bench"}
	inst := env.svcCode + "-w0"
	tr, code, err := env.execSvc.ManualTrigger(env.ctx, appexec.TriggerInput{
		TaskID: env.taskID, Params: json.RawMessage(`{}`), IdempotencyKey: "bench-log-exec", Operator: op, Client: "bench",
	})
	if err != nil || code != 202 {
		b.Fatalf("trigger %d %v", code, err)
	}
	att, err := env.store.Q().GetAttempt(env.ctx, tr.AttemptID)
	if err != nil || att.LeaseToken == nil {
		b.Fatal(err)
	}
	tok := *att.LeaseToken
	now := protocol.FormatTime(time.Now().UTC())
	_, _ = env.execSvc.OnExecuteAck(env.ctx, inst, protocol.TaskExecuteAck{AttemptID: tr.AttemptID, LeaseToken: tok, Accepted: true})
	_, _ = env.execSvc.OnStarted(env.ctx, inst, protocol.TaskStarted{AttemptID: tr.AttemptID, LeaseToken: tok, StartedAt: now})

	const batchSize = 20
	var seq int64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		entries := make([]protocol.TaskLogEntry, batchSize)
		for j := 0; j < batchSize; j++ {
			seq++
			entries[j] = protocol.TaskLogEntry{Seq: seq, Level: "INFO", Message: "benchmark log line payload", Stream: "stdout", Ts: now}
		}
		ack, err := env.execSvc.OnLog(env.ctx, inst, protocol.TaskLog{AttemptID: tr.AttemptID, LeaseToken: tok, Entries: entries})
		if err != nil || ack.Status != "OK" {
			b.Fatalf("log %+v %v", ack, err)
		}
	}
	b.ReportMetric(float64(b.N*batchSize)/b.Elapsed().Seconds(), "lines/s")
}

// 18.4-5 & 18.4-6: MySQL 锁竞争与高并发抢占状态一致性（8 协程并发 DispatchOnce 抢同一 PENDING 执行，断言永远仅生成 1 个 Attempt）
func BenchmarkP8_ConcurrentClaimConsistency(b *testing.B) {
	env := setupBenchEnv(b)
	inst := env.svcCode + "-w0"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = env.db.ExecContext(env.ctx, `UPDATE job_instance SET running_count=0 WHERE instance_id=?`, inst)
		execID := idgen.NewULID()
		_, err := env.db.ExecContext(env.ctx, `INSERT INTO job_execution(execution_id, task_id, service_id, task_code, trigger_type, triggered_by, params, status, handler_version, handler_major, schema_hash, timeout_seconds, retry_policy, max_attempts, attempt_count)
			VALUES (?,?,?,?,'MANUAL','bench','{}','PENDING','1.0.0',1,'deadbeef',60,'{"max_attempts":1}',1,0)`,
			execID, env.taskID, env.svcID, "bench.work")
		if err != nil {
			b.Fatal(err)
		}
		var wg sync.WaitGroup
		for w := 0; w < 8; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = env.rt.DispatchOnce(env.ctx)
			}()
		}
		wg.Wait()
		var cnt int
		if err := env.db.QueryRowContext(env.ctx, `SELECT COUNT(*) FROM job_execution_attempt WHERE execution_id=?`, execID).Scan(&cnt); err != nil {
			b.Fatal(err)
		}
		if cnt != 1 {
			b.Fatalf("state inconsistency: expected 1 attempt under 8-way lock contention, got %d", cnt)
		}
	}
}
