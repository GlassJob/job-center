package sse

import "time"

// ProgressThrottle 合并 progress：每 Attempt 每秒最多 1 条（protocol-http.md §5）。
type ProgressThrottle struct {
	minInterval time.Duration
	lastAttempt int
	lastKey     string
	lastEmit    time.Time
}

func NewProgressThrottle(minInterval time.Duration) *ProgressThrottle {
	if minInterval <= 0 {
		minInterval = time.Second
	}
	return &ProgressThrottle{minInterval: minInterval}
}

// Allow 在内容变化且距该 Attempt 上次推送 ≥ minInterval 时返回 true。
// 首次或 Attempt 切换立即放行。key 建议用 updated_at+percent+message 指纹。
func (t *ProgressThrottle) Allow(now time.Time, attempt int, key string) bool {
	if key == "" {
		return false
	}
	if attempt != t.lastAttempt {
		t.lastAttempt = attempt
		t.lastKey = key
		t.lastEmit = now
		return true
	}
	if key == t.lastKey {
		return false
	}
	if !t.lastEmit.IsZero() && now.Sub(t.lastEmit) < t.minInterval {
		return false
	}
	t.lastKey = key
	t.lastEmit = now
	return true
}
