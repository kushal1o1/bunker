package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadAppliesDefaults(t *testing.T) {
	path := writeTemp(t, "bunker.yaml", `
servers:
  vps-01:
    host: 203.0.113.10
    backup:
      postgres:
        dbs: [project1_db]
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Concurrency != DefaultConcurrency {
		t.Errorf("concurrency = %d, want %d", cfg.Concurrency, DefaultConcurrency)
	}
	if cfg.Storage.Type != DefaultStorageType || cfg.Storage.Dir != DefaultStorageDir {
		t.Errorf("storage defaults not applied: %+v", cfg.Storage)
	}
	if cfg.Retention.Daily != DefaultRetentionD || cfg.Retention.Weekly != DefaultRetentionW || cfg.Retention.Monthly != DefaultRetentionM {
		t.Errorf("retention defaults not applied: %+v", cfg.Retention)
	}
	s := cfg.Servers["vps-01"]
	if s.SSH.User != DefaultSSHUser {
		t.Errorf("ssh user = %q, want %q", s.SSH.User, DefaultSSHUser)
	}
	if s.SSH.Port != DefaultPort {
		t.Errorf("ssh port = %d, want %d", s.SSH.Port, DefaultPort)
	}
}

func TestLoadMissingHost(t *testing.T) {
	path := writeTemp(t, "bunker.yaml", `
servers:
  vps-01:
    backup:
      paths: [/srv/project1/media]
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for missing host, got nil")
	}
	if !strings.Contains(err.Error(), "host is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadNoBackupTarget(t *testing.T) {
	path := writeTemp(t, "bunker.yaml", `
servers:
  vps-01:
    host: 203.0.113.10
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for empty backup spec, got nil")
	}
	if !strings.Contains(err.Error(), "postgres db or file path") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadUnknownField(t *testing.T) {
	path := writeTemp(t, "bunker.yaml", `
servers:
  vps-01:
    host: 203.0.113.10
    backup:
      paths: [/srv/project1/media]
concurrenc: 5
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
}

func TestLoadEmptyServers(t *testing.T) {
	path := writeTemp(t, "bunker.yaml", `{}`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for empty servers, got nil")
	}
}

func TestServerNamesSorted(t *testing.T) {
	path := writeTemp(t, "bunker.yaml", `
servers:
  zebra:
    host: 203.0.113.1
    backup:
      paths: [/m]
  alpha:
    host: 203.0.113.2
    backup:
      paths: [/m]
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	names := cfg.ServerNames()
	if len(names) != 2 || names[0] != "alpha" || names[1] != "zebra" {
		t.Errorf("ServerNames = %v, want [alpha zebra]", names)
	}
}