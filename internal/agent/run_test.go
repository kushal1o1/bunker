package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"filippo.io/age"
	"github.com/kushal1o1/bunker/internal/agent"
	"github.com/kushal1o1/bunker/internal/backup"
	"github.com/kushal1o1/bunker/internal/config"
	"github.com/kushal1o1/bunker/internal/execx"
	"github.com/kushal1o1/bunker/internal/model"
)

// fakeTools puts pg_dump and tar scripts first on PATH. Each prints its
// arguments, so the test can see exactly what the agent ran. With failDB
// set, pg_dump fails for that database.
func fakeTools(t *testing.T, failDB string) {
	t.Helper()
	dir := t.TempDir()
	scripts := map[string]string{
		"pg_dump": `#!/bin/sh
for a in "$@"; do last="$a"; done
if [ "$last" = "` + failDB + `" ]; then echo "pg_dump: connection to server failed" >&2; exit 1; fi
printf 'PGDUMP %s\n' "$*"
`,
		"tar": `#!/bin/sh
printf 'TAR %s\n' "$*"
`,
	}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

type memStatus struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (m *memStatus) PutStatus(_ context.Context, server string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data == nil {
		m.data = map[string][]byte{}
	}
	m.data[server] = data
	return nil
}

func (m *memStatus) get(t *testing.T, server string) agent.Status {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	var st agent.Status
	if err := json.Unmarshal(m.data[server], &st); err != nil {
		t.Fatalf("status.json for %s: %v", server, err)
	}
	return st
}

// setup returns an agent config encrypting to a fresh key, and the path of
// that key's private half (which, on a real setup, only the laptop has).
func setup(t *testing.T) (*agent.Config, string) {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "key.txt")
	if err := os.WriteFile(keyPath, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &agent.Config{
		Agent: agent.Spec{Name: "web-01"},
		Backup: agent.BackupSpec{
			Postgres: []agent.PostgresTarget{
				{Host: "shop-postgres", User: "bunker", DBs: []string{"app"}},
				{Host: "blog-db", Port: 5433, User: "bunker", DBs: []string{"app"}},
			},
			Paths: []string{"/data/shop/uploads"},
		},
		Storage:    agent.StorageSpec{S3: &model.S3Spec{Endpoint: "https://s3.example.com", Bucket: "b"}},
		Encryption: agent.EncryptionSpec{Recipient: id.Recipient().String()},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	return cfg, keyPath
}

func TestRunOnceThenVerifyOnLaptop(t *testing.T) {
	fakeTools(t, "")
	cfg, keyPath := setup(t)
	store := &backup.LocalStore{Dir: t.TempDir()}
	status := &memStatus{}
	var log bytes.Buffer

	r := &agent.Runner{Cfg: cfg, Store: store, Status: status, Exec: execx.Local{}, Log: &log, Version: "v0.5.0-test"}
	st, err := r.RunOnce(t.Context())
	if err != nil {
		t.Fatalf("RunOnce: %v\n%s", err, log.String())
	}

	man, err := store.Manifest(t.Context(), "web-01", st.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if man.Recipient != cfg.Encryption.Recipient {
		t.Errorf("manifest recipient = %s, want the agent's public key", man.Recipient)
	}
	var files []string
	for _, a := range man.Artifacts {
		files = append(files, a.File)
	}
	wantFiles := "postgres/shop-postgres/app.dump.zst.age postgres/blog-db-5433/app.dump.zst.age files/data-shop-uploads.tar.zst.age"
	if strings.Join(files, " ") != wantFiles {
		t.Errorf("files = %v, want %s", files, wantFiles)
	}

	// The laptop side: the private key verifies what the agent wrote.
	laptop := &config.Config{
		Servers:    map[string]model.ServerSpec{"web-01": {}},
		Encryption: model.EncryptionSpec{Identity: keyPath},
	}
	vr, err := (&backup.Verifier{Cfg: laptop, Store: store}).Verify(t.Context(), "web-01", st.Snapshot)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !vr.OK() {
		t.Fatalf("verify failed: %+v", vr)
	}

	got := status.get(t, "web-01")
	if !got.OK || got.Snapshot != st.Snapshot || got.Artifacts != 3 || got.Size == 0 || got.Version != "v0.5.0-test" || got.Error != "" {
		t.Errorf("status = %+v", got)
	}
	for _, want := range []string{"backup started", "[1/3] postgres/shop-postgres/app", "complete: 3 artifact(s)"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log missing %q:\n%s", want, log.String())
		}
	}
}

// The commands run on the machine are exactly what the config says, passed
// as separate arguments with no shell in between.
func TestRunOnceRunsExpectedCommands(t *testing.T) {
	fakeTools(t, "")
	cfg, _ := setup(t)
	cfg.Backup.Paths = []string{"/srv/my files/$HOME"}
	ex := &recordingExec{}
	r := &agent.Runner{Cfg: cfg, Store: &backup.LocalStore{Dir: t.TempDir()}, Exec: ex}
	if _, err := r.RunOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"pg_dump", "-Fc", "-h", "shop-postgres", "-U", "bunker", "app"},
		{"pg_dump", "-Fc", "-h", "blog-db", "-p", "5433", "-U", "bunker", "app"},
		{"tar", "-C", "/srv/my files", "-cf", "-", "$HOME"},
	}
	if len(ex.calls) != len(want) {
		t.Fatalf("calls = %q, want %q", ex.calls, want)
	}
	for i := range want {
		if strings.Join(ex.calls[i], "\x00") != strings.Join(want[i], "\x00") {
			t.Errorf("call %d = %q, want %q", i, ex.calls[i], want[i])
		}
	}
}

type recordingExec struct{ calls [][]string }

func (r *recordingExec) Exec(ctx context.Context, argv []string) (execx.Process, error) {
	r.calls = append(r.calls, argv)
	return execx.Local{}.Exec(ctx, argv)
}

func TestRunOnceFailureLeavesNoSnapshotAndRecordsStatus(t *testing.T) {
	fakeTools(t, "app")
	cfg, _ := setup(t)
	store := &backup.LocalStore{Dir: t.TempDir()}
	status := &memStatus{}
	var log bytes.Buffer

	r := &agent.Runner{Cfg: cfg, Store: store, Status: status, Exec: execx.Local{}, Log: &log}
	_, err := r.RunOnce(t.Context())
	if err == nil || !strings.Contains(err.Error(), "connection to server failed") {
		t.Fatalf("err = %v, want pg_dump's stderr", err)
	}
	snaps, err := store.List(t.Context(), "web-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 0 {
		t.Errorf("partial snapshot left behind: %+v", snaps)
	}
	got := status.get(t, "web-01")
	if got.OK || got.Snapshot != "" || !strings.Contains(got.Error, "connection to server failed") {
		t.Errorf("status = %+v, want a failed run with the error", got)
	}
	if !strings.Contains(log.String(), "backup FAILED") {
		t.Errorf("log missing failure line:\n%s", log.String())
	}
}

func TestRunOnceCancelledRecordsStatus(t *testing.T) {
	fakeTools(t, "")
	cfg, _ := setup(t)
	status := &memStatus{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	r := &agent.Runner{Cfg: cfg, Store: &backup.LocalStore{Dir: t.TempDir()}, Status: status, Exec: execx.Local{}}
	if _, err := r.RunOnce(ctx); err == nil {
		t.Fatal("want an error for a cancelled run")
	}
	if got := status.get(t, "web-01"); got.OK || got.Error == "" {
		t.Errorf("status = %+v, want a failed run even after cancel", got)
	}
}
