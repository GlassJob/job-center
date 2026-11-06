package version

import (
	"testing"
	"time"
)

func TestState(t *testing.T) {
	dep := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	e := &Expected{Version: "1.2.0", GitCommit: "abcdef1234567", DeployedAt: &dep}
	ok := Running{AppVersion: "1.2.0", GitCommit: "abcdef1", StartedAt: dep.Add(time.Minute)}
	old := Running{AppVersion: "1.1.0", GitCommit: "999999999", StartedAt: dep.Add(-time.Hour)}
	newWrong := Running{AppVersion: "1.1.0", GitCommit: "8888888", StartedAt: dep.Add(time.Hour)}
	cases := []struct {
		e    *Expected
		r    []Running
		miss bool
		want string
	}{
		{nil, []Running{ok}, false, NoExpected},
		{e, nil, false, NoOnlineInstance},
		{e, []Running{ok}, false, Consistent},
		{e, []Running{ok}, true, MissingTaskDef},
		{e, []Running{ok, old}, false, Mixed},
		{e, []Running{old}, false, StaleProcess},
		{e, []Running{newWrong}, false, Behind},
	}
	for i, c := range cases {
		if got, _ := State(c.e, c.r, c.miss); got != c.want {
			t.Errorf("case %d: got %s want %s", i, got, c.want)
		}
	}
	if CommitMatch("abc", "abcdef") {
		t.Error("too-short prefix must not match")
	}
	if !Matches(Expected{Version: "2"}, Running{AppVersion: "2"}) {
		t.Error("version fallback")
	}
}
