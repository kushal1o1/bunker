package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/kushal1o1/bunker/internal/config"
)

// Store is where snapshots live: a local folder or an S3 bucket. Both lay a
// snapshot out the same way, <server>/<snapshot>/<file>, with manifest.json
// written last as the completeness marker.
type Store interface {
	// Name is "local" or "s3", for messages and list columns.
	Name() string
	// Where describes the location for humans: a path or s3://bucket/prefix.
	Where() string

	// Create starts writing one artifact. Nothing is final until Commit.
	Create(ctx context.Context, server, snapshot, file string) (ArtifactWriter, error)
	// PutManifest writes manifest.json, which makes the snapshot complete.
	PutManifest(ctx context.Context, m *Manifest) error

	// Manifest reads and checks one snapshot's manifest. A missing manifest
	// wraps fs.ErrNotExist.
	Manifest(ctx context.Context, server, snapshot string) (*Manifest, error)
	// Open reads one artifact. A missing file wraps fs.ErrNotExist.
	Open(ctx context.Context, server, snapshot, file string) (io.ReadCloser, error)
	// List returns one server's snapshots, newest first.
	List(ctx context.Context, server string) ([]SnapshotInfo, error)
	// Delete removes a whole snapshot, complete or partial.
	Delete(ctx context.Context, server, snapshot string) error
}

// ArtifactWriter receives one artifact's encrypted bytes.
type ArtifactWriter interface {
	io.Writer
	// Commit finishes the artifact. It is stored once Commit returns nil.
	Commit() error
	// Abort discards whatever was written. Safe to call after a failed Commit.
	Abort()
}

// Stores is the configured storage: Local, Remote, or both. At least one is set.
type Stores struct {
	Local  Store
	Remote Store
}

// Primary is where backup run writes: local when there is a local folder,
// otherwise S3. With both, the remote copy is uploaded from local afterwards.
func (s *Stores) Primary() Store {
	if s.Local != nil {
		return s.Local
	}
	return s.Remote
}

// Both reports whether snapshots are kept locally and in S3.
func (s *Stores) Both() bool { return s.Local != nil && s.Remote != nil }

// OpenStores builds the stores from the config. Opening S3 reads credentials
// from the environment but does not touch the network.
func OpenStores(cfg *config.Config) (*Stores, error) {
	st := &Stores{}
	if cfg.Storage.Dir != "" {
		st.Local = &LocalStore{Dir: cfg.Storage.Dir}
	}
	if cfg.Storage.S3 != nil {
		s3, err := NewS3Store(*cfg.Storage.S3)
		if err != nil {
			return nil, err
		}
		st.Remote = s3
	}
	return st, nil
}

// --- local -----------------------------------------------------------------

// LocalStore keeps snapshots in a folder on this machine.
type LocalStore struct {
	Dir string
}

func (l *LocalStore) Name() string  { return "local" }
func (l *LocalStore) Where() string { return l.Dir }

func (l *LocalStore) Create(_ context.Context, server, snapshot, file string) (ArtifactWriter, error) {
	full := filepath.Join(SnapshotDir(l.Dir, server, snapshot), file)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return nil, fmt.Errorf("create dir: %w", err)
	}
	f, err := os.Create(full)
	if err != nil {
		return nil, fmt.Errorf("create file: %w", err)
	}
	return &localWriter{f: f}, nil
}

type localWriter struct {
	f    *os.File
	done bool
}

func (w *localWriter) Write(p []byte) (int, error) { return w.f.Write(p) }

func (w *localWriter) Commit() error {
	w.done = true
	if err := w.f.Close(); err != nil {
		return fmt.Errorf("close file: %w", err)
	}
	return nil
}

func (w *localWriter) Abort() {
	if !w.done {
		w.f.Close()
	}
	os.Remove(w.f.Name())
}

func (l *LocalStore) PutManifest(_ context.Context, m *Manifest) error {
	return WriteManifest(ManifestPath(SnapshotDir(l.Dir, m.Server, m.Snapshot)), m)
}

func (l *LocalStore) Manifest(_ context.Context, server, snapshot string) (*Manifest, error) {
	return ReadManifest(ManifestPath(SnapshotDir(l.Dir, server, snapshot)))
}

func (l *LocalStore) Open(_ context.Context, server, snapshot, file string) (io.ReadCloser, error) {
	f, err := os.Open(filepath.Join(SnapshotDir(l.Dir, server, snapshot), file))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", file, err)
	}
	return f, nil
}

func (l *LocalStore) List(_ context.Context, server string) ([]SnapshotInfo, error) {
	return ListSnapshots(l.Dir, server)
}

// Delete removes the snapshot and, if it was the last one, the server's
// folder, so a failed first backup leaves nothing behind.
func (l *LocalStore) Delete(_ context.Context, server, snapshot string) error {
	if err := DeleteSnapshot(l.Dir, server, snapshot); err != nil {
		return err
	}
	os.Remove(filepath.Join(l.Dir, server)) // only succeeds when empty
	return nil
}

// --- shared helpers --------------------------------------------------------

// checkNames rejects a server or snapshot that is not a plain name, so a bad
// value can never reach outside the server's folder or key prefix.
func checkNames(server, snapshot string) error {
	if _, err := time.Parse(snapshotLayout, snapshot); err != nil {
		return fmt.Errorf("refusing %q: not a snapshot id", snapshot)
	}
	if server == "" || server != filepath.Base(server) || server == "." || server == ".." {
		return fmt.Errorf("refusing server %q: not a plain name", server)
	}
	return nil
}

// cleanup deletes a partial snapshot after a failure. It runs even when ctx
// was cancelled (Ctrl-C is the usual reason we are cleaning up), with its own
// short deadline so a dead remote cannot hang the exit.
func cleanup(ctx context.Context, st Store, server, snapshot string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	st.Delete(ctx, server, snapshot)
}

// CopySnapshot copies one complete snapshot between stores, artifacts first
// and manifest last, so the copy only counts once it is whole. A failed copy
// is deleted from dst. progress, if set, is called before each artifact.
func CopySnapshot(ctx context.Context, src, dst Store, info SnapshotInfo, server string, progress func(i, n int)) error {
	if info.Status != Complete {
		return fmt.Errorf("%s/%s is not complete (%s)", server, info.ID, info.Status)
	}
	m := info.Manifest
	for i, a := range m.Artifacts {
		if progress != nil {
			progress(i+1, len(m.Artifacts))
		}
		if err := copyArtifact(ctx, src, dst, server, info.ID, a); err != nil {
			cleanup(ctx, dst, server, info.ID)
			return fmt.Errorf("%s: %w", a.ID, err)
		}
	}
	if err := dst.PutManifest(ctx, m); err != nil {
		cleanup(ctx, dst, server, info.ID)
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}

func copyArtifact(ctx context.Context, src, dst Store, server, snapshot string, a ArtifactEntry) error {
	r, err := src.Open(ctx, server, snapshot, a.File)
	if err != nil {
		return err
	}
	defer r.Close()

	w, err := dst.Create(ctx, server, snapshot, a.File)
	if err != nil {
		return err
	}
	n, err := io.Copy(w, r)
	if err == nil && n != a.Size {
		err = fmt.Errorf("source is %d bytes, manifest says %d (run bunker backup verify)", n, a.Size)
	}
	if err != nil {
		w.Abort()
		return err
	}
	return w.Commit()
}

// IsNotExist reports whether err means the artifact or snapshot is missing.
func IsNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }
