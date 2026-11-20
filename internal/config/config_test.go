package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kushal1o1/bunker/internal/pathexp"
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
	if cfg.Storage.S3 != nil {
		t.Errorf("storage.s3 = %+v, want nil by default", cfg.Storage.S3)
	}
	// The default is "~/<x>", so it must arrive fully expanded and absolute --
	// never the literal "~/<x>", which would resolve relative to the CWD.
	wantDir := filepath.Join(pathexp.Home(), "backups")
	if cfg.Storage.Dir != wantDir {
		t.Errorf("storage dir = %q, want %q", cfg.Storage.Dir, wantDir)
	}
	if !filepath.IsAbs(cfg.Storage.Dir) {
		t.Errorf("storage dir %q is not absolute", cfg.Storage.Dir)
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

// A tilde in storage.dir must be expanded at load time. Left as a literal
// string it becomes a path relative to the working directory, so snapshots
// are written to "./~/..." and the dry-run banner prints a location that is
// not where the data goes.
func TestStorageDirTildeIsExpandedNotRelative(t *testing.T) {
	path := writeTemp(t, "bunker.yaml", `
servers:
  vps-01:
    host: 203.0.113.1
    backup:
      paths: [/srv/data]
storage:
  dir: ~/backupsfromserver
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := filepath.Join(pathexp.Home(), "backupsfromserver")
	if cfg.Storage.Dir != want {
		t.Errorf("storage.dir = %q, want %q", cfg.Storage.Dir, want)
	}
	if strings.Contains(cfg.Storage.Dir, "~") {
		t.Errorf("storage.dir still contains a tilde: %q", cfg.Storage.Dir)
	}
	if !filepath.IsAbs(cfg.Storage.Dir) {
		t.Errorf("storage.dir %q is not absolute", cfg.Storage.Dir)
	}
}

// An already-relative dir is resolved against the working directory, so the
// printed path is honest about where snapshots will actually land.
func TestStorageDirRelativeBecomesAbsolute(t *testing.T) {
	path := writeTemp(t, "bunker.yaml", `
servers:
  vps-01:
    host: 203.0.113.1
    backup:
      paths: [/srv/data]
storage:
  dir: ./out/backups
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !filepath.IsAbs(cfg.Storage.Dir) {
		t.Errorf("storage.dir %q is not absolute", cfg.Storage.Dir)
	}
	if filepath.Base(cfg.Storage.Dir) != "backups" {
		t.Errorf("storage.dir = %q, want it to end in backups", cfg.Storage.Dir)
	}
}

func TestConnectTimeout(t *testing.T) {
	cases := []struct {
		name    string
		line    string // under ssh:, empty for none
		want    time.Duration
		wantErr string
	}{
		{name: "default", want: DefaultSSHTimeout},
		{name: "custom", line: "connect_timeout: 5s", want: 5 * time.Second},
		{name: "minutes", line: "connect_timeout: 1m", want: time.Minute},
		{name: "bare number", line: "connect_timeout: 10", wantErr: "cannot unmarshal !!int `10` into time.Duration"},
		{name: "too small", line: "connect_timeout: 500ms", wantErr: "at least 1s"},
		{name: "not a duration", line: "connect_timeout: soon", wantErr: "parse config"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTemp(t, "bunker.yaml", `
servers:
  vps-01:
    host: 203.0.113.10
    ssh:
      user: bunker
      `+tc.line+`
    backup:
      paths: [/srv/data]
`)
			cfg, err := Load(path)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.Servers["vps-01"].SSH.ConnectTimeout; got != tc.want {
				t.Errorf("connect_timeout = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestKeepAliveInterval(t *testing.T) {
	cases := []struct {
		name    string
		line    string // under ssh:, empty for none
		want    time.Duration
		wantErr string
	}{
		{name: "default", want: DefaultKeepAlive},
		{name: "custom", line: "keepalive_interval: 30s", want: 30 * time.Second},
		{name: "too small", line: "keepalive_interval: 200ms", wantErr: "at least 1s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTemp(t, "bunker.yaml", `
servers:
  vps-01:
    host: 203.0.113.10
    ssh:
      user: bunker
      `+tc.line+`
    backup:
      paths: [/srv/data]
`)
			cfg, err := Load(path)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.Servers["vps-01"].SSH.KeepAliveInterval; got != tc.want {
				t.Errorf("keepalive_interval = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestStorageLocations(t *testing.T) {
	const s3Block = `
  s3:
    endpoint: https://s3.eu-central-1.amazonaws.com/
    bucket: my-bunker
    prefix: /prod`
	cases := []struct {
		name      string
		storage   string // body under storage:, empty for no storage block
		wantLocal bool
		wantS3    bool
		wantErr   string
	}{
		{name: "no storage block is local", wantLocal: true},
		{name: "dir only", storage: "\n  dir: ./b", wantLocal: true},
		{name: "s3 only", storage: s3Block, wantS3: true},
		{name: "both", storage: "\n  dir: ./b" + s3Block, wantLocal: true, wantS3: true},
		{name: "type is gone", storage: "\n  type: local", wantErr: "field type not found"},
		{name: "no scheme", storage: "\n  s3:\n    endpoint: s3.amazonaws.com\n    bucket: b", wantErr: "https:// or http://"},
		{name: "path in endpoint", storage: "\n  s3:\n    endpoint: https://s3.amazonaws.com/my-bunker\n    bucket: b", wantErr: "must not contain a path"},
		{name: "no bucket", storage: "\n  s3:\n    endpoint: https://s3.amazonaws.com", wantErr: "bucket is required"},
		{name: "no endpoint", storage: "\n  s3:\n    bucket: b", wantErr: "endpoint is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `
servers:
  vps-01:
    host: 203.0.113.10
    backup:
      paths: [/srv/data]
`
			if tc.storage != "" {
				body += "storage:" + tc.storage + "\n"
			}
			cfg, err := Load(writeTemp(t, "bunker.yaml", body))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.Storage.Dir != ""; got != tc.wantLocal {
				t.Errorf("local = %v (dir %q), want %v", got, cfg.Storage.Dir, tc.wantLocal)
			}
			if got := cfg.Storage.S3 != nil; got != tc.wantS3 {
				t.Fatalf("s3 = %v, want %v", got, tc.wantS3)
			}
			if tc.wantS3 {
				s3 := cfg.Storage.S3
				if s3.Endpoint != "https://s3.eu-central-1.amazonaws.com" {
					t.Errorf("endpoint = %q, want the trailing slash trimmed", s3.Endpoint)
				}
				if s3.Prefix != "prod/" {
					t.Errorf("prefix = %q, want \"prod/\"", s3.Prefix)
				}
			}
		})
	}
}
