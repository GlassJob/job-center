package schedule

import (
	"fmt"
	"time"

	"github.com/GlassJob/job-center/internal/domain/task"
	"github.com/robfig/cron/v3"
)

// ParseCron 解析严格 6 字段 Cron（与 task.ValidateCron 同一 parser）。
func ParseCron(expr string) (cron.Schedule, error) {
	return task.ValidateCron(expr)
}

// ResolveLocation 任务级时区覆盖；tz 为空则用 defaultLoc。
func ResolveLocation(tz *string, defaultLoc *time.Location) (*time.Location, error) {
	if tz == nil || *tz == "" {
		if defaultLoc == nil {
			return time.UTC, nil
		}
		return defaultLoc, nil
	}
	loc, err := time.LoadLocation(*tz)
	if err != nil {
		return nil, fmt.Errorf("invalid timezone %q: %w", *tz, err)
	}
	return loc, nil
}

// NextFire 计算 now 之后下一次触发（结果 UTC）。now 按 loc 解释。
func NextFire(sched cron.Schedule, now time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	return sched.Next(now.In(loc)).UTC()
}

// NextFireAfter 从 after（含）之后找下一次；若 after 为零则等同 NextFire(now)。
func NextFireAfter(sched cron.Schedule, after, now time.Time, loc *time.Location) time.Time {
	base := after
	if base.IsZero() || !base.After(now) {
		// docs: next_fire_at = next(after max(now, last t))
		if after.After(now) {
			base = after
		} else {
			base = now
		}
	}
	return NextFire(sched, base, loc)
}
