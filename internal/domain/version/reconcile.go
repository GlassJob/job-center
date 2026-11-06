// Package version 版本对账（openapi VersionReconciliation.state）。
package version

import (
	"strings"
	"time"
)

const (
	Consistent       = "CONSISTENT"
	Behind           = "BEHIND"
	Mixed            = "MIXED"
	NoOnlineInstance = "NO_ONLINE_INSTANCE"
	StaleProcess     = "STALE_PROCESS"
	NoExpected       = "NO_EXPECTED_VERSION"
	MissingTaskDef   = "MISSING_TASK_DEFINITION"
)

type Expected struct {
	Version    string
	GitCommit  string
	DeployedAt *time.Time
}

type Running struct {
	AppVersion string
	GitCommit  string
	StartedAt  time.Time
}

// CommitMatch 允许短 SHA（≥7 位）前缀匹配。
func CommitMatch(a, b string) bool {
	a, b = strings.ToLower(strings.TrimSpace(a)), strings.ToLower(strings.TrimSpace(b))
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	return len(a) >= 7 && strings.HasPrefix(b, a)
}

// Matches 优先比较 git_commit；预期无 commit 时比较版本号。
func Matches(e Expected, r Running) bool {
	if e.GitCommit != "" && r.GitCommit != "" {
		return CommitMatch(e.GitCommit, r.GitCommit)
	}
	return e.Version != "" && e.Version == r.AppVersion
}

// State 计算对账状态。running 只含在线类实例（ONLINE/SUSPECT/DRAINING）。
// missingDefs：预期版本已上线但仍有任务定义未被任何在线实例注册。
func State(e *Expected, running []Running, missingDefs bool) (string, []bool) {
	flags := make([]bool, len(running))
	if e == nil {
		return NoExpected, flags
	}
	if len(running) == 0 {
		return NoOnlineInstance, flags
	}
	match, stale := 0, 0
	for i, r := range running {
		if Matches(*e, r) {
			flags[i] = true
			match++
		} else if e.DeployedAt != nil && r.StartedAt.Before(*e.DeployedAt) {
			stale++
		}
	}
	switch {
	case match == len(running):
		if missingDefs {
			return MissingTaskDef, flags
		}
		return Consistent, flags
	case match > 0:
		return Mixed, flags
	case stale == len(running):
		return StaleProcess, flags
	default:
		return Behind, flags
	}
}
