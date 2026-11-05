package task

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseAndCompareVersion(t *testing.T) {
	for _, s := range []string{"1", "1.2", "1.2.3", "2.0.0-rc.1", "1.0+build.5", "10.20.30"} {
		if _, err := ParseVersion(s); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range []string{"", "v1.0", "1.2.3.4", "1..2", "abc", "1.0-"} {
		if _, err := ParseVersion(s); err == nil {
			t.Errorf("%q should be invalid", s)
		}
	}
	v := func(s string) Version { x, _ := ParseVersion(s); return x }
	cases := []struct {
		a, b string
		want int
	}{{"1.0", "1.0.0", 0}, {"1.2", "1.10", -1}, {"2.0", "1.9.9", 1}, {"1.0.0-rc.1", "1.0.0", -1}, {"1.0+b1", "1.0+b2", 0}}
	for _, c := range cases {
		if got := v(c.a).Compare(v(c.b)); got != c.want {
			t.Errorf("%s vs %s = %d want %d", c.a, c.b, got, c.want)
		}
	}
	if !v("1.0").Compatible(v("1.2")) || v("1.9").Compatible(v("2.0")) {
		t.Error("major compatibility rule")
	}
}

func TestValidateCron(t *testing.T) {
	s, err := ValidateCron("0 */10 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 11, 5, 8, 3, 7, 0, time.UTC)
	if n := s.Next(from); !n.Equal(time.Date(2026, 11, 5, 8, 10, 0, 0, time.UTC)) {
		t.Fatalf("next %v", n)
	}
	for _, e := range []string{"*/10 * * * *", "0 0 0 * * * 2026", "CRON_TZ=UTC 0 0 0 * * *", "@hourly", "61 * * * * *", ""} {
		if _, err := ValidateCron(e); err == nil {
			t.Errorf("%q should be rejected", e)
		}
	}
}

func TestNormalizeSchema(t *testing.T) {
	a, ha, err := NormalizeSchema(json.RawMessage(`{"type":"object","properties":{"b":{"type":"integer"},"a":{"type":"string"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	_, hb, _ := NormalizeSchema(json.RawMessage(`{ "properties":{"a":{"type":"string"},"b":{"type":"integer"}}, "type":"object" }`))
	if ha != hb {
		t.Fatalf("hash should be key-order independent: %s %s (%s)", ha, hb, a)
	}
	_, he, err := NormalizeSchema(nil)
	if err != nil || he == "" {
		t.Fatal("empty schema should hash")
	}
	for _, bad := range []string{`[1]`, `{"type":"nope"}`, `{"$ref":"http://evil.example/x.json"}`, `{"$ref":"file:///etc/passwd"}`} {
		if _, _, err := NormalizeSchema(json.RawMessage(bad)); err == nil {
			t.Errorf("%s should be rejected", bad)
		}
	}
}

func TestTaskAllowed(t *testing.T) {
	if !TaskAllowed(nil, "x") || !TaskAllowed([]string{"erp.*"}, "erp.sync") || TaskAllowed([]string{"erp.*"}, "crm.sync") || TaskAllowed([]string{}, "a") {
		t.Fatal("allowed rules")
	}
}
