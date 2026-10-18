package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"filippo.io/age"
	"github.com/kushal1o1/bunker/internal/config"
	"github.com/kushal1o1/bunker/internal/pathexp"
	"github.com/kushal1o1/bunker/internal/sshx"
	"github.com/klauspost/compress/zstd"
)

type Result struct {
	Server   string
	Snapshot string
	Dir      string
	Artifacts []ArtifactEntry
}

type Runner struct {
	Cfg *config.Config
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
	})
	if err != nil {
		return nil, err
	}
	defer client.Close()

	recipient, err := LoadRecipient(pathexp.Expand(r.Cfg.Encryption.Identity))
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	snapshot := NewSnapshotID(now)
	dir := SnapshotDir(r.Cfg.Storage.Dir, serverName, snapshot)
	serverDir := filepath.Dir(dir)
	fail := func(err error) (*Result, error) {
		os.RemoveAll(dir)
		os.Remove(serverDir)
		return nil, err
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create snapshot dir: %w", err)
	}

	man := &Manifest{
		Server:    serverName,
		Snapshot:  snapshot,
		CreatedAt: now.Format(time.RFC3339),
		Recipient: recipient.String(),
		Artifacts: nil,
	}

	arts := buildArtifacts(spec)
	var errOut error
	for i, art := range arts {
		select {
		case <-ctx.Done():
			return fail(ctx.Err())
		default:
		}

		r.emit(Event{Server: serverName, Kind: ArtifactStarted, Artifact: art.ID(), Index: i + 1, Total: len(arts)})
		start := time.Now()
		entry, err := pull(ctx, client, art, dir, recipient)
		if err != nil {
			errOut = fmt.Errorf("%s: %w", art.ID(), err)
			break
		}
		man.Artifacts = append(man.Artifacts, entry)
		r.emit(Event{Server: serverName, Kind: ArtifactDone, Artifact: art.ID(), Index: i + 1, Total: len(arts), Size: entry.Size, Elapsed: time.Since(start)})
	}
	if errOut != nil {
		return fail(errOut)
	}

	if err := WriteManifest(ManifestPath(dir), man); err != nil {
		return fail(fmt.Errorf("write manifest: %w", err))
	}

	return &Result{
		Server:    serverName,
		Snapshot:  snapshot,
		Dir:       dir,
		Artifacts: man.Artifacts,
	}, nil
}

// pull streams one artifact off the server: zstd-compress, age-encrypt, write
// to disk, then checksum the stored file.
func pull(ctx context.Context, client *sshx.Client, art Artifact, dir string, recipient *age.X25519Recipient) (ArtifactEntry, error) {
	// Everything that can fail locally is set up *before* the remote command
	// starts. Once Exec returns, the remote side is already writing into the
	// SSH stdout pipe; if we then failed on a local error we would have to
	// tear the process down mid-stream, and pg_dump can block writing into a
	// full pipe that nothing is draining.
	rel := art.FilePath()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return ArtifactEntry{}, fmt.Errorf("create dir: %w", err)
	}

	f, err := os.Create(full)
	if err != nil {
		return ArtifactEntry{}, fmt.Errorf("create file: %w", err)
	}

	encW, err := age.Encrypt(f, recipient)
	if err != nil {
		f.Close()
		return ArtifactEntry{}, fmt.Errorf("age encrypt: %w", err)
	}
	zw, err := zstd.NewWriter(encW)
	if err != nil {
		encW.Close()
		f.Close()
		return ArtifactEntry{}, fmt.Errorf("zstd encoder: %w", err)
	}

	proc, err := client.Exec(ctx, art.Command())
	if err != nil {
		zw.Close()
		encW.Close()
		f.Close()
		return ArtifactEntry{}, err
	}
	defer proc.Kill()

	_, err = io.Copy(zw, proc.Out)
	if err != nil {
		zw.Close()
		encW.Close()
		f.Close()
		return ArtifactEntry{}, fmt.Errorf("stream: %w", err)
	}
	if err := zw.Close(); err != nil {
		encW.Close()
		f.Close()
		return ArtifactEntry{}, fmt.Errorf("finalize zstd: %w", err)
	}
	if err := encW.Close(); err != nil {
		f.Close()
		return ArtifactEntry{}, fmt.Errorf("finalize age: %w", err)
	}
	if err := f.Close(); err != nil {
		return ArtifactEntry{}, fmt.Errorf("close file: %w", err)
	}

	if err := proc.Wait(); err != nil {
		return ArtifactEntry{}, err
	}

	sum, size, err := hashFile(full)
	if err != nil {
		return ArtifactEntry{}, fmt.Errorf("checksum stored artifact: %w", err)
	}
	return ArtifactEntry{
		ID:     art.ID(),
		File:   rel,
		Size:   size,
		SHA256: hex.EncodeToString(sum),
	}, nil
}

// hashFile returns the SHA-256 and byte length of path.
//
// Errors are returned rather than swallowed: the previous version handed back
// nil on failure, which pull then hex-encoded into an empty string and wrote
// into manifest.json - a snapshot that looked valid to anything trusting its
// own recorded checksum.
func hashFile(path string) ([]byte, int64, error) {
	h := sha256.New()
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	n, err := io.Copy(h, f)
	if err != nil {
		return nil, n, fmt.Errorf("hash %s: %w", path, err)
	}
	return h.Sum(nil), n, nil
}

func totalSize(arts []ArtifactEntry) int64 {
	var n int64
	for _, a := range arts {
		n += a.Size
	}
	return n
}