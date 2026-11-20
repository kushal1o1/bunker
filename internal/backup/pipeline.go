package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"time"

	"filippo.io/age"
	"github.com/klauspost/compress/zstd"
	"github.com/kushal1o1/bunker/internal/config"
	"github.com/kushal1o1/bunker/internal/pathexp"
	"github.com/kushal1o1/bunker/internal/sshx"
)

type Result struct {
	Server   string
	Snapshot string
	// Dir is the local snapshot folder, or "" when storage is S3 only.
	Dir       string
	Artifacts []ArtifactEntry
	// UploadErr is set when the snapshot was stored locally but copying it
	// to S3 failed. The local snapshot is complete and usable.
	UploadErr error
}

type Runner struct {
	Cfg *config.Config
	// Stores is where snapshots go. Nil means open them from Cfg.
	Stores *Stores
	// Progress receives events as the run advances. It may be nil. With
	// several servers running at once it is called from several goroutines,
	// so it must be safe for concurrent use.
	Progress func(Event)
}

// EventKind identifies a step of one server's run.
type EventKind int

const (
	// Connecting: the run started and is opening the SSH connection.
	Connecting EventKind = iota
	// ArtifactStarted: the remote command for one artifact is about to run.
	ArtifactStarted
	// ArtifactDone: one artifact is stored and checksummed.
	ArtifactDone
	// Uploading: copying artifact Index of Total from local storage to S3.
	Uploading
)

// Event is one progress update from Runner.Run. Index is 1-based within
// Total artifacts; Size and Elapsed are set on ArtifactDone.
type Event struct {
	Server   string
	Kind     EventKind
	Artifact string
	Index    int
	Total    int
	Size     int64
	Elapsed  time.Duration
}

func (r *Runner) emit(e Event) {
	if r.Progress != nil {
		r.Progress(e)
	}
}

func (r *Runner) stores() (*Stores, error) {
	if r.Stores != nil {
		return r.Stores, nil
	}
	return OpenStores(r.Cfg)
}

// Run pulls a full snapshot from one server into local storage. It only ever
// executes the allowlisted commands on the remote side and reads their stdout.
func (r *Runner) Run(ctx context.Context, serverName string) (*Result, error) {
	spec := r.Cfg.Servers[serverName]
	r.emit(Event{Server: serverName, Kind: Connecting})

	client, err := sshx.Connect(ctx, sshx.Config{
		Host:       spec.Host,
		Port:       spec.SSH.Port,
		User:       spec.SSH.User,
		KeyPath:    pathexp.Expand(spec.SSH.Key),
		KnownHosts: pathexp.Expand(spec.SSH.KnownHosts),
		Timeout:    spec.SSH.ConnectTimeout,
		KeepAlive:  spec.SSH.KeepAliveInterval,
	})
	if err != nil {
		return nil, err
	}
	defer client.Close()

	recipient, err := LoadRecipient(pathexp.Expand(r.Cfg.Encryption.Identity))
	if err != nil {
		return nil, err
	}

	stores, err := r.stores()
	if err != nil {
		return nil, err
	}
	store := stores.Primary()

	now := time.Now().UTC()
	snapshot := NewSnapshotID(now)
	fail := func(err error) (*Result, error) {
		cleanup(ctx, store, serverName, snapshot)
		return nil, err
	}

	man := &Manifest{
		Server:    serverName,
		Snapshot:  snapshot,
		CreatedAt: now.Format(time.RFC3339),
		Recipient: recipient.String(),
		Artifacts: nil,
	}

	arts := buildArtifacts(spec)
	for i, art := range arts {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		r.emit(Event{Server: serverName, Kind: ArtifactStarted, Artifact: art.ID(), Index: i + 1, Total: len(arts)})
		start := time.Now()
		entry, err := pull(ctx, client, art, store, serverName, snapshot, recipient)
		if err != nil {
			return fail(fmt.Errorf("%s: %w", art.ID(), err))
		}
		man.Artifacts = append(man.Artifacts, entry)
		r.emit(Event{Server: serverName, Kind: ArtifactDone, Artifact: art.ID(), Index: i + 1, Total: len(arts), Size: entry.Size, Elapsed: time.Since(start)})
	}

	if err := store.PutManifest(ctx, man); err != nil {
		return fail(fmt.Errorf("write manifest: %w", err))
	}

	res := &Result{Server: serverName, Snapshot: snapshot, Artifacts: man.Artifacts}
	if stores.Local != nil {
		res.Dir = SnapshotDir(r.Cfg.Storage.Dir, serverName, snapshot)
	}
	if stores.Both() {
		// The SSH session is no longer needed; free it before a long upload.
		client.Close()
		info := SnapshotInfo{ID: snapshot, Created: now, Status: Complete, Manifest: man}
		res.UploadErr = CopySnapshot(ctx, stores.Local, stores.Remote, info, serverName, func(i, n int) {
			r.emit(Event{Server: serverName, Kind: Uploading, Index: i, Total: n})
		})
	}
	return res, nil
}

// pull streams one artifact off the server: zstd-compress, age-encrypt, and
// write to the store, hashing the stored bytes on the way through.
func pull(ctx context.Context, client *sshx.Client, art Artifact, store Store, server, snapshot string, recipient *age.X25519Recipient) (ArtifactEntry, error) {
	// Everything that can fail locally is set up *before* the remote command
	// starts. Once Exec returns, the remote side is already writing into the
	// SSH stdout pipe; if we then failed on a local error we would have to
	// tear the process down mid-stream, and pg_dump can block writing into a
	// full pipe that nothing is draining.
	rel := art.FilePath()
	w, err := store.Create(ctx, server, snapshot, rel)
	if err != nil {
		return ArtifactEntry{}, err
	}
	h := sha256.New()
	counted := &countWriter{}
	encW, err := age.Encrypt(io.MultiWriter(w, h, counted), recipient)
	if err != nil {
		w.Abort()
		return ArtifactEntry{}, fmt.Errorf("age encrypt: %w", err)
	}
	zw, err := zstd.NewWriter(encW)
	if err != nil {
		w.Abort()
		return ArtifactEntry{}, fmt.Errorf("zstd encoder: %w", err)
	}

	proc, err := client.Exec(ctx, art.Command())
	if err != nil {
		zw.Close()
		w.Abort()
		return ArtifactEntry{}, err
	}
	defer proc.Kill()

	abort := func(err error) (ArtifactEntry, error) {
		zw.Close()
		w.Abort()
		return ArtifactEntry{}, err
	}
	if _, err := io.Copy(zw, proc.Out); err != nil {
		return abort(fmt.Errorf("stream: %w", err))
	}
	if err := zw.Close(); err != nil {
		return abort(fmt.Errorf("finalize zstd: %w", err))
	}
	if err := encW.Close(); err != nil {
		w.Abort()
		return ArtifactEntry{}, fmt.Errorf("finalize age: %w", err)
	}
	// Wait before Commit: a remote command that failed (pg_dump exit 1) must
	// never leave a committed artifact behind.
	if err := proc.Wait(); err != nil {
		w.Abort()
		return ArtifactEntry{}, err
	}
	if err := w.Commit(); err != nil {
		w.Abort()
		return ArtifactEntry{}, err
	}

	return ArtifactEntry{
		ID:     art.ID(),
		File:   rel,
		Size:   counted.n,
		SHA256: hex.EncodeToString(h.Sum(nil)),
	}, nil
}

// countWriter counts bytes written through it.
type countWriter struct{ n int64 }

func (c *countWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

func totalSize(arts []ArtifactEntry) int64 {
	var n int64
	for _, a := range arts {
		n += a.Size
	}
	return n
}
