package schedule

import (
	"encoding/json"
	"math"
	"math/rand"
	"time"
)

// RetryPolicy 对应 job_schedule.retry_policy / execution 快照。
type RetryPolicy struct {
	MaxAttempts         int      `json:"max_attempts"`
	Strategy            string   `json:"strategy"` // EXPONENTIAL | FIXED
	InitialDelaySeconds float64  `json:"initial_delay_seconds"`
	MaxDelaySeconds     float64  `json:"max_delay_seconds"`
	Multiplier          float64  `json:"multiplier"`
	Jitter              float64  `json:"jitter"`
	RetryableErrorCodes []string `json:"retryable_error_codes"`
	RetryOnTimeout      bool     `json:"retry_on_timeout"`
	RetryOnLost         bool     `json:"retry_on_lost"`
}

// ParseRetryPolicy 解析 JSON；缺省 max_attempts=1、retry_on_lost=true。
func ParseRetryPolicy(raw json.RawMessage) RetryPolicy {
	p := RetryPolicy{
		MaxAttempts:         1,
		Strategy:            "EXPONENTIAL",
		InitialDelaySeconds: 10,
		MaxDelaySeconds:     600,
		Multiplier:          2,
		Jitter:              0.2,
		RetryableErrorCodes: []string{"*"},
		RetryOnTimeout:      false,
		RetryOnLost:         true,
	}
	if len(raw) == 0 {
		return p
	}
	_ = json.Unmarshal(raw, &p)
	if p.MaxAttempts < 1 {
		p.MaxAttempts = 1
	}
	if p.Strategy == "" {
		p.Strategy = "EXPONENTIAL"
	}
	if p.InitialDelaySeconds <= 0 {
		p.InitialDelaySeconds = 10
	}
	if p.MaxDelaySeconds <= 0 {
		p.MaxDelaySeconds = 600
	}
	if p.Multiplier <= 0 {
		p.Multiplier = 2
	}
	if p.RetryableErrorCodes == nil {
		p.RetryableErrorCodes = []string{"*"}
	}
	return p
}

// Delay 第 n 次重试前的等待（n 从 1 起，即第二次 attempt 的退避）。
func (p RetryPolicy) Delay(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	var base float64
	switch p.Strategy {
	case "FIXED":
		base = p.InitialDelaySeconds
	default:
		base = p.InitialDelaySeconds * math.Pow(p.Multiplier, float64(n-1))
	}
	if base > p.MaxDelaySeconds {
		base = p.MaxDelaySeconds
	}
	if p.Jitter > 0 {
		j := p.Jitter
		if j > 1 {
			j = 1
		}
		factor := 1 + (rand.Float64()*2-1)*j // 1 ± jitter
		base *= factor
		if base < 0 {
			base = 0
		}
	}
	return time.Duration(base * float64(time.Second))
}

// ShouldRetryFailed 业务失败是否重试。
func (p RetryPolicy) ShouldRetryFailed(attemptCount int, reason, errCode string, retryable *bool) bool {
	if attemptCount >= p.MaxAttempts {
		return false
	}
	if reason == "INVALID_PARAMS" || reason == "UNKNOWN_TASK" {
		return false
	}
	if retryable != nil && !*retryable {
		return false
	}
	return codeMatches(p.RetryableErrorCodes, errCode)
}

// ShouldRetryTimeout / ShouldRetryLost。
func (p RetryPolicy) ShouldRetryTimeout(attemptCount int) bool {
	return p.RetryOnTimeout && attemptCount < p.MaxAttempts
}

func (p RetryPolicy) ShouldRetryLost(attemptCount int) bool {
	return p.RetryOnLost && attemptCount < p.MaxAttempts
}

func codeMatches(codes []string, code string) bool {
	if len(codes) == 0 {
		return true
	}
	for _, c := range codes {
		if c == "*" || c == code {
			return true
		}
	}
	return false
}
