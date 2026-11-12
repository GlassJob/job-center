package scheduler

import (
	"sync"
	"time"
)

// 循环工作相位。
const (
	PhaseDisabled = "DISABLED" // 调度器未启用
	PhaseIdle     = "IDLE"     // 等待下一轮（ticker 间隙）
	PhaseRunning  = "RUNNING"  // 正在干活
	PhaseSleeping = "SLEEPING" // Claim 有界 sleep
	PhaseStopped  = "STOPPED"  // 已停止
)

// LoopStatus 单个循环的工作状态快照。
type LoopStatus struct {
	Phase          string     `json:"phase"`
	Detail         string     `json:"detail,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
	LastStartedAt  *time.Time `json:"last_started_at,omitempty"`
	LastFinishedAt *time.Time `json:"last_finished_at,omitempty"`
	LastResult     string     `json:"last_result,omitempty"`
}

// Status 调度器三循环只读状态。
type Status struct {
	Enabled  bool       `json:"enabled"`
	NodeID   string     `json:"node_id"`
	Claim    LoopStatus `json:"claim"`
	Generate LoopStatus `json:"generate"`
	Queued   LoopStatus `json:"queued"`
}

type loopTracker struct {
	mu             sync.Mutex
	phase          string
	detail         string
	updatedAt      time.Time
	lastStartedAt  *time.Time
	lastFinishedAt *time.Time
	lastResult     string
}

func (t *loopTracker) set(phase, detail, result string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now().UTC()
	t.phase = phase
	t.detail = detail
	t.updatedAt = now
	if result != "" {
		t.lastResult = result
	}
	switch phase {
	case PhaseRunning:
		cp := now
		t.lastStartedAt = &cp
	case PhaseIdle, PhaseSleeping, PhaseStopped:
		if t.lastStartedAt != nil {
			cp := now
			t.lastFinishedAt = &cp
		}
	}
}

func (t *loopTracker) snapshot() LoopStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return LoopStatus{
		Phase:          t.phase,
		Detail:         t.detail,
		UpdatedAt:      t.updatedAt,
		LastStartedAt:  t.lastStartedAt,
		LastFinishedAt: t.lastFinishedAt,
		LastResult:     t.lastResult,
	}
}
