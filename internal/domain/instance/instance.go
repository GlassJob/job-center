// Package instance 定义执行器实例状态机（docs/state-machine.md §3）。
// 注意：实例/连接状态 ≠ 执行状态（任务书 20.3）；SUSPECT 不会直接导致 Attempt 失败。
package instance

import "fmt"

type Status string

const (
	Registering Status = "REGISTERING"
	Online      Status = "ONLINE"
	Suspect     Status = "SUSPECT"
	Offline     Status = "OFFLINE"
	Draining    Status = "DRAINING"
)

var transitions = map[Status]map[Status]bool{
	Registering: {Online: true, Offline: true, Draining: true},
	Online:      {Suspect: true, Offline: true, Draining: true, Registering: true},
	Suspect:     {Online: true, Offline: true, Draining: true, Registering: true},
	Draining:    {Offline: true, Suspect: true, Registering: true},
	Offline:     {Registering: true},
}

// CanTransition 报告迁移是否合法（同状态视为幂等合法）。
// X→REGISTERING 只发生在同 instance_id 新会话认证成功时（session_epoch+1）。
func CanTransition(from, to Status) bool {
	if from == to {
		return true
	}
	return transitions[from][to]
}

func Parse(s string) (Status, error) {
	st := Status(s)
	if _, ok := transitions[st]; !ok {
		return "", fmt.Errorf("unknown instance status %q", s)
	}
	return st, nil
}

// Dispatchable 只有 ONLINE 实例可接新任务。
func (s Status) Dispatchable() bool { return s == Online }

// AfterDisconnect 连接断开后的目标状态：DRAINING/REGISTERING → OFFLINE；
// 正常关闭(1000)且没有未终结 Attempt → OFFLINE；其余 → SUSPECT（等待重连或超时）。
func AfterDisconnect(cur Status, normalClose bool, activeAttempts int) Status {
	switch cur {
	case Offline:
		return Offline
	case Draining, Registering:
		return Offline
	}
	if normalClose && activeAttempts == 0 {
		return Offline
	}
	return Suspect
}
