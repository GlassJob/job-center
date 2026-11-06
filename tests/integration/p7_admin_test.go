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
	"strings"
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
)

func TestP7OverviewSSEDeployVersions(t *testing.T) {
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
	cfg.HTTP.ExecutorAddr = "127.0.0.1:28280"
	cfg.HTTP.AdminAddr = "127.0.0.1:28281"
	cfg.NodeID = "p7-test"
	if cfg.Security.AdminClients == nil {
		cfg.Security.AdminClients = map[string]config.AdminClient{}
	}
	cfg.Security.AdminClients["glassadmin"] = config.AdminClient{Secret: "dev-admin-secret-16b", MaxPerms: []string{"*"}}
	cfg.Security.AdminClients["deployer"] = config.AdminClient{Secret: "dev-deployer-secret16", MaxPerms: []string{"*"}}

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
	watcher := monitoring.NewWatcher(store, monitoring.AlertOptions{Interval: time.Minute}, log)
	_, _ = watcher.Evaluate(ctx)

	ws := wstransport.NewServer(reg, cfg, val, log)
	api := httpapi.New(db, store, cat, cfg, loc, tz)
	api.SetWatcher(watcher)
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
	svc, err := cat.CreateService(ctx, actor, catalog.CreateServiceInput{ServiceCode: "p7.svc", ServiceName: "P7"})
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + cfg.HTTP.AdminAddr
	secret := "dev-admin-secret-16b"

	t.Run("overview", func(t *testing.T) {
		code, body := signedJSON(t, http.MethodGet, base+"/v1/admin/overview", nil, "", secret, []string{"job.service.view"}, false)
		if code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		var env struct {
			Code string         `json:"code"`
			Data map[string]any `json:"data"`
		}
		_ = json.Unmarshal([]byte(body), &env)
		if env.Code != "OK" || env.Data["service_count"] == nil || env.Data["today_timezone"] == nil {
			t.Fatalf("overview: %s", body)
		}
	})

	t.Run("register_deployment_api", func(t *testing.T) {
		body := fmt.Sprintf(`{"service_code":"p7.svc","environment":"dev","deployment_version":"1.0.0","git_commit":"abcdef1234567","build_id":"b-p7-1","deployment_status":"SUCCEEDED","set_expected":true}`)
		code, resp := clientSignedJSON(t, http.MethodPost, base+"/v1/deployments", body, "deployer", "dev-deployer-secret16")
		if code != 200 {
			t.Fatalf("%d %s", code, resp)
		}
		var env struct {
			Data map[string]any `json:"data"`
		}
		_ = json.Unmarshal([]byte(resp), &env)
		if env.Data["is_expected"] != true || env.Data["source"] != "API" {
			t.Fatalf("%s", resp)
		}
	})

	t.Run("manual_deployment", func(t *testing.T) {
		body := `{"deployment_version":"1.0.1","git_commit":"bbbbbbb1111111","set_expected":true}`
		code, resp := signedJSON(t, http.MethodPost, base+fmt.Sprintf("/v1/admin/services/%d/deployments", svc.ID), nil, body, secret, []string{"job.deployment.manage"}, false)
		if code != 200 {
			t.Fatalf("%d %s", code, resp)
		}
	})

	t.Run("versions", func(t *testing.T) {
		code, resp := signedJSON(t, http.MethodGet, base+"/v1/admin/versions?environment=dev&service_id="+strconv.FormatInt(svc.ID, 10), nil, "", secret, []string{"job.service.view"}, false)
		if code != 200 {
			t.Fatalf("%d %s", code, resp)
		}
		var env struct {
			Data []map[string]any `json:"data"`
		}
		_ = json.Unmarshal([]byte(resp), &env)
		if len(env.Data) != 1 || env.Data[0]["state"] != "NO_ONLINE_INSTANCE" {
			t.Fatalf("%s", resp)
		}
	})

	t.Run("sse_ticket_and_stream", func(t *testing.T) {
		execID := "01P7SSETEST000000000000000"
		_, err := db.ExecContext(ctx, `INSERT INTO job_definition(service_id, task_code, task_name, handler_version, input_schema, schema_hash)
			VALUES (?,?,?,?,?,?)`, svc.ID, "p7.task", "P7 Task", "1.0.0", `{"type":"object"}`, "deadbeef")
		if err != nil {
			t.Fatal(err)
		}
		var taskID int64
		if err := db.QueryRowContext(ctx, `SELECT id FROM job_definition WHERE service_id=? AND task_code='p7.task'`, svc.ID).Scan(&taskID); err != nil {
			t.Fatal(err)
		}
		_, err = db.ExecContext(ctx, `INSERT INTO job_execution(execution_id, task_id, service_id, task_code, trigger_type, triggered_by, params, status, handler_version, handler_major, schema_hash, timeout_seconds, retry_policy, max_attempts, attempt_count)
			VALUES (?,?,?,?,'MANUAL','admin:t','{}','SUCCEEDED','1.0.0',1,'deadbeef',60,'{}',1,0)`,
			execID, taskID, svc.ID, "p7.task")
		if err != nil {
			t.Fatal(err)
		}
		code, resp := signedJSON(t, http.MethodPost, base+"/v1/admin/sse-tickets", nil, fmt.Sprintf(`{"execution_id":%q}`, execID), secret, []string{"job.execution.log.view"}, false)
		if code != 201 {
			t.Fatalf("ticket %d %s", code, resp)
		}
		var env struct {
			Data struct {
				Ticket string `json:"ticket"`
				URL    string `json:"url"`
			} `json:"data"`
		}
		_ = json.Unmarshal([]byte(resp), &env)
		if env.Data.Ticket == "" || !strings.Contains(env.Data.URL, "ticket=") {
			t.Fatalf("%s", resp)
		}
		req, _ := http.NewRequest(http.MethodGet, base+fmt.Sprintf("/v1/admin/executions/%s/events?ticket=%s", execID, env.Data.Ticket), nil)
		cli := &http.Client{Timeout: 5 * time.Second}
		resp2, err := cli.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != 200 {
			b, _ := io.ReadAll(resp2.Body)
			t.Fatalf("sse %d %s", resp2.StatusCode, b)
		}
		buf := make([]byte, 8192)
		n, _ := resp2.Body.Read(buf)
		got := string(buf[:n])
		if !strings.Contains(got, "event: end") && !strings.Contains(got, "event: state") && !strings.Contains(got, ": ping") {
			t.Fatalf("unexpected sse body: %q", got)
		}
	})

	t.Run("cron_preview", func(t *testing.T) {
		body := `{"cron_expression":"0 */5 * * * *","timezone":"","count":3}`
		code, resp := signedJSON(t, http.MethodPost, base+"/v1/admin/cron/preview", nil, body, secret, []string{"job.task.view"}, false)
		if code != 200 {
			t.Fatalf("%d %s", code, resp)
		}
		var env struct {
			Data struct {
				Valid bool     `json:"valid"`
				Next  []string `json:"next_fire_times"`
			} `json:"data"`
		}
		_ = json.Unmarshal([]byte(resp), &env)
		if !env.Data.Valid || len(env.Data.Next) != 3 {
			t.Fatalf("%s", resp)
		}
	})

	t.Run("timezone_empty_as_null", func(t *testing.T) {
		// create schedule via definition already inserted in sse test may not exist if order differs — ensure task
		var taskID int64
		err := db.QueryRowContext(ctx, `SELECT id FROM job_definition WHERE service_id=? LIMIT 1`, svc.ID).Scan(&taskID)
		if err != nil {
			_, err = db.ExecContext(ctx, `INSERT INTO job_definition(service_id, task_code, task_name, handler_version, input_schema, schema_hash)
				VALUES (?,?,?,?,?,?)`, svc.ID, "p7.tz", "TZ", "1.0.0", `{"type":"object"}`, "beef")
			if err != nil {
				t.Fatal(err)
			}
			_ = db.QueryRowContext(ctx, `SELECT id FROM job_definition WHERE service_id=? AND task_code='p7.tz'`, svc.ID).Scan(&taskID)
		}
		_, _ = db.ExecContext(ctx, `INSERT IGNORE INTO job_schedule(task_id, schedule_type, cron_expression, timezone, enabled, manual_enabled, timeout_seconds, concurrency_policy, concurrency_scope, max_concurrency, queue_max, retry_policy, misfire_policy, misfire_threshold_seconds, catch_up_max, dispatch_strategy, version)
			VALUES (?,'CRON','0 * * * * *','Asia/Shanghai',0,1,60,'SKIP','TASK',1,0,'{}','SKIP',60,10,'LEASED',1)`, taskID)
		var ver int64
		_ = db.QueryRowContext(ctx, `SELECT version FROM job_schedule WHERE task_id=?`, taskID).Scan(&ver)
		body := `{"timezone":""}`
		code, resp := signedJSON(t, http.MethodPut, base+fmt.Sprintf("/v1/admin/tasks/%d/schedule", taskID),
			map[string]string{"If-Match": strconv.FormatInt(ver, 10)}, body, secret, []string{"job.schedule.update"}, false)
		if code != 200 {
			t.Fatalf("%d %s", code, resp)
		}
		var env struct {
			Data map[string]any `json:"data"`
		}
		_ = json.Unmarshal([]byte(resp), &env)
		if env.Data["timezone"] != nil {
			t.Fatalf("expected null timezone, got %v body=%s", env.Data["timezone"], resp)
		}
	})

	t.Run("alerts", func(t *testing.T) {
		code, resp := signedJSON(t, http.MethodGet, base+"/v1/admin/alerts", nil, "", secret, []string{"job.service.view"}, false)
		if code != 200 {
			t.Fatalf("%d %s", code, resp)
		}
	})
}

func clientSignedJSON(t *testing.T, method, urlStr, body, client, secret string) (int, string) {
	t.Helper()
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := fmt.Sprintf("%x", time.Now().UnixNano())
	u, _ := http.NewRequest(method, urlStr, nil)
	canon, err := security.Canonical(method, u.URL.Path, u.URL.RawQuery, []byte(body), ts, nonce, "")
	if err != nil {
		t.Fatal(err)
	}
	sig := security.Sign(secret, canon)
	req, err := http.NewRequest(method, urlStr, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(security.HeaderClient, client)
	req.Header.Set(security.HeaderTimestamp, ts)
	req.Header.Set(security.HeaderNonce, nonce)
	req.Header.Set(security.HeaderSignature, sig)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
