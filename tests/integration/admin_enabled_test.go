package integration_test

import (
	"bytes"
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
)

func TestAdminEnabledAndSchedule(t *testing.T) {
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
	cfg.HTTP.ExecutorAddr = "127.0.0.1:28090"
	cfg.HTTP.AdminAddr = "127.0.0.1:28091"
	cfg.NodeID = "p2-admin-test"
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
	val, _ := protocol.DefaultValidator()
	reg := registration.New(store, cfg.Security.CredentialPepper, cfg.NodeID, time.Minute, log)
	loc, tz, _, _ := schedule.ResolveDefaultTimezone("", os.Getenv, "")
	cat := catalog.New(store, cfg.Security.CredentialPepper, loc, tz)
	mon := monitoring.New(store, monitoring.Options{SuspectAfter: time.Minute, OfflineAfter: 2 * time.Minute, SweepInterval: time.Minute, NodeID: cfg.NodeID}, log)
	_ = mon
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
	// register a task so schedule exists
	c, _, err := websocket.Dial(ctx, "ws://"+cfg.HTTP.ExecutorAddr+"/v1/executor/ws", &websocket.DialOptions{Subprotocols: []string{protocol.Subprotocol}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")
	mustAuth(t, ctx, c, cred.Token, "demo.svc", "inst-admin1")
	cron := "0 */5 * * * *"
	ack := mustRegister(t, ctx, c, "demo.svc", "inst-admin1", "1.0.0", "abcd1234", []protocol.TaskDecl{{
		TaskCode: "demo.hello", TaskName: "Hello", HandlerVersion: "1.0.0", DefaultCron: &cron, TimeoutSeconds: 30, MaxConcurrency: 1,
	}})
	if !ack.OK || len(ack.AcceptedTasks) != 1 {
		t.Fatalf("%+v", ack)
	}
	taskID := ack.AcceptedTasks[0].TaskID
	sch, err := store.Q().GetSchedule(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + cfg.HTTP.AdminAddr
	secret := "dev-admin-secret-16b"

	t.Run("bad_signature_401", func(t *testing.T) {
		code, body := signedJSON(t, http.MethodPut, base+fmt.Sprintf("/v1/admin/tasks/%d/enabled", taskID),
			map[string]string{"If-Match": strconv.FormatInt(sch.Version, 10)},
			`{"enabled":true}`, secret, []string{"job.task.update"}, true)
		if code != 401 {
			t.Fatalf("code=%d body=%s", code, body)
		}
		var env map[string]any
		_ = json.Unmarshal([]byte(body), &env)
		if env["code"] != "UNAUTHENTICATED" {
			t.Fatalf("%v", env)
		}
	})

	t.Run("perm_denied_403", func(t *testing.T) {
		code, body := signedJSON(t, http.MethodPut, base+fmt.Sprintf("/v1/admin/tasks/%d/enabled", taskID),
			map[string]string{"If-Match": strconv.FormatInt(sch.Version, 10)},
			`{"enabled":true}`, secret, []string{"job.task.view"}, false)
		if code != 403 {
			t.Fatalf("code=%d body=%s", code, body)
		}
	})

	t.Run("enabled_ok", func(t *testing.T) {
		code, body := signedJSON(t, http.MethodPut, base+fmt.Sprintf("/v1/admin/tasks/%d/enabled", taskID),
			map[string]string{"If-Match": strconv.FormatInt(sch.Version, 10)},
			`{"enabled":true}`, secret, []string{"job.task.update"}, false)
		if code != 200 {
			t.Fatalf("code=%d body=%s", code, body)
		}
		var env struct {
			Code string         `json:"code"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(body), &env); err != nil {
			t.Fatal(err)
		}
		if env.Code != "OK" || env.Data["enabled"] != true {
			t.Fatalf("%s", body)
		}
		sch.Version = int64(env.Data["version"].(float64))
	})

	t.Run("schedule_rejects_enabled_field", func(t *testing.T) {
		code, body := signedJSON(t, http.MethodPut, base+fmt.Sprintf("/v1/admin/tasks/%d/schedule", taskID),
			map[string]string{"If-Match": strconv.FormatInt(sch.Version, 10)},
			`{"enabled":false,"timeout_seconds":60}`, secret, []string{"job.schedule.update"}, false)
		if code != 400 {
			t.Fatalf("code=%d body=%s", code, body)
		}
	})

	t.Run("schedule_ok", func(t *testing.T) {
		code, body := signedJSON(t, http.MethodPut, base+fmt.Sprintf("/v1/admin/tasks/%d/schedule", taskID),
			map[string]string{"If-Match": strconv.FormatInt(sch.Version, 10)},
			`{"timeout_seconds":90,"manual_enabled":true}`, secret, []string{"job.schedule.update"}, false)
		if code != 200 {
			t.Fatalf("code=%d body=%s", code, body)
		}
		var env struct {
			Code string         `json:"code"`
			Data map[string]any `json:"data"`
		}
		_ = json.Unmarshal([]byte(body), &env)
		if env.Data["timeout_seconds"].(float64) != 90 || env.Data["enabled"] != true {
			t.Fatalf("schedule should not clear enabled: %s", body)
		}
	})
}

func signedJSON(t *testing.T, method, urlStr string, extraHdr map[string]string, body, secret string, perms []string, badSig bool) (int, string) {
	t.Helper()
	op := security.EncodeOperator(security.Operator{ID: "1", Name: "alice", Perms: perms, ClientIP: "127.0.0.1", RequestID: "req-1"})
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := fmt.Sprintf("%x", time.Now().UnixNano())
	u, _ := http.NewRequest(method, urlStr, nil)
	canon, err := security.Canonical(method, u.URL.Path, u.URL.RawQuery, []byte(body), ts, nonce, op)
	if err != nil {
		t.Fatal(err)
	}
	sig := security.Sign(secret, canon)
	if badSig {
		sig = "deadbeef"
	}
	req, err := http.NewRequest(method, urlStr, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(security.HeaderClient, "glassadmin")
	req.Header.Set(security.HeaderTimestamp, ts)
	req.Header.Set(security.HeaderNonce, nonce)
	req.Header.Set(security.HeaderOperator, op)
	req.Header.Set(security.HeaderSignature, sig)
	for k, v := range extraHdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
