package idgen

import (
	"regexp"
	"testing"
)

func TestULIDFormatAndOrder(t *testing.T) {
	re := regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
	prev := ""
	seen := map[string]bool{}
	for i := 0; i < 10000; i++ {
		id := NewULID()
		if !re.MatchString(id) {
			t.Fatalf("bad ulid %q", id)
		}
		if id <= prev {
			t.Fatalf("not monotonic: %s <= %s", id, prev)
		}
		if seen[id] {
			t.Fatalf("dup %s", id)
		}
		seen[id] = true
		prev = id
	}
}
