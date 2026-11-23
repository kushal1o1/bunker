package backup_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
	"github.com/kushal1o1/bunker/internal/backup"
	"github.com/kushal1o1/bunker/internal/config"
	"github.com/kushal1o1/bunker/internal/model"
)

const testBucket = "bunker-test"

// fakeS3 is an in-process S3 with one empty bucket. It runs over TLS because
// over plain http minio-go sends multipart parts in AWS's chunked signing
// format, which real S3 and MinIO decode but gofakes3 stores raw.
type fakeS3 struct {
	url       string
	transport http.RoundTripper
}

// startFakeS3 also sets fake credentials, so the S3 store is exercised end
// to end without a network.
func startFakeS3(t *testing.T) *fakeS3 {
	t.Helper()
	mem := s3mem.New()
	if err := mem.CreateBucket(testBucket); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(gofakes3.New(mem).Server())
	t.Cleanup(srv.Close)
	t.Setenv(backup.EnvAccessKey, "test-access")
	t.Setenv(backup.EnvSecretKey, "test-secret")
	return &fakeS3{url: srv.URL, transport: srv.Client().Transport}
}

// store opens the bucket configured in cfg through the fake's transport.
func (f *fakeS3) store(t *testing.T, cfg *config.Config) *backup.S3Store {
	t.Helper()
	st, err := backup.NewS3StoreForTest(*cfg.Storage.S3, f.transport)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// stores is what the CLI would build from cfg: local (if set) plus S3.
func (f *fakeS3) stores(t *testing.T, cfg *config.Config) *backup.Stores {
	t.Helper()
	st := &backup.Stores{Remote: f.store(t, cfg)}
	if cfg.Storage.Dir != "" {
		st.Local = &backup.LocalStore{Dir: cfg.Storage.Dir}
	}
	return st
}

func (f *fakeS3) runner(t *testing.T, cfg *config.Config) *backup.Runner {
	return &backup.Runner{Cfg: cfg, Stores: f.stores(t, cfg)}
}

func (f *fakeS3) verifier(t *testing.T, cfg *config.Config) *backup.Verifier {
	return &backup.Verifier{Cfg: cfg, Store: f.store(t, cfg)}
}

// s3Config loads the standard test config and points storage at the fake
// bucket. withLocal keeps the local folder too ("both" mode).
func s3Config(t *testing.T, responder func(cmd string) ([]byte, int), f *fakeS3, withLocal bool) *config.Config {
	t.Helper()
	fs := startFakeServer(t, responder)
	keyPath, khPath := writeAuthKeyFiles(t, fs)
	storageDir := filepath.Join(t.TempDir(), "backups")
	cfg := loadTestConfig(t, writeConfig(t, fs, keyPath, khPath, writeIdentity(t), storageDir, ""))
	if !withLocal {
		cfg.Storage.Dir = ""
	}
	cfg.Storage.S3 = &model.S3Spec{Endpoint: f.url, Bucket: testBucket, Prefix: "prod/"}
	return cfg
}

func TestS3OnlyRunListVerifyDelete(t *testing.T) {
	f := startFakeS3(t)
	cfg := s3Config(t, stdResponder, f, false)
	ctx := context.Background()

	res, err := f.runner(t, cfg).Run(ctx, "vps-01")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Dir != "" {
		t.Errorf("S3-only run reported local dir %q", res.Dir)
	}

	st := f.store(t, cfg)
	snaps, err := st.List(ctx, "vps-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 || snaps[0].ID != res.Snapshot || snaps[0].Status != backup.Complete {
		t.Fatalf("List = %+v, want one complete %s", snaps, res.Snapshot)
	}
	if snaps[0].Size() != totalOf(res.Artifacts) {
		t.Errorf("listed size %d, want %d", snaps[0].Size(), totalOf(res.Artifacts))
	}

	vres, err := f.verifier(t, cfg).Verify(ctx, "vps-01", res.Snapshot)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !vres.OK() {
		t.Fatalf("S3 snapshot did not verify: %+v", vres.Artifacts)
	}
	if !strings.HasPrefix(vres.Artifacts[0].Path, "s3://"+testBucket+"/prod/vps-01/") {
		t.Errorf("artifact path = %q, want an s3:// URL under the prefix", vres.Artifacts[0].Path)
	}

	if err := st.Delete(ctx, "vps-01", res.Snapshot); err != nil {
		t.Fatal(err)
	}
	if snaps, _ := st.List(ctx, "vps-01"); len(snaps) != 0 {
		t.Fatalf("after Delete, List = %+v", snaps)
	}
}

// A failed S3-only run must leave no objects: a half-uploaded snapshot has
// no manifest, but it would still cost storage and clutter list.
func TestS3OnlyFailedRunLeavesNothing(t *testing.T) {
	failing := func(cmd string) ([]byte, int) {
		if strings.Contains(cmd, "tar ") {
			return []byte("tar: cannot open"), 2
		}
		return stdResponder(cmd)
	}
	f := startFakeS3(t)
	cfg := s3Config(t, failing, f, false)
	ctx := context.Background()

	if _, err := f.runner(t, cfg).Run(ctx, "vps-01"); err == nil {
		t.Fatal("Run succeeded with a failing tar")
	}
	snaps, err := f.store(t, cfg).List(ctx, "vps-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 0 {
		t.Fatalf("failed run left %d snapshot(s) in S3: %+v", len(snaps), snaps)
	}
}

func TestBothWritesLocalThenUploads(t *testing.T) {
	f := startFakeS3(t)
	cfg := s3Config(t, stdResponder, f, true)
	ctx := context.Background()

	var uploads int
	r := f.runner(t, cfg)
	r.Progress = func(e backup.Event) {
		if e.Kind == backup.Uploading {
			uploads++
		}
	}
	res, err := r.Run(ctx, "vps-01")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.UploadErr != nil {
		t.Fatalf("UploadErr = %v", res.UploadErr)
	}
	if uploads != len(res.Artifacts) {
		t.Errorf("Uploading events = %d, want %d", uploads, len(res.Artifacts))
	}

	local, _ := backup.ListSnapshots(cfg.Storage.Dir, "vps-01")
	remote, _ := f.store(t, cfg).List(ctx, "vps-01")
	if len(local) != 1 || local[0].Status != backup.Complete {
		t.Fatalf("local = %+v", local)
	}
	if len(remote) != 1 || remote[0].Status != backup.Complete {
		t.Fatalf("remote = %+v", remote)
	}
	if remote[0].Manifest.Artifacts[0].SHA256 != local[0].Manifest.Artifacts[0].SHA256 {
		t.Error("S3 copy has a different checksum than the local snapshot")
	}

	// verify --remote: the S3 copy itself must decrypt.
	vres, err := f.verifier(t, cfg).Verify(ctx, "vps-01", res.Snapshot)
	if err != nil || !vres.OK() {
		t.Fatalf("remote verify: err=%v ok=%v", err, vres != nil && vres.OK())
	}
}

// When only the upload fails, the local snapshot is complete and kept, and
// the error is reported separately so the run can still exit non-zero.
func TestBothUploadFailureKeepsLocal(t *testing.T) {
	f := startFakeS3(t)
	cfg := s3Config(t, stdResponder, f, true)
	cfg.Storage.S3.Bucket = "no-such-bucket" // the runner opens stores from this

	res, err := f.runner(t, cfg).Run(context.Background(), "vps-01")
	if err != nil {
		t.Fatalf("Run: %v (an upload failure must not fail the local snapshot)", err)
	}
	if res.UploadErr == nil {
		t.Fatal("UploadErr = nil, want the bucket error")
	}
	local, _ := backup.ListSnapshots(cfg.Storage.Dir, "vps-01")
	if len(local) != 1 || local[0].Status != backup.Complete {
		t.Fatalf("local snapshot not kept after upload failure: %+v", local)
	}
}

func TestVerifyS3DetectsCorruption(t *testing.T) {
	f := startFakeS3(t)
	cfg := s3Config(t, stdResponder, f, false)
	ctx := context.Background()
	res, err := f.runner(t, cfg).Run(ctx, "vps-01")
	if err != nil {
		t.Fatal(err)
	}

	// Flip one byte of the first artifact in the bucket.
	st := f.store(t, cfg)
	file := res.Artifacts[0].File
	r, err := st.Open(ctx, "vps-01", res.Snapshot, file)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(r)
	r.Close()
	data[len(data)/2] ^= 0xff
	w, err := st.Create(ctx, "vps-01", res.Snapshot, file)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(data)
	if err := w.Commit(); err != nil {
		t.Fatal(err)
	}

	vres, err := f.verifier(t, cfg).Verify(ctx, "vps-01", res.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if vres.OK() {
		t.Fatal("corrupted S3 artifact verified OK")
	}
	if a := vres.Artifacts[0]; !a.SizeOK || a.SumOK {
		t.Errorf("size=%v sha256=%v, want size ok and sha256 failed", a.SizeOK, a.SumOK)
	}
}

func TestVerifyS3MissingArtifact(t *testing.T) {
	f := startFakeS3(t)
	cfg := s3Config(t, stdResponder, f, false)
	ctx := context.Background()
	res, err := f.runner(t, cfg).Run(ctx, "vps-01")
	if err != nil {
		t.Fatal(err)
	}
	st := f.store(t, cfg)
	if _, err := st.Open(ctx, "vps-01", res.Snapshot, "nope.age"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open missing = %v, want os.ErrNotExist", err)
	}
	if _, err := f.verifier(t, cfg).Verify(ctx, "vps-01", "2020-01-01T00-00-00"); err == nil {
		t.Fatal("verify of a missing S3 snapshot succeeded")
	}
}

// CopySnapshot is what storage sync uses: a snapshot missing from S3 is
// copied from local storage, manifest last.
func TestCopySnapshotToS3(t *testing.T) {
	f := startFakeS3(t)
	cfg := s3Config(t, stdResponder, f, true)
	ctx := context.Background()
	res, err := f.runner(t, cfg).Run(ctx, "vps-01")
	if err != nil {
		t.Fatal(err)
	}
	st := f.store(t, cfg)
	if err := st.Delete(ctx, "vps-01", res.Snapshot); err != nil {
		t.Fatal(err)
	}

	local, _ := backup.ListSnapshots(cfg.Storage.Dir, "vps-01")
	if err := backup.CopySnapshot(ctx, &backup.LocalStore{Dir: cfg.Storage.Dir}, st, local[0], "vps-01", nil); err != nil {
		t.Fatalf("CopySnapshot: %v", err)
	}
	remote, _ := st.List(ctx, "vps-01")
	if len(remote) != 1 || remote[0].Status != backup.Complete {
		t.Fatalf("after copy, remote = %+v", remote)
	}
}

func TestS3NeedsCredentialsAndBucket(t *testing.T) {
	f := startFakeS3(t)
	spec := model.S3Spec{Endpoint: f.url, Bucket: "missing"}

	st, err := backup.NewS3StoreForTest(spec, f.transport)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("Check on a missing bucket = %v", err)
	}

	t.Setenv(backup.EnvAccessKey, "")
	if _, err := backup.NewS3Store(spec); err == nil || !strings.Contains(err.Error(), backup.EnvAccessKey) {
		t.Errorf("NewS3Store without credentials = %v, want a message naming %s", err, backup.EnvAccessKey)
	}
}

func totalOf(arts []backup.ArtifactEntry) int64 {
	var n int64
	for _, a := range arts {
		n += a.Size
	}
	return n
}

// status.json and check.json sit next to the snapshot folders: list,
// verify and delete must not mistake them for snapshots, and only those two
// names may be written there.
func TestS3ServerFiles(t *testing.T) {
	f := startFakeS3(t)
	cfg := s3Config(t, stdResponder, f, false)
	ctx := context.Background()
	st := f.store(t, cfg)

	if _, err := st.GetServerFile(ctx, "vps-01", backup.StatusName); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing status: err = %v, want fs.ErrNotExist", err)
	}
	res, err := f.runner(t, cfg).Run(ctx, "vps-01")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{backup.StatusName, backup.CheckName} {
		if err := st.PutServerFile(ctx, "vps-01", name, []byte(`{"ok":true}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutServerFile(ctx, "vps-01", "evil.json", []byte("x")); err == nil {
		t.Error("PutServerFile accepted a name other than status.json / check.json")
	}
	if err := st.PutServerFile(ctx, "../x", backup.StatusName, []byte("x")); err == nil {
		t.Error("PutServerFile accepted a server name with a path")
	}

	got, err := st.GetServerFile(ctx, "vps-01", backup.StatusName)
	if err != nil || string(got) != `{"ok":true}` {
		t.Fatalf("GetServerFile = %q, %v", got, err)
	}
	snaps, err := st.List(ctx, "vps-01")
	if err != nil || len(snaps) != 1 || snaps[0].ID != res.Snapshot {
		t.Fatalf("List = %+v, %v; want only the snapshot", snaps, err)
	}
	if err := st.Delete(ctx, "vps-01", res.Snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetServerFile(ctx, "vps-01", backup.StatusName); err != nil {
		t.Errorf("deleting a snapshot removed status.json: %v", err)
	}
}
