package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/GlassJob/job-center/internal/bootstrap"
	"github.com/GlassJob/job-center/internal/config"
	"github.com/GlassJob/job-center/internal/handler"
	"github.com/GlassJob/job-center/internal/pkg/database"
	applog "github.com/GlassJob/job-center/internal/pkg/log"
	"github.com/GlassJob/job-center/internal/repository/mysql"
	"github.com/GlassJob/job-center/internal/scheduler"
	"github.com/GlassJob/job-center/migrations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "job-center: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", "", "path to config file (yaml)")
	flag.Parse()
	if *cfgPath == "" {
		if _, err := os.Stat("configs/local.yaml"); err == nil {
			*cfgPath = "configs/local.yaml"
		}
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	log := applog.New(cfg.Log.Level, cfg.Log.Format).With("node_id", cfg.NodeID)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := database.MigrateUp(cfg.Database.DSN, migrations.FS); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	db, err := database.Open(ctx, cfg.Database.DSN, database.PoolOptions{
		MaxOpenConns:    cfg.Database.MaxOpenConns,
		MaxIdleConns:    cfg.Database.MaxIdleConns,
		ConnMaxLifetime: cfg.Database.ConnMaxLifetimeDuration(),
	})
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	store := mysql.New(db)
	if _, err := bootstrap.EnsureAdminClient(ctx, store, cfg.Security.CredentialPepper, cfg.Security.BootstrapClientSecret, cfg.Security.BootstrapClientName, log); err != nil {
		return fmt.Errorf("bootstrap client: %w", err)
	}

	app := handler.NewRouter(handler.Deps{
		Store:     store,
		Pepper:    cfg.Security.CredentialPepper,
		Skew:      cfg.Security.TimestampSkew(),
		NodeID:    cfg.NodeID,
		WebSocket: cfg.WebSocket,
		Log:       log,
	})
	sch := scheduler.New(store, app.Executions, cfg.Scheduler, cfg.NodeID, log)
	app.Schedules.SetHorizonGenerator(sch)
	if app.SchedulerHandler != nil {
		app.SchedulerHandler.Scheduler = sch
	}
	go sch.Start(ctx)

	srv := &http.Server{Addr: cfg.HTTP.Addr, Handler: app.Engine}

	errCh := make(chan error, 1)
	go func() {
		log.Info("http listen", "addr", cfg.HTTP.Addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}
