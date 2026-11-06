package sse

import "testing"

func TestCursor(t *testing.T) {
	c, err := Parse("1052:1:37")
	if err != nil || c.EventID != 1052 || c.AttemptNumber != 1 || c.Seq != 37 || c.String() != "1052:1:37" {
		t.Fatal(c, err)
	}
	if z, err := Parse(""); err != nil || z != (Cursor{}) {
		t.Fatal("empty")
	}
	for _, bad := range []string{"1:2", "a:1:1", "-1:0:0"} {
		if _, err := Parse(bad); err == nil {
			t.Error(bad)
		}
	}
	if !c.LogAfter(2, 1) || !c.LogAfter(1, 38) || c.LogAfter(1, 37) || c.LogAfter(0, 99) {
		t.Error("LogAfter")
	}
}
