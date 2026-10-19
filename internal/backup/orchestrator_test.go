package backup_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kushal1o1/bunker/internal/backup"
)

// writeMultiConfig writes a config with one server per name, all pointing at
// the same fake server and backing up one path each. Servers listed in
// badHosts get an empty known_hosts file, so their host key check fails.
func writeMultiConfig(t *testing.T, fs *fakeServer, names []string, badHosts map[string]bool, concurrency int) (cfgPath, storageDir string) {
	t.Helper()
	keyPath, khPath := writeAuthKeyFiles(t, fs)
	emptyKH := filepath.Join(t.TempDir(), "empty_known_hosts")
	if err := os.WriteFile(emptyKH, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	identity := writeIdentity(t)
	storageDir = filepath.Join(t.TempDir(), "backups")
	_, port, err := net.SplitHostPort(fs.addr)
	if err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	b.WriteString("servers:\n")
	for _, n := range names {
		kh := khPath
		if badHosts[n] {
			kh = emptyKH
		}
		fmt.Fprintf(&b, `  %s:
    host: 127.0.0.1
    ssh:
      user: backup-svc
      port: %s
      key: %s
      known_hosts: %s
    backup:
      paths: [/srv/%s]
`, n, port, keyPath, kh, n)
	}
	fmt.Fprintf(&b, "storage:\n  dir: %s\nencryption:\n  identity: %s\nconcurrency: %d\n", storageDir, identity, concurrency)

	cfgPath = filepath.Join(t.TempDir(), "bunker.yaml")
	if err := os.WriteFile(cfgPath, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath, storageDir
}

func TestRunManyRespectsConcurrencyAndKeepsOrder(t *testing.T) {
	var inFlight, maxInFlight int32
	fs := startFakeServer(t, func(cmd string) ([]byte, int) {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			m := atomic.LoadInt32(&maxInFlight)
			if n <= m || atomic.CompareAndSwapInt32(&maxInFlight, m, n) {
				break
			}
		}
		time.Sleep(150 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		return []byte("TAR" + cmd), 0
	})

	names := []string{"s1", "s2", "s3", "s4"}
	cfgPath, _ := writeMultiConfig(t, fs, names, nil, 2)
	cfg := loadTestConfig(t, cfgPath)

	var mu sync.Mutex
	done := map[string]int{}
	r := &backup.Runner{Cfg: cfg, Progress: func(e backup.Event) {
		if e.Kind == backup.ArtifactDone {
			mu.Lock()
			done[e.Server]++
			mu.Unlock()
		}
	}}

	results := backup.RunMany(context.Background(), r, names, cfg.Concurrency, nil)

	if got := atomic.LoadInt32(&maxInFlight); got != 2 {
		t.Errorf("max servers in flight = %d, want 2", got)
	}
	if len(results) != len(names) {
		t.Fatalf("got %d results, want %d", len(results), len(names))
	}
	for i, res := range results {
		if res.Server != names[i] {
			t.Errorf("results[%d].Server = %s, want %s (input order)", i, res.Server, names[i])
		}
		if res.Err != nil || res.Result == nil {
			t.Errorf("%s: err=%v result=%v, want success", res.Server, res.Err, res.Result)
		}
		if done[res.Server] != 1 {
			t.Errorf("%s: %d ArtifactDone events, want 1", res.Server, done[res.Server])
		}
	}
}

func TestRunManyOneFailureDoesNotStopOthers(t *testing.T) {
	fs := startFakeServer(t, func(cmd string) ([]byte, int) { return []byte("TAR"), 0 })

	names := []string{"good1", "bad", "good2"}
	cfgPath, storageDir := writeMultiConfig(t, fs, names, map[string]bool{"bad": true}, 3)
	cfg := loadTestConfig(t, cfgPath)

	var calls int32
	results := backup.RunMany(context.Background(), &backup.Runner{Cfg: cfg}, names, cfg.Concurrency,
		func(backup.ServerResult) { atomic.AddInt32(&calls, 1) })

	if calls != 3 {
		t.Errorf("onDone called %d times, want 3", calls)
	}
	for _, res := range results {
		switch res.Server {
		case "bad":
			if res.Err == nil {
				t.Error("bad: want host key error, got nil")
			}
		default:
			if res.Err != nil {
				t.Errorf("%s: %v, want success", res.Server, res.Err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(storageDir, "bad")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("bad: storage folder should not exist, stat err = %v", err)
	}
}

func TestRunManyCancelledBeforeStart(t *testing.T) {
	fs := startFakeServer(t, func(cmd string) ([]byte, int) { return []byte("TAR"), 0 })
	names := []string{"s1", "s2"}
	cfgPath, _ := writeMultiConfig(t, fs, names, nil, 1)
	cfg := loadTestConfig(t, cfgPath)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, res := range backup.RunMany(ctx, &backup.Runner{Cfg: cfg}, names, 1, nil) {
		if !errors.Is(res.Err, context.Canceled) {
			t.Errorf("%s: err = %v, want context.Canceled", res.Server, res.Err)
		}
	}
}
