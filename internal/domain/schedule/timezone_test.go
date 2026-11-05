package schedule

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveDefaultTimezone(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	_, name, src, err := ResolveDefaultTimezone("America/New_York", env(nil), "/nonexistent")
	if err != nil || name != "America/New_York" || src != "config" {
		t.Fatalf("config: %s %s %v", name, src, err)
	}
	if _, _, _, err := ResolveDefaultTimezone("Mars/Base", env(nil), ""); err == nil {
		t.Fatal("bad configured tz should error")
	}
	_, name, src, _ = ResolveDefaultTimezone("", env(map[string]string{"TZ": ":Europe/Berlin"}), "/nonexistent")
	if name != "Europe/Berlin" || src != "TZ" {
		t.Fatalf("TZ: %s %s", name, src)
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "localtime")
	target := "/usr/share/zoneinfo/Asia/Tokyo"
	if _, err := os.Stat(target); err != nil {
		t.Skip("tzdata not installed")
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, name, _, _ = ResolveDefaultTimezone("", env(nil), link)
	if name != "Asia/Tokyo" {
		t.Fatalf("localtime link: %s", name)
	}
	_, name, src, _ = ResolveDefaultTimezone("", env(nil), filepath.Join(dir, "missing"))
	if name != "UTC" || src != "fallback" {
		t.Fatalf("fallback: %s %s", name, src)
	}
}

func TestValidateTimezone(t *testing.T) {
	s := func(v string) *string { return &v }
	if ValidateTimezone(nil) != nil || ValidateTimezone(s("")) != nil || ValidateTimezone(s("Asia/Shanghai")) != nil {
		t.Fatal("valid tz rejected")
	}
	for _, b := range []string{"Asia/Nowhere", "/etc/passwd", "Asia/ Shanghai"} {
		if ValidateTimezone(s(b)) == nil {
			t.Errorf("%q accepted", b)
		}
	}
}
