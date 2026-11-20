package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	Dir       string // local snapshot folder, "" for S3
	Where     string // the store: a path or s3://bucket/prefix
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
	Cfg *config.Config
	// Store is where the snapshot is read from. Nil means the primary store:
	// local when there is a local folder, otherwise S3.
	Store Store
	Logf  func(format string, args ...any)
}

// Verify checks one stored snapshot:
//
//  1. manifest parses (ParseManifest) and its recipient matches the local identity
//  2. every artifact exists at the recorded size
//  3. every artifact's SHA-256 matches the manifest
//  4. every artifact decrypts and fully zstd-decodes to io.Discard
//
// Each artifact is read exactly once: the bytes are hashed and counted while
// they are decrypted, so an S3 snapshot is downloaded once, never saved.
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
	if err := checkNames(server, snapshot); err != nil {
		return nil, fmt.Errorf("no such snapshot: %s/%s", server, snapshot)
	}

	store := v.Store
	if store == nil {
		stores, err := OpenStores(v.Cfg)
		if err != nil {
			return nil, err
		}
		store = stores.Primary()
	}

	man, err := store.Manifest(ctx, server, snapshot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("no such snapshot: %s/%s in %s (%w)", server, snapshot, store.Where(), err)
		}
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
		Where:     store.Where(),
		Recipient: man.Recipient,
		KeyID:     keyID,
		NoKey:     man.Recipient != keyID,
	}
	if l, ok := store.(*LocalStore); ok {
		res.Dir = SnapshotDir(l.Dir, server, snapshot)
	}
	decryptWith := id
	if res.NoKey {
		decryptWith = nil
	}

	for _, entry := range man.Artifacts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		st := ArtifactStatus{Entry: entry, Path: artifactPath(store, server, snapshot, entry.File), NoKey: res.NoKey}
		r, err := store.Open(ctx, server, snapshot, entry.File)
		if err != nil {
			st.Err = err
			res.Artifacts = append(res.Artifacts, st)
			continue
		}
		st.SizeOK, st.SumOK, st.DecryptOK, st.Err = checkArtifact(r, entry, decryptWith)
		r.Close()
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

// artifactPath names an artifact for humans: a file path or an s3:// URL.
func artifactPath(store Store, server, snapshot, file string) string {
	if l, ok := store.(*LocalStore); ok {
		return filepath.Join(SnapshotDir(l.Dir, server, snapshot), file)
	}
	return store.Where() + server + "/" + snapshot + "/" + file
}

// checkArtifact reads one artifact once. The bytes are counted and hashed as
// they pass through decryption, and whatever decryption did not consume is
// drained afterwards, so size and SHA-256 always cover the whole file even
// when decryption stops early on corrupt data.
//
// id is nil when no key matches; then only size and SHA-256 are checked.
// Decryption proves the artifact is readable: age, then zstd to io.Discard.
// Both formats put headers and trailers around the payload, so a partial copy
// fails here even if its first bytes look fine.
func checkArtifact(r io.Reader, entry ArtifactEntry, id *age.X25519Identity) (sizeOK, sumOK, decryptOK bool, err error) {
	h := sha256.New()
	counted := &countWriter{}
	tee := io.TeeReader(r, io.MultiWriter(h, counted))

	var decErr error
	if id != nil {
		decErr = decrypt(tee, id)
	}
	if _, err := io.Copy(io.Discard, tee); err != nil {
		return false, false, false, fmt.Errorf("read %s: %w", entry.File, err)
	}

	sizeOK = counted.n == entry.Size
	sumOK = sizeOK && hex.EncodeToString(h.Sum(nil)) == entry.SHA256
	if id == nil {
		return sizeOK, sumOK, false, nil
	}
	return sizeOK, sumOK, decErr == nil, decErr
}

func decrypt(r io.Reader, id *age.X25519Identity) error {
	ar, err := age.Decrypt(r, id)
	if err != nil {
		return fmt.Errorf("age decrypt: %w", err)
	}
	zr, err := zstd.NewReader(ar)
	if err != nil {
		return fmt.Errorf("zstd init: %w", err)
	}
	defer zr.Close()
	if _, err := io.Copy(io.Discard, zr); err != nil {
		return fmt.Errorf("decompress: %w", err)
	}
	return nil
}
