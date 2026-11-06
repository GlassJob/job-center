package security

import (
	"testing"
	"time"
)

func TestSSETicket(t *testing.T) {
	ti := NewTicketIssuer("k", time.Minute)
	raw, _ := ti.Issue("E1", Operator{ID: "u1", Perms: []string{"job.execution.log.view"}})
	if _, err := ti.Redeem(raw, "E2"); err == nil {
		t.Fatal("wrong execution accepted")
	}
	tk, err := ti.Redeem(raw, "E1")
	if err != nil || tk.OperatorID != "u1" {
		t.Fatal(err)
	}
	if _, err := ti.Redeem(raw, "E1"); err == nil {
		t.Fatal("reuse accepted")
	}
	if _, err := ti.Redeem(raw+"x", "E1"); err == nil {
		t.Fatal("tampered accepted")
	}
	ti.now = func() time.Time { return time.Now().Add(-2 * time.Minute) }
	old, _ := ti.Issue("E1", Operator{ID: "u1"})
	ti.now = time.Now
	if _, err := ti.Redeem(old, "E1"); err == nil {
		t.Fatal("expired accepted")
	}
}
