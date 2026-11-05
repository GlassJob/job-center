package instance

import "testing"

func TestTransitions(t *testing.T) {
	ok := [][2]Status{{Registering, Online}, {Online, Suspect}, {Suspect, Online}, {Suspect, Offline}, {Online, Draining}, {Draining, Offline}, {Offline, Registering}, {Online, Online}}
	for _, c := range ok {
		if !CanTransition(c[0], c[1]) {
			t.Errorf("%s->%s should be allowed", c[0], c[1])
		}
	}
	bad := [][2]Status{{Offline, Online}, {Offline, Suspect}, {Offline, Draining}, {Draining, Online}}
	for _, c := range bad {
		if CanTransition(c[0], c[1]) {
			t.Errorf("%s->%s should be rejected", c[0], c[1])
		}
	}
	if _, err := Parse("BUSY"); err == nil {
		t.Error("parse should fail")
	}
	if !Online.Dispatchable() || Suspect.Dispatchable() || Draining.Dispatchable() {
		t.Error("dispatchable")
	}
}

func TestAfterDisconnect(t *testing.T) {
	cases := []struct {
		cur    Status
		normal bool
		active int
		want   Status
	}{
		{Online, false, 0, Suspect}, {Online, true, 0, Offline}, {Online, true, 2, Suspect},
		{Draining, false, 1, Offline}, {Registering, false, 0, Offline}, {Suspect, false, 0, Suspect}, {Offline, false, 0, Offline},
	}
	for _, c := range cases {
		if got := AfterDisconnect(c.cur, c.normal, c.active); got != c.want {
			t.Errorf("%+v got %s", c, got)
		}
	}
}
