package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kushal1o1/bunker/internal/agent"
	"github.com/kushal1o1/bunker/internal/backup"
	"github.com/kushal1o1/bunker/internal/config"
)

// agentSetup is a config with one pull server ("web", local snapshots from
// setupRetention) and one agent server ("web-01", snapshots only in S3).
func agentSetup(t *testing.T) (cfgPath string, remote *memStore) {
	t.Helper()
	cfgPath, storage := setupRetention(t)
	appendS3Block(t, cfgPath)
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := strings.Replace(string(data), "servers:\n", "servers:\n  web-01:\n    agent: true\n", 1)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	remote = newMemStore()
	withBothStores(t, storage, remote)

	id := backup.NewSnapshotID(time.Now().Add(-6 * time.Hour))
	man := &backup.Manifest{
		Server: "web-01", Snapshot: id, Recipient: "age1test",
		Artifacts: []backup.ArtifactEntry{{ID: "postgres/shop-postgres/app", File: "postgres/shop-postgres/app.dump.zst.age", Size: 10, SHA256: "abc"}},
	}
	if err := remote.PutManifest(context.Background(), man); err != nil {
		t.Fatal(err)
	}
	return cfgPath, remote
}

func putAgentStatus(t *testing.T, remote *memStore, st agent.Status) {
	t.Helper()
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.PutServerFile(context.Background(), st.Agent, backup.StatusName, data); err != nil {
		t.Fatal(err)
	}
}

func TestStatusHealthy(t *testing.T) {
	cfgPath, remote := agentSetup(t)
	putAgentStatus(t, remote, agent.Status{Agent: "web-01", OK: true, Finished: time.Now().Add(-6 * time.Hour)})
	if err := ExecuteContext(context.Background(), []string{"-c", cfgPath, "status"}); err != nil {
		t.Fatalf("status: %v", err)
	}
}

func TestStatusAgentFailed(t *testing.T) {
	cfgPath, remote := agentSetup(t)
	putAgentStatus(t, remote, agent.Status{Agent: "web-01", OK: false, Error: "pg_dump exit 1: boom", Finished: time.Now()})
	err := ExecuteContext(context.Background(), []string{"-c", cfgPath, "status"})
	if err == nil || !strings.Contains(err.Error(), "1 server(s) need attention") {
		t.Fatalf("err = %v, want one server needing attention", err)
	}
}

func TestStatusNoSnapshot(t *testing.T) {
	cfgPath, _ := agentSetup(t)
	// web-01's agent has not reported yet (fine), but a server with no
	// complete snapshot at all needs attention.
	dir := filepath.Dir(cfgPath)
	if err := os.RemoveAll(filepath.Join(dir, "store", "web")); err != nil {
		t.Fatal(err)
	}
	err := ExecuteContext(context.Background(), []string{"-c", cfgPath, "status", "web"})
	if err == nil || !strings.Contains(err.Error(), "need attention") {
		t.Fatalf("err = %v, want attention for a server with no snapshot", err)
	}
	if err := ExecuteContext(context.Background(), []string{"-c", cfgPath, "status", "web-01"}); err != nil {
		t.Fatalf("agent without a report yet but with a snapshot: %v", err)
	}
}

func TestAgentLastRun(t *testing.T) {
	remote := newMemStore()
	now := time.Now()
	if got, failed := agentLastRun(context.Background(), remote, "web-01", now); got != "no report yet" || failed {
		t.Errorf("no status = %q, %v", got, failed)
	}
	putAgentStatus(t, remote, agent.Status{Agent: "web-01", OK: true, Finished: now.Add(-3 * time.Hour)})
	if got, failed := agentLastRun(context.Background(), remote, "web-01", now); got != "ok 3h ago" || failed {
		t.Errorf("ok status = %q, %v", got, failed)
	}
	putAgentStatus(t, remote, agent.Status{Agent: "web-01", Error: "pg_dump exit 1:\n  password authentication failed", Finished: now.Add(-90 * time.Second)})
	if got, failed := agentLastRun(context.Background(), remote, "web-01", now); got != "FAILED 1m ago: pg_dump exit 1: password authentication failed" || !failed {
		t.Errorf("failed status = %q, %v", got, failed)
	}
}

func TestHumanAge(t *testing.T) {
	cases := map[time.Duration]string{
		-time.Second:                  "0s",
		45 * time.Second:              "45s",
		12 * time.Minute:              "12m",
		6 * time.Hour:                 "6h",
		48 * time.Hour:                "2d",
		52*time.Hour + 30*time.Minute: "2d4h",
	}
	for d, want := range cases {
		if got := humanAge(d); got != want {
			t.Errorf("humanAge(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestBackupRunSkipsAgentServers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := backup.GenerateIdentity(filepath.Join(home, ".config", "bunker", "key.txt")); err != nil {
		t.Fatal(err)
	}
	cfgPath, _ := agentSetup(t)
	err := ExecuteContext(context.Background(), []string{"-c", cfgPath, "backup", "run", "web-01"})
	if err == nil || !strings.Contains(err.Error(), "has agent: true") {
		t.Fatalf("run web-01: err = %v, want an agent error", err)
	}
	// --all plans only the pull server, so it does not trip over web-01.
	if err := ExecuteContext(context.Background(), []string{"-c", cfgPath, "--no-hints", "backup", "run", "--all", "--dry-run"}); err != nil {
		t.Fatalf("run --all --dry-run: %v", err)
	}
}

// An agent's snapshot exists only in S3: verify must look there without
// --remote. The test key does not match the manifest, so it fails with NO
// KEY, which proves the manifest was found in S3, not "no such snapshot"
// in the local folder.
func TestVerifyAgentServerReadsS3(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := backup.GenerateIdentity(filepath.Join(home, ".config", "bunker", "key.txt")); err != nil {
		t.Fatal(err)
	}
	cfgPath, remote := agentSetup(t)
	snaps, err := remote.List(context.Background(), "web-01")
	if err != nil || len(snaps) != 1 {
		t.Fatalf("setup: %v %+v", err, snaps)
	}

	err = ExecuteContext(context.Background(), []string{"-c", cfgPath, "backup", "verify", "web-01", snaps[0].ID})
	if err == nil {
		t.Fatal("verify passed with a mismatched key")
	}
	if strings.Contains(err.Error(), "no such snapshot") {
		t.Fatalf("verify looked in local storage: %v", err)
	}
}

// failingList fails to list one server's snapshots.
type failingList struct {
	*memStore
	server string
}

func (f failingList) List(ctx context.Context, server string) ([]backup.SnapshotInfo, error) {
	if server == f.server {
		return nil, errors.New("access denied")
	}
	return f.memStore.List(ctx, server)
}

func TestStatusOneServerFailingDoesNotHideOthers(t *testing.T) {
	cfgPath, remote := agentSetup(t)
	storage := filepath.Join(filepath.Dir(cfgPath), "store")
	prev := storesFromConfig
	storesFromConfig = func(*config.Config) (*backup.Stores, error) {
		return &backup.Stores{Local: &backup.LocalStore{Dir: storage}, Remote: failingList{remote, "web-01"}}, nil
	}
	t.Cleanup(func() { storesFromConfig = prev })
	prevTTY := isTerminal
	isTerminal = func() bool { return true }
	t.Cleanup(func() { isTerminal = prevTTY })

	var out, errOut bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"-c", cfgPath, "status"})
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "1 server(s) need attention") {
		t.Fatalf("err = %v, want one server needing attention", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("want header and 2 rows:\n%s", out.String())
	}
	if !strings.HasPrefix(lines[0], "SERVER") || !strings.HasPrefix(lines[1], "web ") || !strings.HasPrefix(lines[2], "web-01 ") {
		t.Errorf("rows not in config order:\n%s", out.String())
	}
	if !strings.Contains(lines[1], "pull") || strings.Contains(lines[1], "none") {
		t.Errorf("web should show its snapshot:\n%s", lines[1])
	}
	if !strings.Contains(lines[2], "cannot list snapshots: access denied") {
		t.Errorf("web-01 should show the error:\n%s", lines[2])
	}
	if !strings.Contains(errOut.String(), "checking 0/2 servers...") || strings.Contains(out.String(), "checking") {
		t.Errorf("progress belongs on stderr only:\nstdout:\n%s\nstderr:\n%q", out.String(), errOut.String())
	}
}
