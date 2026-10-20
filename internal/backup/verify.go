package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"filippo.io/age"
	"github.com/klauspost/compress/zstd"
	"github.com/kushal1o1/bunker/internal/config"
	"github.com/kushal1o1/bunker/internal/pathexp"
)

// ArtifactStatus is the per-artifact outcome of one verify pass. NoKey is set
// when the snapshot's recipient does not match the local identity; decrypt is
// then impossible by construction, so it is reported as NO KEY rather than a
// corruption FAIL.
type ArtifactStatus struct {
	Entry     ArtifactEntry
	Path      string
	SizeOK    bool
	SumOK     bool
	DecryptOK bool
	NoKey     bool
	Err       error
}

// VerifyResult summarizes one snapshot verification.
type VerifyResult struct {
	Server    string
	Snapshot  string
	Dir       string
	Recipient string // recipient recorded in the manifest
	KeyID     string // recipient of the local identity
	NoKey     bool
	Artifacts []ArtifactStatus
}

// OK reports whether every artifact passed every applicable check. A snapshot
// with no matching key is not OK: verification exists to prove the data is
// readable, and it is not.
func (vr *VerifyResult) OK() bool {
	if vr == nil {
		return false
	}
	for _, a := range vr.Artifacts {
		if a.NoKey || !a.SizeOK || !a.SumOK || !a.DecryptOK {
			return false
		}
	}
	return true
}

type Verifier struct {
	Cfg  *config.Config
	Logf func(format string, args ...any)
}

// Verify checks one stored snapshot without touching the network:
//
//  1. manifest parses (ReadManifest) and its recipient matches the local identity
//  2. every artifact exists at the recorded size
//  3. every artifact's SHA-256 matches the manifest
//  4. every artifact decrypts and fully zstd-decodes to io.Discard
//
// Decrypt is skipped wholesale when no key matches (res.NoKey): retrying
// twenty files against the wrong recipient would only answer the question the
// recipient check already answered.
//
// TODO: pg_restore --list over the decrypted dump to prove the -Fc custom
// format archive is structurally valid. Deferred because pg_restore is not on PATH on this
// machine and would pull a Postgres client dependency into bunker.
//
// TODO: a full restore into a throwaway database later .
func (v *Verifier) Verify(ctx context.Context, server, snapshot string) (*VerifyResult, error) {
	if _, ok := v.Cfg.Servers[server]; !ok {
		return nil, fmt.Errorf("unknown server %q (try: bunker server list)", server)
	}

	dir := SnapshotDir(v.Cfg.Storage.Dir, server, snapshot)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("no such snapshot: %s/%s (stored under %s)", server, snapshot, v.Cfg.Storage.Dir)
	}

	man, err := ReadManifest(ManifestPath(dir))
	if err != nil {
		return nil, err
	}

	id, err := LoadIdentity(pathexp.Expand(v.Cfg.Encryption.Identity))
	if err != nil {
		return nil, err
	}

	keyID := id.Recipient().String()
	res := &VerifyResult{
		Server:    server,
		Snapshot:  snapshot,
		Dir:       dir,
		Recipient: man.Recipient,
		KeyID:     keyID,
		NoKey:     man.Recipient != keyID,
	}

	for _, entry := range man.Artifacts {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		st := ArtifactStatus{Entry: entry, Path: filepath.Join(dir, entry.File)}
		st.SizeOK, st.SumOK, st.Err = verifyFile(ctx, st.Path, entry)
		if st.Err != nil {
			res.Artifacts = append(res.Artifacts, st)
			continue
		}

		if res.NoKey {
			st.NoKey = true
			res.Artifacts = append(res.Artifacts, st)
			continue
		}

		st.DecryptOK, st.Err = verifyDecrypt(ctx, st.Path, id)
		res.Artifacts = append(res.Artifacts, st)
	}

	if v.Logf != nil {
		v.Logf("  manifest recipient  %s", res.Recipient)
		if res.NoKey {
			v.Logf("  local identity      %s  (NO KEY: decrypt skipped)", res.KeyID)
		} else {
			v.Logf("  local identity      %s", res.KeyID)
		}
	}
	return res, nil
}

// verifyFile confirms the stored file matches the manifest's size and SHA-256.
func verifyFile(ctx context.Context, path string, entry ArtifactEntry) (sizeOK, sumOK bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return false, false, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return false, false, fmt.Errorf("stat %s: %w", path, err)
	}
	if fi.Size() != entry.Size {
		return false, false, nil
	}

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, false, fmt.Errorf("hash %s: %w", path, err)
	}
	if hex.EncodeToString(h.Sum(nil)) != entry.SHA256 {
		return true, false, nil
	}
	return true, true, nil
}

// verifyDecrypt proves an artifact is genuinely readable: age-decrypt it, then
// zstd-decode the whole stream to io.Discard. Both formats write headers /
// trailers only around the real payload, so a partially streamed copy would
// succeed while the tail is garbage; io.Copy must return without error.
func verifyDecrypt(ctx context.Context, path string, id *age.X25519Identity) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	r, err := age.Decrypt(f, id)
	if err != nil {
		return false, fmt.Errorf("age decrypt: %w", err)
	}
	zr, err := zstd.NewReader(r)
	if err != nil {
		return false, fmt.Errorf("zstd init: %w", err)
	}
	defer zr.Close()

	if _, err := io.Copy(io.Discard, zr); err != nil {
		return false, fmt.Errorf("decompress: %w", err)
	}
	return true, nil
}
