package schedule

import (
	"time"

	"github.com/robfig/cron/v3"
)

// FirePlan 一次调度周期要创建的触发点。
type FirePlan struct {
	// Fires 要真正创建的执行点（CRON 或 CATCH_UP）。
	Fires []FireTick
	// SkippedCount misfire SKIP 汇总跳过的点数（不含 Fires）。
	SkippedCount int
	// SkipAuditAt SKIP 策略下写 1 条 SKIPPED 审计的 scheduled_at（最早错过点）；零值表示不写。
	SkipAuditAt time.Time
	// LastTick 本轮处理到的最后一个计划点（用于 last_fire_at）。
	LastTick time.Time
}

// FireTick 单个触发。
type FireTick struct {
	ScheduledAt time.Time
	TriggerType string // CRON | CATCH_UP
}

// ComputeFires 按 misfire 策略计算本轮应创建的触发点。
// nextFireAt 为库中到期的 next_fire_at；threshold 为 misfire_threshold_seconds。
// catchUpMax 上限 ≤100。
func ComputeFires(sched cron.Schedule, nextFireAt, now time.Time, loc *time.Location, policy string, thresholdSec, catchUpMax int) FirePlan {
	if loc == nil {
		loc = time.UTC
	}
	if thresholdSec < 0 {
		thresholdSec = 60
	}
	if catchUpMax < 1 {
		catchUpMax = 10
	}
	if catchUpMax > 100 {
		catchUpMax = 100
	}
	if policy == "" {
		policy = "SKIP"
	}

	late := now.Sub(nextFireAt) > time.Duration(thresholdSec)*time.Second
	if !late {
		// 轻微延迟：正常触发一次
		return FirePlan{
			Fires:    []FireTick{{ScheduledAt: nextFireAt.UTC(), TriggerType: "CRON"}},
			LastTick: nextFireAt.UTC(),
		}
	}

	// 枚举错过点：从 nextFireAt 起迭代 Next，最多 catchUpMax+1 次
	missed := make([]time.Time, 0, catchUpMax+1)
	cur := nextFireAt
	for i := 0; i < catchUpMax+1; i++ {
		if cur.After(now) {
			break
		}
		missed = append(missed, cur.UTC())
		nxt := sched.Next(cur.In(loc)).UTC()
		if !nxt.After(cur) {
			break
		}
		cur = nxt
	}
	if len(missed) == 0 {
		return FirePlan{}
	}

	var plan FirePlan
	plan.LastTick = missed[len(missed)-1]
	switch policy {
	case "FIRE_ONCE":
		last := missed[len(missed)-1]
		plan.Fires = []FireTick{{ScheduledAt: last, TriggerType: "CATCH_UP"}}
		plan.SkippedCount = len(missed) - 1
	case "CATCH_UP":
		limit := catchUpMax
		if len(missed) > limit {
			plan.SkippedCount = len(missed) - limit
			missed = missed[:limit]
		}
		for _, t := range missed {
			plan.Fires = append(plan.Fires, FireTick{ScheduledAt: t, TriggerType: "CATCH_UP"})
		}
		plan.LastTick = missed[len(missed)-1]
	default: // SKIP
		plan.SkipAuditAt = missed[0]
		plan.SkippedCount = len(missed)
		plan.LastTick = missed[len(missed)-1]
	}
	return plan
}
