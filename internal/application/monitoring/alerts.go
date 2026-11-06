package monitoring

import (
	"context"
	"log/slog"
	"sync"
	"time"

	repo "github.com/GlassJob/job-center/internal/repository/mysql"
)

// AlertOptions 实例离线 / 任务逾期监控。
type AlertOptions struct {
	Interval        time.Duration // 评估周期
	OfflineWindow   time.Duration // 近多久内转 OFFLINE 的实例计入告警
	OverdueGrace    time.Duration // 调度/派发逾期宽限
}

// Watcher 周期评估告警，新出现的告警写 WARN 日志；最新快照供 Admin API 读取。
type Watcher struct {
	store *repo.Store
	opt   AlertOptions
	log   *slog.Logger

	mu       sync.RWMutex
	snapshot []*repo.Alert
	at       time.Time
	seen     map[string]bool
}

func NewWatcher(store *repo.Store, opt AlertOptions, log *slog.Logger) *Watcher {
	if opt.Interval <= 0 {
		opt.Interval = 30 * time.Second
	}
	if opt.OfflineWindow <= 0 {
		opt.OfflineWindow = 24 * time.Hour
	}
	if opt.OverdueGrace <= 0 {
		opt.OverdueGrace = 5 * time.Minute
	}
	return &Watcher{store: store, opt: opt, log: log, seen: map[string]bool{}}
}

func key(a *repo.Alert) string {
	return a.Type + "|" + a.InstanceID + "|" + a.ExecutionID + "|" + a.ServiceCode + "|" + a.TaskCode
}

// Evaluate 立即评估一次并返回告警。
func (w *Watcher) Evaluate(ctx context.Context) ([]*repo.Alert, error) {
	q := w.store.Q()
	inst, err := q.InstanceAlerts(ctx, w.opt.OfflineWindow)
	if err != nil {
		return nil, err
	}
	od, err := q.OverdueAlerts(ctx, w.opt.OverdueGrace)
	if err != nil {
		return nil, err
	}
	all := append(inst, od...)
	if all == nil {
		all = []*repo.Alert{}
	}
	w.mu.Lock()
	next := map[string]bool{}
	for _, a := range all {
		k := key(a)
		next[k] = true
		if !w.seen[k] && w.log != nil {
			w.log.Warn("monitor alert", "type", a.Type, "service", a.ServiceCode, "task", a.TaskCode, "instance", a.InstanceID, "execution", a.ExecutionID, "msg", a.Message)
		}
	}
	w.seen, w.snapshot, w.at = next, all, time.Now().UTC()
	w.mu.Unlock()
	return all, nil
}

func (w *Watcher) Snapshot() ([]*repo.Alert, time.Time) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.snapshot, w.at
}

func (w *Watcher) Run(ctx context.Context) {
	t := time.NewTicker(w.opt.Interval)
	defer t.Stop()
	for {
		if _, err := w.Evaluate(ctx); err != nil && ctx.Err() == nil {
			w.log.Error("monitor evaluate failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
