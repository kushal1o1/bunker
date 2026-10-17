package cli

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kushal1o1/bunker/internal/backup"
)

// setupRetention writes a config with policy daily 1 (weekly and monthly 1
// too, since 0 means "use the default") and four snapshots on two days in
// one week and month, plus one folder without a manifest.
func setupRetention(t *testing.T) (cfgPath, storage string) {
	t.Helper()
	dir := t.TempDir()
	storage = filepath.Join(dir, "store")
	cfgPath = filepath.Join(dir, "bunker.yaml")
	cfg := `servers:
  web:
    host: 127.0.0.1
    backup:
      paths: [/srv/data]
storage:
  dir: ` + storage + `
retention:
  daily: 1
  weekly: 1
  monthly: 1
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"2026-10-17T18-00-00", "2026-10-17T06-00-00", "2026-10-16T09-00-00", "2026-10-15T09-00-00"} {
		d := backup.SnapshotDir(storage, "web", id)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		m := &backup.Manifest{
			Server: "web", Snapshot: id, Recipient: "age1test",
			Artifacts: []backup.ArtifactEntry{{ID: "files/srv/data", File: "files/srv-data.tar.zst.age", Size: 10, SHA256: "abc"}},
		}
		if err := backup.WriteManifest(backup.ManifestPath(d), m); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(backup.SnapshotDir(storage, "web", "2026-10-01T09-00-00"), 0o755); err != nil {
		t.Fatal(err)
	}
	return cfgPath, storage
}

func snapshotIDs(t *testing.T, storage string) []string {
	t.Helper()
	snaps, err := backup.ListSnapshots(storage, "web")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range snaps {
		ids = append(ids, s.ID)
	}
	return ids
}

func TestRetentionApplyPreviewDeletesNothing(t *testing.T) {
	cfgPath, storage := setupRetention(t)
	before := snapshotIDs(t, storage)

	if err := ExecuteContext(context.Background(), []string{"-c", cfgPath, "retention", "apply"}); err != nil {
		t.Fatal(err)
	}
	if after := snapshotIDs(t, storage); !reflect.DeepEqual(after, before) {
		t.Fatalf("preview changed storage: %v -> %v", before, after)
	}
}

func TestRetentionApplyYesDeletesOnlyPruned(t *testing.T) {
	cfgPath, storage := setupRetention(t)

	if err := ExecuteContext(context.Background(), []string{"-c", cfgPath, "retention", "apply", "--yes"}); err != nil {
		t.Fatal(err)
	}
	// Kept: the newest snapshot (daily 1, weekly 1, monthly 1). The folder
	// without a manifest is skipped, never deleted.
	want := []string{"2026-10-17T18-00-00", "2026-10-01T09-00-00"}
	if got := snapshotIDs(t, storage); !reflect.DeepEqual(got, want) {
		t.Fatalf("after --yes = %v, want %v", got, want)
	}
}

func TestRetentionApplyUnknownServer(t *testing.T) {
	cfgPath, _ := setupRetention(t)
	if err := ExecuteContext(context.Background(), []string{"-c", cfgPath, "retention", "apply", "nope"}); err == nil {
		t.Fatal("want error for unknown server")
	}
}
