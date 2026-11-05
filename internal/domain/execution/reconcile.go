// Package execution 包含 Execution/Attempt 领域规则。P1 只实现重连时的 Attempt 裁决（纯函数）；
// 状态迁移、租约续期与派发在 P2 实现。
package execution

import "time"

const (
	DecisionContinue   = "CONTINUE"
	DecisionAbort      = "ABORT"
	DecisionSendResult = "SEND_RESULT"
)

// Terminal Attempt 终态（LOST 单独处理：可能在 lost_grace 内被原执行者认领）。
var Terminal = map[string]bool{"SUCCEEDED": true, "FAILED": true, "CANCELLED": true, "TIMED_OUT": true}

// ReconcileInput 重连时 SDK 上报的一个 running_attempt 与中心记录。
type ReconcileInput struct {
	Found         bool
	Status        string
	LeaseMatches  bool
	OwnedByCaller bool // attempt.instance_id == 上报实例
	IsCurrent     bool // execution.current_attempt_id == attempt_id
	LostAt        *time.Time
	Now           time.Time // 数据库时间
	LostGrace     time.Duration
	LocalState    string // ACCEPTED|RUNNING|FINISHED_UNREPORTED|CANCELLING
}

// Decide 按 protocol-websocket.md §6 裁决表返回 decision 与 reason。
func Decide(in ReconcileInput) (decision, reason string) {
	if !in.Found {
		return DecisionAbort, "ATTEMPT_NOT_FOUND"
	}
	if Terminal[in.Status] {
		return DecisionAbort, "ALREADY_FINAL"
	}
	if !in.LeaseMatches || !in.OwnedByCaller {
		return DecisionAbort, "LEASE_INVALID"
	}
	if in.Status == "LOST" {
		if !in.IsCurrent || in.LostAt == nil || in.Now.Sub(*in.LostAt) > in.LostGrace {
			return DecisionAbort, "LEASE_INVALID"
		}
	}
	if in.LocalState == "FINISHED_UNREPORTED" {
		return DecisionSendResult, ""
	}
	return DecisionContinue, ""
}
