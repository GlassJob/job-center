package schedule

import (
	"testing"
	"time"

	"github.com/GlassJob/job-center/internal/domain/task"
)

func TestComputeFiresNormal(t *testing.T) {
	sched, err := task.ValidateCron("0 */10 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	loc := time.UTC
	next := time.Date(2026, 11, 5, 8, 10, 0, 0, time.UTC)
	now := next.Add(5 * time.Second) // within threshold
	plan := ComputeFires(sched, next, now, loc, "SKIP", 60, 10)
	if len(plan.Fires) != 1 || plan.Fires[0].TriggerType != "CRON" || plan.SkippedCount != 0 {
		t.Fatalf("%+v", plan)
	}
}

func TestComputeFiresSKIP(t *testing.T) {
	sched, err := task.ValidateCron("0 * * * * *") // every minute
	if err != nil {
		t.Fatal(err)
	}
	loc := time.UTC
	next := time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC)
	now := next.Add(5 * time.Minute) // 5 missed
	plan := ComputeFires(sched, next, now, loc, "SKIP", 60, 10)
	if len(plan.Fires) != 0 || plan.SkippedCount < 4 || plan.SkipAuditAt.IsZero() {
		t.Fatalf("%+v", plan)
	}
	if !plan.SkipAuditAt.Equal(next) {
		t.Fatalf("audit at %v want %v", plan.SkipAuditAt, next)
	}
}

func TestComputeFiresFIRE_ONCE(t *testing.T) {
	sched, _ := task.ValidateCron("0 * * * * *")
	loc := time.UTC
	next := time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC)
	now := next.Add(3 * time.Minute)
	plan := ComputeFires(sched, next, now, loc, "FIRE_ONCE", 60, 10)
	if len(plan.Fires) != 1 || plan.Fires[0].TriggerType != "CATCH_UP" {
		t.Fatalf("%+v", plan)
	}
}

func TestComputeFiresCATCH_UPCap(t *testing.T) {
	sched, _ := task.ValidateCron("*/1 * * * * *") // every second
	loc := time.UTC
	next := time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC)
	now := next.Add(30 * time.Second)
	plan := ComputeFires(sched, next, now, loc, "CATCH_UP", 1, 5)
	if len(plan.Fires) != 5 {
		t.Fatalf("want 5 fires got %d %+v", len(plan.Fires), plan)
	}
	if plan.SkippedCount < 1 {
		t.Fatalf("expected skipped beyond cap: %+v", plan)
	}
}

func TestRetryDelay(t *testing.T) {
	p := ParseRetryPolicy([]byte(`{"max_attempts":3,"strategy":"EXPONENTIAL","initial_delay_seconds":10,"multiplier":2,"jitter":0,"max_delay_seconds":600}`))
	d1 := p.Delay(1)
	d2 := p.Delay(2)
	if d1 != 10*time.Second || d2 != 20*time.Second {
		t.Fatalf("%v %v", d1, d2)
	}
	if !p.ShouldRetryLost(1) || p.ShouldRetryLost(3) {
		t.Fatal("lost retry bounds")
	}
	if p.ShouldRetryTimeout(1) {
		t.Fatal("default retry_on_timeout false")
	}
}
