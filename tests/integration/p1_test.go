package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GlassJob/job-center/internal/application/catalog"
	"github.com/GlassJob/job-center/internal/application/monitoring"
	"github.com/GlassJob/job-center/internal/application/registration"
	"github.com/GlassJob/job-center/internal/config"
	"github.com/GlassJob/job-center/internal/domain/schedule"
	"github.com/GlassJob/job-center/internal/infrastructure/database"
	"strconv"
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

func TestP1ExecutorLifecycle(t *testing.T) {
	cfgPath := os.Getenv("JC_TEST_CONFIG")
	if cfgPath == "" {
		cfgPath = filepath.Join("..", "..", "configs", "local.yaml")
	}
	if _, err := os.Stat(cfgPath); err != nil {
		t.Skip("no local.yaml; set JC_TEST_CONFIG")
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	// use unique ports for test
	cfg.HTTP.ExecutorAddr = "127.0.0.1:28080"
	cfg.HTTP.AdminAddr = "127.0.0.1:28081"
	cfg.NodeID = "p1-test"
	cfg.Instance.SuspectAfter = config.Duration(2 * time.Second)
	cfg.Instance.OfflineAfter = config.Duration(3 * time.Second)
	cfg.Instance.SweepInterval = config.Duration(500 * time.Millisecond)
	cfg.Instance.HeartbeatInterval = config.Duration(1 * time.Second)
	cfg.WebSocket.AuthTimeout = config.Duration(3 * time.Second)
	cfg.WebSocket.PingInterval = config.Duration(30 * time.Second)
	cfg.WebSocket.PingTimeout = config.Duration(60 * time.Second)
	if cfg.Security.AdminClients == nil {
		cfg.Security.AdminClients = map[string]config.AdminClient{}
	}
	cfg.Security.AdminClients["glassadmin"] = config.AdminClient{Secret: "dev-admin-secret-16b", MaxPerms: []string{"*"}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := logger.New("error", "text", os.Stderr)

	if _, err := database.MigrateUp(ctx, cfg.Database.DSN, migrations.FS, log); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(ctx, cfg.Database.DSN, database.PoolOptions{MaxOpenConns: 10, MaxIdleConns: 5, ConnMaxLifetime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// isolate test data
	cleanup(t, db)
	store := repo.New(db)
	val, err := protocol.DefaultValidator()
	if err != nil {
		t.Fatal(err)
	}
	reg := registration.New(store, cfg.Security.CredentialPepper, cfg.NodeID, time.Minute, log)
	loc, tz, _, _ := schedule.ResolveDefaultTimezone("", os.Getenv, "")
	cat := catalog.New(store, cfg.Security.CredentialPepper, loc, tz)
	mon := monitoring.New(store, monitoring.Options{
		SuspectAfter: cfg.Instance.SuspectAfter.D(), OfflineAfter: cfg.Instance.OfflineAfter.D(),
		SweepInterval: cfg.Instance.SweepInterval.D(), NodeID: cfg.NodeID,
	}, log)
	go mon.Run(ctx)

	ws := wstransport.NewServer(reg, cfg, val, log)
	api := httpapi.New(db, store, cat, cfg, loc, tz)
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
	time.Sleep(200 * time.Millisecond)

	actor := catalog.Actor{Client: "test", Operator: security.Operator{ID: "t", Name: "tester"}}
	svc, err := cat.CreateService(ctx, actor, catalog.CreateServiceInput{ServiceCode: "demo.svc", ServiceName: "Demo"})
	if err != nil {
		t.Fatal(err)
	}
	cred, err := cat.CreateCredential(ctx, actor, svc.ID, catalog.CreateCredentialInput{AllowedTaskCodes: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}

	base := "ws://" + cfg.HTTP.ExecutorAddr + "/v1/executor/ws"

	t.Run("auth_ok", func(t *testing.T) {
		c := dial(t, ctx, base)
		defer c.Close(websocket.StatusNormalClosure, "")
		resp := mustAuth(t, ctx, c, cred.Token, "demo.svc", "inst-auth-ok")
		if !resp.OK || resp.SessionEpoch < 1 {
			t.Fatalf("auth resp: %+v", resp)
		}
	})

	t.Run("auth_bad_token", func(t *testing.T) {
		c := dial(t, ctx, base)
		defer c.Close(websocket.StatusNormalClosure, "")
		send(t, ctx, c, protocol.TypeAuthRequest, protocol.AuthRequest{
			ServiceCode: "demo.svc", Credential: "jcs_badkeyid123456.ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklm",
			InstanceID: "inst-bad", ProtocolVersions: []string{"1.0"}, SDK: protocol.SDKInfo{Language: "go", Version: "0.0.1"},
		})
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		var env protocol.Envelope
		err := wsjson.Read(rctx, c, &env)
		if err != nil {
			code := websocket.CloseStatus(err)
			if code != protocol.CloseAuthFailed {
				t.Fatalf("want close %d or auth.response, got err=%v code=%v", protocol.CloseAuthFailed, err, code)
			}
			return
		}
		if env.Type != protocol.TypeAuthResponse {
			t.Fatalf("type=%s", env.Type)
		}
		var resp protocol.AuthResponse
		_ = json.Unmarshal(env.Data, &resp)
		if resp.OK || resp.Error == nil || resp.Error.Code != protocol.ErrAuthInvalidCredential {
			t.Fatalf("want AUTH_INVALID_CREDENTIAL, got %+v", resp)
		}
	})

	t.Run("register_and_heartbeat", func(t *testing.T) {
		c := dial(t, ctx, base)
		defer c.Close(websocket.StatusNormalClosure, "")
		mustAuth(t, ctx, c, cred.Token, "demo.svc", "inst-reg-1")
		cron := "0 */5 * * * *"
		ack := mustRegister(t, ctx, c, "demo.svc", "inst-reg-1", "1.0.0", "abc1234", []protocol.TaskDecl{{
			TaskCode: "demo.hello", TaskName: "Hello", HandlerVersion: "1.0.0", DefaultCron: &cron, TimeoutSeconds: 30, MaxConcurrency: 2,
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}})
		if !ack.OK || len(ack.AcceptedTasks) != 1 {
			t.Fatalf("register ack %+v", ack)
		}
		hb := mustHeartbeat(t, ctx, c, "inst-reg-1", 0)
		if hb.InstanceStatus != "ONLINE" {
			t.Fatalf("status=%s", hb.InstanceStatus)
		}
		// version queryable via signed admin API
		code, raw := signedJSON(t, http.MethodGet, "http://"+cfg.HTTP.AdminAddr+"/v1/admin/instances?service_id="+itoa(svc.ID),
			nil, "", "dev-admin-secret-16b", []string{"job.service.view"}, false)
		if code != 200 {
			t.Fatalf("instances %d %s", code, raw)
		}
		var body struct {
			Data struct {
				List []struct {
					InstanceID string  `json:"instance_id"`
					AppVersion *string `json:"app_version"`
					GitCommit  *string `json:"git_commit"`
					Status     string  `json:"status"`
				} `json:"list"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(raw), &body); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, it := range body.Data.List {
			if it.InstanceID == "inst-reg-1" {
				found = true
				if it.AppVersion == nil || *it.AppVersion != "1.0.0" || it.GitCommit == nil || *it.GitCommit != "abc1234" || it.Status != "ONLINE" {
					t.Fatalf("instance %+v", it)
				}
			}
		}
		if !found {
			t.Fatal("instance not listed")
		}
	})

	t.Run("reregister_preserves_schedule", func(t *testing.T) {
		c := dial(t, ctx, base)
		defer c.Close(websocket.StatusNormalClosure, "")
		mustAuth(t, ctx, c, cred.Token, "demo.svc", "inst-sched")
		cron1 := "0 */5 * * * *"
		ack1 := mustRegister(t, ctx, c, "demo.svc", "inst-sched", "1.0.0", "c1", []protocol.TaskDecl{{
			TaskCode: "demo.ping", TaskName: "Ping", HandlerVersion: "1.0.0", DefaultCron: &cron1, TimeoutSeconds: 30, MaxConcurrency: 1,
		}})
		if !ack1.OK {
			t.Fatal(ack1)
		}
		taskID := ack1.AcceptedTasks[0].TaskID
		// admin-like update: enable schedule and change cron
		_, err := db.ExecContext(ctx, `UPDATE job_schedule SET cron_expression=?, enabled=1, config_source='ADMIN', version=version+1 WHERE task_id=?`, "0 */10 * * * *", taskID)
		if err != nil {
			t.Fatal(err)
		}
		cron2 := "0 * * * * *"
		ack2 := mustRegister(t, ctx, c, "demo.svc", "inst-sched", "1.1.0", "c2", []protocol.TaskDecl{{
			TaskCode: "demo.ping", TaskName: "Ping", HandlerVersion: "1.1.0", DefaultCron: &cron2, TimeoutSeconds: 30, MaxConcurrency: 1,
		}})
		if !ack2.OK {
			t.Fatal(ack2)
		}
		var cron string
		var enabled int
		var src string
		if err := db.QueryRowContext(ctx, `SELECT cron_expression, enabled, config_source FROM job_schedule WHERE task_id=?`, taskID).Scan(&cron, &enabled, &src); err != nil {
			t.Fatal(err)
		}
		if cron != "0 */10 * * * *" || enabled != 1 || src != "ADMIN" {
			t.Fatalf("schedule overwritten: cron=%s enabled=%d src=%s", cron, enabled, src)
		}
	})

	t.Run("multi_instance", func(t *testing.T) {
		c1 := dial(t, ctx, base)
		defer c1.Close(websocket.StatusNormalClosure, "")
		c2 := dial(t, ctx, base)
		defer c2.Close(websocket.StatusNormalClosure, "")
		mustAuth(t, ctx, c1, cred.Token, "demo.svc", "inst-multi-a")
		mustAuth(t, ctx, c2, cred.Token, "demo.svc", "inst-multi-b")
		mustRegister(t, ctx, c1, "demo.svc", "inst-multi-a", "2.0.0", "gA", nil)
		mustRegister(t, ctx, c2, "demo.svc", "inst-multi-b", "2.0.1", "gB", nil)
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_instance WHERE service_id=? AND status='ONLINE' AND instance_id IN ('inst-multi-a','inst-multi-b')`, svc.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Fatalf("online=%d", n)
		}
	})

	t.Run("session_replace", func(t *testing.T) {
		c1 := dial(t, ctx, base)
		mustAuth(t, ctx, c1, cred.Token, "demo.svc", "inst-replace")
		mustRegister(t, ctx, c1, "demo.svc", "inst-replace", "1.0.0", "old", nil)

		c2 := dial(t, ctx, base)
		defer c2.Close(websocket.StatusNormalClosure, "")
		resp := mustAuth(t, ctx, c2, cred.Token, "demo.svc", "inst-replace")
		if resp.SessionEpoch < 2 {
			t.Fatalf("epoch=%d", resp.SessionEpoch)
		}
		// old connection should get notice / close
		ctx2, cancel2 := context.WithTimeout(ctx, 2*time.Second)
		defer cancel2()
		_, _, err := c1.Read(ctx2)
		if err == nil {
			// may have read notice first
			_, _, err = c1.Read(ctx2)
		}
		if err == nil {
			t.Fatal("expected old connection to close")
		}
		code := websocket.CloseStatus(err)
		if code != protocol.CloseSessionReplaced && code != websocket.StatusNormalClosure && code != -1 {
			// -1 if abrupt; accept replaced
			t.Logf("close status=%v err=%v", code, err)
		}
	})

	t.Run("suspect_offline", func(t *testing.T) {
		c := dial(t, ctx, base)
		mustAuth(t, ctx, c, cred.Token, "demo.svc", "inst-offline")
		mustRegister(t, ctx, c, "demo.svc", "inst-offline", "1.0.0", "z", nil)
		// drop connection without unregister
		_ = c.Close(websocket.StatusGoingAway, "bye")
		deadline := time.Now().Add(8 * time.Second)
		var status string
		for time.Now().Before(deadline) {
			_ = db.QueryRowContext(ctx, `SELECT status FROM job_instance WHERE instance_id='inst-offline'`).Scan(&status)
			if status == "OFFLINE" || status == "SUSPECT" {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if status != "SUSPECT" && status != "OFFLINE" {
			t.Fatalf("status=%s want SUSPECT/OFFLINE", status)
		}
		deadline = time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			_ = db.QueryRowContext(ctx, `SELECT status FROM job_instance WHERE instance_id='inst-offline'`).Scan(&status)
			if status == "OFFLINE" {
				return
			}
			time.Sleep(300 * time.Millisecond)
		}
		t.Fatalf("never OFFLINE, status=%s", status)
	})
}

func cleanup(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, q := range []string{
		"DELETE FROM job_instance_task", "DELETE FROM job_execution_log", "DELETE FROM job_execution_attempt", "DELETE FROM job_execution_event",
		"DELETE FROM job_execution", "DELETE FROM job_task_state WHERE task_id NOT IN (SELECT id FROM job_definition WHERE service_id IN (SELECT id FROM job_service WHERE service_code IN ('sdk.demo','sdk.demo.php')))", "DELETE FROM job_schedule WHERE task_id NOT IN (SELECT id FROM job_definition WHERE service_id IN (SELECT id FROM job_service WHERE service_code IN ('sdk.demo','sdk.demo.php')))", "DELETE FROM job_definition_version WHERE definition_id NOT IN (SELECT id FROM job_definition WHERE service_id IN (SELECT id FROM job_service WHERE service_code IN ('sdk.demo','sdk.demo.php')))",
		"DELETE FROM job_definition WHERE service_id NOT IN (SELECT id FROM job_service WHERE service_code IN ('sdk.demo','sdk.demo.php'))", "DELETE FROM job_instance", "DELETE FROM job_service_credential WHERE service_id NOT IN (SELECT id FROM job_service WHERE service_code IN ('sdk.demo','sdk.demo.php'))",
		"DELETE FROM job_deployment", "DELETE FROM job_archive_run",
		"DELETE FROM job_service WHERE service_code IN ('demo.svc','p5.svc','p7.svc','p8.svc','state.svc')", "DELETE FROM job_operation_audit",
	} {
		if _, err := db.Exec(q); err != nil {
			// some tables may not exist yet in partial migrate — ignore
			t.Log(q, err)
		}
	}
}

func dial(t *testing.T, ctx context.Context, url string) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: []string{protocol.Subprotocol}})
	if err != nil {
		t.Fatal(err)
	}
	c.SetReadLimit(1 << 20)
	return c
}

func send(t *testing.T, ctx context.Context, c *websocket.Conn, typ string, data any) string {
	t.Helper()
	b, id, err := protocol.NewMessage(typ, "", "", data)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
	return id
}

func readEnv(t *testing.T, ctx context.Context, c *websocket.Conn) protocol.Envelope {
	t.Helper()
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var env protocol.Envelope
	if err := wsjson.Read(rctx, c, &env); err != nil {
		t.Fatal(err)
	}
	return env
}

func mustAuth(t *testing.T, ctx context.Context, c *websocket.Conn, token, service, instance string) protocol.AuthResponse {
	t.Helper()
	send(t, ctx, c, protocol.TypeAuthRequest, protocol.AuthRequest{
		ServiceCode: service, Credential: token, InstanceID: instance,
		ProtocolVersions: []string{"1.0"}, SDK: protocol.SDKInfo{Language: "go", Version: "0.1.0"},
	})
	env := readEnv(t, ctx, c)
	if env.Type != protocol.TypeAuthResponse {
		t.Fatalf("got %s", env.Type)
	}
	var resp protocol.AuthResponse
	if err := json.Unmarshal(env.Data, &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("auth failed: %+v", resp)
	}
	return resp
}

func mustRegister(t *testing.T, ctx context.Context, c *websocket.Conn, service, instance, ver, commit string, tasks []protocol.TaskDecl) *protocol.RegisterAck {
	t.Helper()
	if tasks == nil {
		tasks = []protocol.TaskDecl{}
	}
	send(t, ctx, c, protocol.TypeServiceRegister, protocol.RegisterData{
		ServiceCode: service, ServiceName: "Demo", InstanceID: instance, Environment: "dev",
		Runtime: protocol.RuntimeInfo{Language: "go", Version: "1.22"},
		Deployment: protocol.DeploymentInfo{Version: ver, GitCommit: commit},
		Capacity: struct {
			MaxConcurrency int `json:"max_concurrency"`
		}{MaxConcurrency: 4},
		Tasks: tasks, StartedAt: protocol.FormatTime(time.Now()),
		Host: &protocol.HostInfo{Hostname: "box", IP: "127.0.0.1", PID: 1},
	})
	env := readEnv(t, ctx, c)
	if env.Type != protocol.TypeServiceRegisterAck {
		t.Fatalf("got %s data=%s", env.Type, string(env.Data))
	}
	var ack protocol.RegisterAck
	if err := json.Unmarshal(env.Data, &ack); err != nil {
		t.Fatal(err)
	}
	return &ack
}

func mustHeartbeat(t *testing.T, ctx context.Context, c *websocket.Conn, instance string, running int) *protocol.HeartbeatAck {
	t.Helper()
	send(t, ctx, c, protocol.TypeServiceHeartbeat, protocol.Heartbeat{
		InstanceID: instance, Status: "ONLINE", Running: running,
	})
	env := readEnv(t, ctx, c)
	if env.Type != protocol.TypeServiceHeartbeatAck {
		t.Fatalf("got %s", env.Type)
	}
	var ack protocol.HeartbeatAck
	if err := json.Unmarshal(env.Data, &ack); err != nil {
		t.Fatal(err)
	}
	return &ack
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }
