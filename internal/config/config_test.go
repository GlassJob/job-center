package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadYAMLAndEnvOverride(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(p, []byte(`
database: {dsn: "u:p@tcp(h:1)/db"}
security: {credential_pepper: "0123456789abcdef"}
instance: {heartbeat_interval: 2s, suspect_after: 6s, offline_after: 20s, sweep_interval: 1s}
`), 0o600)
	t.Setenv("JC_DB_DSN", "env:pw@tcp(127.0.0.1:3307)/glassjob_center")
	t.Setenv("JC_ADMIN_CLIENT_SECRETS", "glassadmin:aaaaaaaaaaaaaaaa,deployer:bbbbbbbbbbbbbbbb")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Database.DSN != "env:pw@tcp(127.0.0.1:3307)/glassjob_center" {
		t.Errorf("env override failed: %s", c.Database.DSN)
	}
	if c.Instance.SuspectAfter.D() != 6*time.Second || c.WebSocket.AuthTimeout.D() != 5*time.Second {
		t.Errorf("durations: %v %v", c.Instance.SuspectAfter.D(), c.WebSocket.AuthTimeout.D())
	}
	if len(c.Security.AdminClients) != 2 || c.Security.AdminClients["deployer"].Secret != "bbbbbbbbbbbbbbbb" {
		t.Errorf("admin clients: %+v", c.Security.AdminClients)
	}
	if c.NodeID == "" {
		t.Error("node id default")
	}
}

func TestValidateErrors(t *testing.T) {
	c := Default()
	if err := c.Validate(); err == nil {
		t.Fatal("missing dsn/pepper must fail")
	}
	c.Database.DSN = "x"
	c.Security.CredentialPepper = "0123456789abcdef"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Instance.SuspectAfter = c.Instance.HeartbeatInterval
	if err := c.Validate(); err == nil {
		t.Fatal("suspect_after must exceed heartbeat")
	}
	if err := Default().ApplyEnv(func(k string) (string, bool) {
		if k == "JC_ADMIN_CLIENT_SECRETS" {
			return "nocolon", true
		}
		return "", false
	}); err == nil {
		t.Fatal("bad env format must fail")
	}
}
