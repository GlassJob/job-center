package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/GlassJob/job-center/internal/application/catalog"
	appexec "github.com/GlassJob/job-center/internal/application/execution"
	"github.com/GlassJob/job-center/internal/application/scheduler"
	"github.com/GlassJob/job-center/internal/application/monitoring"
	"github.com/GlassJob/job-center/internal/application/registration"
	"github.com/GlassJob/job-center/internal/config"
	"github.com/GlassJob/job-center/internal/domain/schedule"
	"github.com/GlassJob/job-center/internal/infrastructure/database"
	"github.com/GlassJob/job-center/internal/infrastructure/logger"
	"github.com/GlassJob/job-center/internal/protocol"
	repo "github.com/GlassJob/job-center/internal/repository/mysql"
	httpapi "github.com/GlassJob/job-center/internal/transport/http"
	wstransport "github.com/GlassJob/job-center/internal/transport/websocket"
	"github.com/GlassJob/job-center/migrations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "job-center: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", "", "path to YAML config")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	log := logger.New(cfg.Log.Level, cfg.Log.Format, os.Stdout).With("node_id", cfg.NodeID)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if _, err := database.MigrateUp(ctx, cfg.Database.DSN, migrations.FS, log); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	db, err := database.Open(ctx, cfg.Database.DSN, database.PoolOptions{
		MaxOpenConns: cfg.Database.MaxOpenConns, MaxIdleConns: cfg.Database.MaxIdleConns, ConnMaxLifetime: cfg.Database.ConnMaxLifetime.D(),
	})
	if err != nil {
		return err
	}
	defer db.Close()

	store := repo.New(db)
	val, err := protocol.DefaultValidator()
	if err != nil {
		return fmt.Errorf("protocol schemas: %w", err)
	}
	reg := registration.New(store, cfg.Security.CredentialPepper, cfg.NodeID, 60*time.Second, log)
	loc, tzName, src, err := schedule.ResolveDefaultTimezone(cfg.Scheduler.DefaultTimezone, os.Getenv, "")
	if err != nil {
		return err
	}
	log.Info("default schedule timezone", "timezone", tzName, "source", src)
	cat := catalog.New(store, cfg.Security.CredentialPepper, loc, tzName)
	mon := monitoring.New(store, monitoring.Options{
		SuspectAfter: cfg.Instance.SuspectAfter.D(), OfflineAfter: cfg.Instance.OfflineAfter.D(),
		SweepInterval: cfg.Instance.SweepInterval.D(), NodeID: cfg.NodeID,
	}, log)
	if _, err := mon.RecoverOnStartup(ctx); err != nil {
		return fmt.Errorf("recover: %w", err)
	}
	go mon.Run(ctx)

	ws := wstransport.NewServer(reg, cfg, val, log)
	api := httpapi.New(db, store, cat, cfg, loc, tzName)
	leaseTTL := cfg.WebSocket.LeaseTTL.D()
	if leaseTTL <= 0 {
		leaseTTL = 60 * time.Second
	}
	execSvc := appexec.New(store, ws.Hub(), leaseTTL, log)
	ws.SetExecution(execSvc)
	api.SetExecution(execSvc)

	startedAt := time.Now().UTC()
	sch := scheduler.New(store, scheduler.Options{
		NodeID: cfg.NodeID, TickInterval: cfg.Scheduler.TickInterval.D(), LeaseTTL: cfg.Scheduler.LeaseTTL.D(),
		DefaultLocation: loc, BatchSize: 200,
	}, log)
	go sch.Run(ctx)
	rt := appexec.NewRuntime(execSvc, appexec.RuntimeOptions{
		NodeID: cfg.NodeID, SweepInterval: 5 * time.Second, LeaseTTL: leaseTTL,
		LostGrace: cfg.Scheduler.LostGrace.D(), TimeoutGrace: cfg.Scheduler.TimeoutGrace.D(),
		DispatchInterval: time.Second, StartedAt: startedAt,
	})
	go rt.Run(ctx)

	execMux := http.NewServeMux()
	api.MountExecutor(execMux)
	ws.Mount(execMux)
	execSrv := &http.Server{Addr: cfg.HTTP.ExecutorAddr, Handler: execMux}

	adminMux := http.NewServeMux()
	api.MountAdmin(adminMux)
	adminSrv := &http.Server{Addr: cfg.HTTP.AdminAddr, Handler: adminMux}

	errCh := make(chan error, 2)
	go func() {
		log.Info("executor http listening", "addr", cfg.HTTP.ExecutorAddr)
		if err := execSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	go func() {
		log.Info("admin http listening", "addr", cfg.HTTP.AdminAddr)
		if err := adminSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout.D())
	defer cancel()
	_ = execSrv.Shutdown(shutdownCtx)
	_ = adminSrv.Shutdown(shutdownCtx)
	log.Info("shutdown complete")
	return nil
}
