// Package monitoring 实例在线状态检测：ONLINE→SUSPECT→OFFLINE（全部基于数据库 NOW(3)）。
package monitoring

import (
	"context"
	"log/slog"
	"time"

	repo "github.com/GlassJob/job-center/internal/repository/mysql"
)

type Options struct {
	SuspectAfter  time.Duration
	OfflineAfter  time.Duration
	SweepInterval time.Duration
	NodeID        string
}

type Monitor struct {
	store *repo.Store
	opt   Options
	log   *slog.Logger
}

func New(store *repo.Store, opt Options, log *slog.Logger) *Monitor {
	return &Monitor{store: store, opt: opt, log: log}
}

// RecoverOnStartup 本节点上次运行遗留的在线实例：ONLINE→SUSPECT，DRAINING/REGISTERING→OFFLINE。
func (m *Monitor) RecoverOnStartup(ctx context.Context) (int64, error) {
	n, err := m.store.Q().RecoverNode(ctx, m.opt.NodeID)
	if err == nil && n > 0 {
		m.log.Warn("recovered stale instances from previous run", "node_id", m.opt.NodeID, "count", n)
	}
	return n, err
}

// Sweep 执行一次扫描。语句幂等，多节点同时执行也安全。
func (m *Monitor) Sweep(ctx context.Context) (suspect, offline int64, err error) {
	q := m.store.Q()
	if suspect, err = q.SweepSuspect(ctx, m.opt.SuspectAfter); err != nil {
		return
	}
	offline, err = q.SweepOffline(ctx, m.opt.OfflineAfter)
	if suspect > 0 || offline > 0 {
		m.log.Info("instance sweep", "to_suspect", suspect, "to_offline", offline)
	}
	return
}

// Run 周期扫描直到 ctx 取消。
func (m *Monitor) Run(ctx context.Context) {
	t := time.NewTicker(m.opt.SweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, _, err := m.Sweep(ctx); err != nil && ctx.Err() == nil {
				m.log.Error("instance sweep failed", "err", err)
			}
		}
	}
}
