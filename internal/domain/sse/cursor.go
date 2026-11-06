// Package sse SSE 游标 <event_id>:<attempt_number>:<seq>（protocol-http.md §5）。
package sse

import (
	"fmt"
	"strconv"
	"strings"
)

type Cursor struct {
	EventID       int64
	AttemptNumber int
	Seq           int64
}

func (c Cursor) String() string { return fmt.Sprintf("%d:%d:%d", c.EventID, c.AttemptNumber, c.Seq) }

// Parse 空串 = 从头；格式错误返回 error。
func Parse(s string) (Cursor, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Cursor{}, nil
	}
	p := strings.Split(s, ":")
	if len(p) != 3 {
		return Cursor{}, fmt.Errorf("bad cursor %q", s)
	}
	e, err1 := strconv.ParseInt(p[0], 10, 64)
	a, err2 := strconv.Atoi(p[1])
	q, err3 := strconv.ParseInt(p[2], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || e < 0 || a < 0 || q < 0 {
		return Cursor{}, fmt.Errorf("bad cursor %q", s)
	}
	return Cursor{e, a, q}, nil
}

// LogAfter (attempt_number, seq) > (c.AttemptNumber, c.Seq)
func (c Cursor) LogAfter(attempt int, seq int64) bool {
	return attempt > c.AttemptNumber || (attempt == c.AttemptNumber && seq > c.Seq)
}
