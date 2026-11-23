package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kushal1o1/bunker/internal/backup"
	"github.com/kushal1o1/bunker/internal/config"
)

// memStore is an in-memory backup.Store standing in for S3, so the CLI's
// local+S3 logic is tested without a bucket.
type memStore struct {
	mu          sync.Mutex
	files       map[string][]byte // server/snapshot/file
	serverFiles map[string][]byte // server/status.json
}

func newMemStore() *memStore {
	return &memStore{files: map[string][]byte{}, serverFiles: map[string][]byte{}}
}

func (m *memStore) PutServerFile(_ context.Context, server, name string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.serverFiles[server+"/"+name] = data
	return nil
}

func (m *memStore) GetServerFile(_ context.Context, server, name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.serverFiles[server+"/"+name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return data, nil
}

func (m *memStore) Name() string  { return "s3" }
func (m *memStore) Where() string { return "s3://mem/" }

func (m *memStore) Create(_ context.Context, server, snapshot, file string) (backup.ArtifactWriter, error) {
	return &memWriter{m: m, key: server + "/" + snapshot + "/" + file}, nil
}

type memWriter struct {
	m   *memStore
	key string
	buf bytes.Buffer
}

func (w *memWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }
func (w *memWriter) Abort()                      {}
func (w *memWriter) Commit() error {
	w.m.mu.Lock()
	defer w.m.mu.Unlock()
	w.m.files[w.key] = w.buf.Bytes()
	return nil
}

func (m *memStore) PutManifest(ctx context.Context, man *backup.Manifest) error {
	data, err := json.Marshal(man)
	if err != nil {
		return err
	}
	w, _ := m.Create(ctx, man.Server, man.Snapshot, backup.ManifestName)
	w.Write(data)
	return w.Commit()
}

func (m *memStore) get(key string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.files[key]
	return b, ok
}

func (m *memStore) Manifest(_ context.Context, server, snapshot string) (*backup.Manifest, error) {
	data, ok := m.get(server + "/" + snapshot + "/" + backup.ManifestName)
	if !ok {
		return nil, fs.ErrNotExist
	}
	return backup.ParseManifest(data, "mem")
}

func (m *memStore) Open(_ context.Context, server, snapshot, file string) (io.ReadCloser, error) {
	data, ok := m.get(server + "/" + snapshot + "/" + file)
	if !ok {
		return nil, fs.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *memStore) List(ctx context.Context, server string) ([]backup.SnapshotInfo, error) {
	m.mu.Lock()
	ids := map[string]bool{}
	for k := range m.files {
		if parts := strings.SplitN(k, "/", 3); parts[0] == server {
			ids[parts[1]] = true
		}
	}
	m.mu.Unlock()
	var out []backup.SnapshotInfo
	for id := range ids {
		created, _ := time.Parse("2006-01-02T15-04-05", id)
		info := backup.SnapshotInfo{ID: id, Created: created, Status: backup.NoManifest}
		if man, err := m.Manifest(ctx, server, id); err == nil {
			info.Status, info.Manifest = backup.Complete, man
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

func (m *memStore) Delete(_ context.Context, server, snapshot string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.files {
		if strings.HasPrefix(k, server+"/"+snapshot+"/") {
			delete(m.files, k)
		}
	}
	return nil
}

// withBothStores makes the config use its local folder plus remote, and
// points openStores at them.
func withBothStores(t *testing.T, storage string, remote *memStore) {
	t.Helper()
	prev := storesFromConfig
	storesFromConfig = func(*config.Config) (*backup.Stores, error) {
		return &backup.Stores{Local: &backup.LocalStore{Dir: storage}, Remote: remote}, nil
	}
	t.Cleanup(func() { storesFromConfig = prev })
}

// writeLocalArtifacts gives setupRetention's snapshots real artifact files,
// so they can be copied.
func writeLocalArtifacts(t *testing.T, storage string) {
	t.Helper()
	snaps, err := backup.ListSnapshots(storage, "web")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range snaps {
		if s.Status != backup.Complete {
			continue
		}
		p := filepath.Join(backup.SnapshotDir(storage, "web", s.ID), s.Manifest.Artifacts[0].File)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, bytes.Repeat([]byte("x"), 10), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func remoteIDs(t *testing.T, m *memStore) []string {
	t.Helper()
	snaps, _ := m.List(context.Background(), "web")
	var ids []string
	for _, s := range snaps {
		if s.Status == backup.Complete {
			ids = append(ids, s.ID)
		}
	}
	return ids
}

func TestStorageSyncUploadsMissingSnapshots(t *testing.T) {
	cfgPath, storage := setupRetention(t)
	writeLocalArtifacts(t, storage)
	remote := newMemStore()
	withBothStores(t, storage, remote)
	appendS3Block(t, cfgPath)

	if err := ExecuteContext(context.Background(), []string{"-c", cfgPath, "storage", "sync"}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	want := []string{"2026-10-17T18-00-00", "2026-10-17T06-00-00", "2026-10-16T09-00-00", "2026-10-15T09-00-00"}
	if got := remoteIDs(t, remote); !reflect.DeepEqual(got, want) {
		t.Fatalf("remote after sync = %v, want %v (the folder without a manifest is skipped)", got, want)
	}
	if _, ok := remote.get("web/2026-10-17T18-00-00/files/srv-data.tar.zst.age"); !ok {
		t.Error("artifact was not copied")
	}

	// A second sync has nothing to do.
	if err := ExecuteContext(context.Background(), []string{"-c", cfgPath, "storage", "sync"}); err != nil {
		t.Fatalf("second sync: %v", err)
	}
}

func TestStorageSyncNeedsBothStores(t *testing.T) {
	cfgPath, _ := setupRetention(t)
	err := ExecuteContext(context.Background(), []string{"-c", cfgPath, "storage", "sync"})
	if err == nil || !strings.Contains(err.Error(), "storage.dir and storage.s3") {
		t.Fatalf("sync without s3 = %v", err)
	}
}

// With both stores, retention deletes a snapshot from local and S3, and a
// snapshot that exists only in S3 still takes part in the policy.
func TestRetentionDeletesFromBothStores(t *testing.T) {
	cfgPath, storage := setupRetention(t)
	writeLocalArtifacts(t, storage)
	remote := newMemStore()
	withBothStores(t, storage, remote)
	appendS3Block(t, cfgPath)

	if err := ExecuteContext(context.Background(), []string{"-c", cfgPath, "storage", "sync"}); err != nil {
		t.Fatal(err)
	}
	// The oldest snapshot now exists only in S3, as if its local copy had
	// been removed by hand. It must still be pruned.
	if err := backup.DeleteSnapshot(storage, "web", "2026-10-15T09-00-00"); err != nil {
		t.Fatal(err)
	}

	if err := ExecuteContext(context.Background(), []string{"-c", cfgPath, "retention", "apply", "--yes"}); err != nil {
		t.Fatalf("retention: %v", err)
	}
	// daily 1 / weekly 1 / monthly 1 over one week and month keeps only the newest.
	if got := remoteIDs(t, remote); !reflect.DeepEqual(got, []string{"2026-10-17T18-00-00"}) {
		t.Errorf("remote after retention = %v", got)
	}
	// The old folder without a manifest is an abandoned run and goes too.
	if got := snapshotIDs(t, storage); !reflect.DeepEqual(got, []string{"2026-10-17T18-00-00"}) {
		t.Errorf("local after retention = %v", got)
	}
}

func TestListMergedMarksWhereEachCopyIs(t *testing.T) {
	_, storage := setupRetention(t)
	writeLocalArtifacts(t, storage)
	remote := newMemStore()
	ctx := context.Background()
	stores := &backup.Stores{Local: &backup.LocalStore{Dir: storage}, Remote: remote}

	snaps, _ := backup.ListSnapshots(storage, "web")
	if err := backup.CopySnapshot(ctx, stores.Local, remote, snaps[0], "web", nil); err != nil {
		t.Fatal(err)
	}
	rows, err := listMerged(ctx, stores, "web")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r.id] = copyStatus(r.local) + "|" + copyStatus(r.remote)
	}
	want := map[string]string{
		"2026-10-17T18-00-00": "complete|complete",
		"2026-10-17T06-00-00": "complete|-",
		"2026-10-16T09-00-00": "complete|-",
		"2026-10-15T09-00-00": "complete|-",
		"2026-10-01T09-00-00": "no manifest|-",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("listMerged = %v\nwant %v", got, want)
	}
}

// appendS3Block adds an s3 block under setupRetention's storage: section, so
// the config asks for both stores. The store itself is the in-memory fake.
func appendS3Block(t *testing.T, cfgPath string) {
	t.Helper()
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "storage:\n  dir:") {
		t.Fatal("setupRetention config changed shape")
	}
	cfg := strings.Replace(string(data), "storage:\n", "storage:\n  s3:\n    endpoint: https://s3.example.com\n    bucket: test\n", 1)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}
