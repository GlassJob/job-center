package security

import (
	"strings"
	"testing"
	"time"
)

func TestCredentialRoundTrip(t *testing.T) {
	tok, kid, sec, err := NewCredential()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, "jcs_"+kid+".") {
		t.Fatalf("token format %q", tok)
	}
	k2, s2, err := ParseCredential(tok)
	if err != nil || k2 != kid || s2 != sec {
		t.Fatalf("parse: %v %s %s", err, k2, s2)
	}
	h := HashSecret("pepper-0123456789", sec)
	if !VerifySecret("pepper-0123456789", sec, h) {
		t.Fatal("verify failed")
	}
	if VerifySecret("pepper-0123456789", sec+"x", h) || VerifySecret("other-pepper-xxxx", sec, h) {
		t.Fatal("verify should fail")
	}
	if got := RedactCredential("token=" + tok + " end"); strings.Contains(got, sec) || !strings.Contains(got, "jcs_"+kid+".***") {
		t.Fatalf("redact: %s", got)
	}
}

func TestParseCredentialRejects(t *testing.T) {
	for _, s := range []string{"", "jcs_", "abc", "jcs_short.x", "jcs_abcdefgh", "jcs_ABCDEFGH.0123456789012345678901234567890123", "bearer jcs_abcdefgh.0123456789012345678901234567890123"} {
		if _, _, err := ParseCredential(s); err == nil {
			t.Errorf("expected reject %q", s)
		}
	}
}

func TestCanonicalAndSign(t *testing.T) {
	q, err := CanonicalQuery("b=2&a=x y&a=1&c=%7E")
	if err != nil {
		t.Fatal(err)
	}
	if q != "a=1&a=x%20y&b=2&c=~" {
		t.Fatalf("canonical query %q", q)
	}
	op := EncodeOperator(Operator{ID: "1", Name: "alice", Perms: []string{"job.task.view"}})
	c, _ := Canonical("get", "/v1/admin/tasks", "limit=20&page=1", nil, "1793872000", "abc", op)
	lines := strings.Split(c, "\n")
	if len(lines) != 7 || lines[0] != "GET" || lines[2] != "limit=20&page=1" ||
		lines[3] != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("canonical: %q", c)
	}
	sig := Sign("secret-0123456789", c)
	if !VerifySignature("secret-0123456789", c, sig) || VerifySignature("secret-0123456789", c+"x", sig) || VerifySignature("s", c, "zz") {
		t.Fatal("signature verify mismatch")
	}
	o, err := DecodeOperator(op)
	if err != nil || o.Name != "alice" || !o.Has("job.task.view") || o.Has("job.task.execute") {
		t.Fatalf("operator: %+v %v", o, err)
	}
	if _, err := DecodeOperator("!!"); err == nil {
		t.Fatal("bad operator accepted")
	}
}

func TestNonceCache(t *testing.T) {
	c := NewNonceCache(time.Minute)
	now := time.Now()
	c.now = func() time.Time { return now }
	if !c.Use("a", "n1") || c.Use("a", "n1") || !c.Use("b", "n1") {
		t.Fatal("nonce semantics")
	}
	now = now.Add(2 * time.Minute)
	if !c.Use("a", "n1") {
		t.Fatal("expired nonce should be reusable")
	}
}
