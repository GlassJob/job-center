package sse

import (
	"testing"
	"time"
)

func TestProgressThrottle(t *testing.T) {
	th := NewProgressThrottle(time.Second)
	t0 := time.Unix(1000, 0).UTC()
	if !th.Allow(t0, 1, "a") {
		t.Fatal("first emit")
	}
	if th.Allow(t0.Add(100*time.Millisecond), 1, "b") {
		t.Fatal("within 1s must suppress")
	}
	if !th.Allow(t0.Add(time.Second), 1, "b") {
		t.Fatal("after 1s allow")
	}
	if th.Allow(t0.Add(time.Second+10*time.Millisecond), 1, "b") {
		t.Fatal("same key suppress")
	}
	if !th.Allow(t0.Add(time.Second+20*time.Millisecond), 2, "c") {
		t.Fatal("new attempt immediate")
	}
}
