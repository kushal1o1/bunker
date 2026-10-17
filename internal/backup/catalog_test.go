package backup

import (
	"os"
	"path/filepath"
	"testing"
)

// writeSnapshot creates <storage>/<server>/<id>/manifest.json with one
// artifact of the given size.
func writeSnapshot(t *testing.T, storage, server, id string, size int64) {
	t.Helper()
	dir := SnapshotDir(storage, server, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	m := &Manifest{
		Server:    server,
		Snapshot:  id,
		CreatedAt: "2026-10-17T09:12:03Z",
		Recipient: "age1test",
		Artifacts: []ArtifactEntry{{ID: "postgres/db", File: "postgres/db.dump.zst.age", Size: size, SHA256: "abc"}},
	}
	if err := WriteManifest(ManifestPath(dir), m); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteSnapshot(t *testing.T) {
	storage := t.TempDir()
	writeSnapshot(t, storage, "web", "2026-10-16T09-00-00", 1)
	writeSnapshot(t, storage, "web", "2026-10-17T09-00-00", 1)

	if err := DeleteSnapshot(storage, "web", "2026-10-16T09-00-00"); err != nil {
		t.Fatal(err)
	}
	got, err := ListSnapshots(storage, "web")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "2026-10-17T09-00-00" {
		t.Fatalf("after delete = %+v, want only 2026-10-17T09-00-00", got)
	}

	for _, tc := range []struct{ server, snap string }{
		{"web", ".."},
		{"web", ""},
		{"web", "../2026-10-17T09-00-00"},
		{"..", "2026-10-17T09-00-00"},
		{"a/b", "2026-10-17T09-00-00"},
		{"", "2026-10-17T09-00-00"},
	} {
		if err := DeleteSnapshot(storage, tc.server, tc.snap); err == nil {
			t.Errorf("DeleteSnapshot(%q, %q) = nil, want refusal", tc.server, tc.snap)
		}
	}
	if _, err := os.Stat(SnapshotDir(storage, "web", "2026-10-17T09-00-00")); err != nil {
		t.Fatalf("remaining snapshot was touched: %v", err)
	}
}

func TestListSnapshotsMissingServerDir(t *testing.T) {
	got, err := ListSnapshots(t.TempDir(), "nope")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d snapshots, want 0", len(got))
	}
}

func TestListSnapshotsNewestFirst(t *testing.T) {
	storage := t.TempDir()
	writeSnapshot(t, storage, "web", "2026-10-15T09-00-00", 10)
	writeSnapshot(t, storage, "web", "2026-10-17T09-00-00", 30)
	writeSnapshot(t, storage, "web", "2026-10-16T09-00-00", 20)

	got, err := ListSnapshots(storage, "web")
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{"2026-10-17T09-00-00", "2026-10-16T09-00-00", "2026-10-15T09-00-00"}
	wantSizes := []int64{30, 20, 10}
	if len(got) != len(wantIDs) {
		t.Fatalf("got %d snapshots, want %d", len(got), len(wantIDs))
	}
	for i, s := range got {
		if s.ID != wantIDs[i] || s.Status != Complete || s.Size() != wantSizes[i] {
			t.Errorf("[%d] = %s %s size %d, want %s complete size %d", i, s.ID, s.Status, s.Size(), wantIDs[i], wantSizes[i])
		}
		if s.Created.Format(snapshotLayout) != s.ID {
			t.Errorf("[%d] Created = %v, does not match ID %s", i, s.Created, s.ID)
		}
	}
}

func TestListSnapshotsStatuses(t *testing.T) {
	storage := t.TempDir()
	serverDir := filepath.Join(storage, "web")

	writeSnapshot(t, storage, "web", "2026-10-17T09-00-00", 1)

	// Killed mid-run: directory but no manifest.
	if err := os.MkdirAll(filepath.Join(serverDir, "2026-10-16T09-00-00"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Corrupt manifest.
	bad := filepath.Join(serverDir, "2026-10-15T09-00-00")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ManifestPath(bad), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Manifest that parses but records no artifacts.
	empty := filepath.Join(serverDir, "2026-10-14T09-00-00")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteManifest(ManifestPath(empty), &Manifest{Server: "web", Snapshot: "x", Recipient: "age1test"}); err != nil {
		t.Fatal(err)
	}
	// Noise that must be ignored.
	if err := os.MkdirAll(filepath.Join(serverDir, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serverDir, "2026-10-13T09-00-00"), []byte("a file"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ListSnapshots(storage, "web")
	if err != nil {
		t.Fatal(err)
	}
	want := []SnapshotStatus{Complete, NoManifest, BadManifest, BadManifest}
	if len(got) != len(want) {
		t.Fatalf("got %d snapshots, want %d: %+v", len(got), len(want), got)
	}
	for i, s := range got {
		if s.Status != want[i] {
			t.Errorf("[%d] %s status = %s, want %s", i, s.ID, s.Status, want[i])
		}
		if s.Status == BadManifest && s.Err == nil {
			t.Errorf("[%d] %s: BadManifest without Err", i, s.ID)
		}
		if s.Status != Complete && s.Size() != 0 {
			t.Errorf("[%d] %s: size %d, want 0", i, s.ID, s.Size())
		}
	}
}
