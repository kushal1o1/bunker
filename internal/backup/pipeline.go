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
	Cfg  *config.Config
	Logf func(format string, args ...any)
}

// Run pulls a full snapshot from one server into local storage. It only ever
// executes the allowlisted commands on the remote side and reads their stdout.
func (r *Runner) Run(ctx context.Context, serverName string) (*Result, error) {
	spec := r.Cfg.Servers[serverName]

	client, err := sshx.Connect(ctx, sshx.Config{
		Host:       spec.Host,
		Port:       spec.SSH.Port,
		User:       spec.SSH.User,
		KeyPath:    pathexp.Expand(spec.SSH.Key),
		KnownHosts: pathexp.Expand(spec.SSH.KnownHosts),
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

	var errOut error
	for _, art := range buildArtifacts(spec) {
		select {
		case <-ctx.Done():
			return fail(ctx.Err())
		default:
		}

		entry, err := pull(ctx, client, art, dir, recipient, r.Logf)
		if err != nil {
			errOut = fmt.Errorf("%s: %w", art.ID(), err)
			break
		}
		man.Artifacts = append(man.Artifacts, entry)
	}
	if errOut != nil {
		return fail(errOut)
	}

	if err := WriteManifest(ManifestPath(dir), man); err != nil {
		return fail(fmt.Errorf("write manifest: %w", err))
	}

	if r.Logf != nil {
		r.Logf("  snapshot %s (%s)", snapshot, humanBytes(totalSize(man.Artifacts)))
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
func pull(ctx context.Context, client *sshx.Client, art Artifact, dir string, recipient *age.X25519Recipient, logf func(string, ...any)) (ArtifactEntry, error) {
	proc, err := client.Exec(ctx, art.Command())
	if err != nil {
		return ArtifactEntry{}, err
	}
	defer proc.Kill()

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

	start := time.Now()
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

	sum, size := hashFile(full)
	if logf != nil {
		logf("  ok %-46s %6s  %s", art.ID(), humanBytes(size), time.Since(start).Round(time.Millisecond))
	}
	return ArtifactEntry{
		ID:     art.ID(),
		File:   rel,
		Size:   size,
		SHA256: hex.EncodeToString(sum),
	}, nil
}

func hashFile(path string) ([]byte, int64) {
	h := sha256.New()
	f, err := os.Open(path)
	if err != nil {
		return nil, 0
	}
	defer f.Close()
	n, _ := io.Copy(h, f)
	return h.Sum(nil), n
}

func totalSize(arts []ArtifactEntry) int64 {
	var n int64
	for _, a := range arts {
		n += a.Size
	}
	return n
}