package execution

import (
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	recent, old := now.Add(-30*time.Second), now.Add(-90*time.Second)
	base := ReconcileInput{Found: true, Status: "RUNNING", LeaseMatches: true, OwnedByCaller: true, IsCurrent: true, Now: now, LostGrace: time.Minute, LocalState: "RUNNING"}
	mod := func(f func(*ReconcileInput)) ReconcileInput { c := base; f(&c); return c }
	cases := []struct {
		name     string
		in       ReconcileInput
		dec, why string
	}{
		{"not found", mod(func(i *ReconcileInput) { i.Found = false }), DecisionAbort, "ATTEMPT_NOT_FOUND"},
		{"running ok", base, DecisionContinue, ""},
		{"finished unreported", mod(func(i *ReconcileInput) { i.LocalState = "FINISHED_UNREPORTED" }), DecisionSendResult, ""},
		{"terminal", mod(func(i *ReconcileInput) { i.Status = "SUCCEEDED"; i.LocalState = "FINISHED_UNREPORTED" }), DecisionAbort, "ALREADY_FINAL"},
		{"token mismatch", mod(func(i *ReconcileInput) { i.LeaseMatches = false }), DecisionAbort, "LEASE_INVALID"},
		{"other instance", mod(func(i *ReconcileInput) { i.OwnedByCaller = false }), DecisionAbort, "LEASE_INVALID"},
		{"lost within grace", mod(func(i *ReconcileInput) { i.Status = "LOST"; i.LostAt = &recent }), DecisionContinue, ""},
		{"lost within grace finished", mod(func(i *ReconcileInput) { i.Status = "LOST"; i.LostAt = &recent; i.LocalState = "FINISHED_UNREPORTED" }), DecisionSendResult, ""},
		{"lost after grace", mod(func(i *ReconcileInput) { i.Status = "LOST"; i.LostAt = &old }), DecisionAbort, "LEASE_INVALID"},
		{"lost superseded", mod(func(i *ReconcileInput) { i.Status = "LOST"; i.LostAt = &recent; i.IsCurrent = false }), DecisionAbort, "LEASE_INVALID"},
	}
	for _, c := range cases {
		d, r := Decide(c.in)
		if d != c.dec || r != c.why {
			t.Errorf("%s: got %s/%s want %s/%s", c.name, d, r, c.dec, c.why)
		}
	}
}
