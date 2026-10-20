package backup_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kushal1o1/bunker/internal/backup"
	"github.com/kushal1o1/bunker/internal/config"
)

var stdResponder = func(cmd string) ([]byte, int) {
	switch {
	case strings.Contains(cmd, "pg_dump"):
		return []byte("FAKEDUMP-" + strings.Repeat("data", 10000) + "-END"), 0
	case strings.Contains(cmd, "tar "):
		return []byte("FAKETAR" + strings.Repeat("x", 100000)), 0
	default:
		return []byte("unexpected command: " + cmd), 1
	}
}

// createSnapshot runs one full fake backup and returns the config plus the
// resulting snapshot, ready for verification.
func createSnapshot(t *testing.T, responder func(cmd string) ([]byte, int)) (*config.Config, *backup.Result) {
	t.Helper()
	fs := startFakeServer(t, responder)
	keyPath, khPath := writeAuthKeyFiles(t, fs)
	identity := writeIdentity(t)
	storageDir := filepath.Join(t.TempDir(), "backups")
	cfgPath := writeConfig(t, fs, keyPath, khPath, identity, storageDir, "")
	cfg := loadTestConfig(t, cfgPath)

	res, err := runnerFor(cfg).Run(context.Background(), "vps-01")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return cfg, res
}

func verifySnapshot(t *testing.T, cfg *config.Config, res *backup.Result) *backup.VerifyResult {
	t.Helper()
	vres, err := (&backup.Verifier{Cfg: cfg}).Verify(context.Background(), "vps-01", res.Snapshot)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	return vres
}

func TestVerifyPassesOnHealthySnapshot(t *testing.T) {
	cfg, res := createSnapshot(t, stdResponder)
	vres := verifySnapshot(t, cfg, res)

	if !vres.OK() {
		t.Fatalf("healthy snapshot reported not OK")
	}
	if vres.NoKey {
		t.Error("healthy snapshot reported NO KEY")
	}
	if vres.Recipient != vres.KeyID {
		t.Errorf("recipient %s != local identity %s", vres.Recipient, vres.KeyID)
	}
	if len(vres.Artifacts) != 2 {
		t.Fatalf("artifacts = %d, want 2", len(vres.Artifacts))
	}
	for _, a := range vres.Artifacts {
		if !a.SizeOK || !a.SumOK || !a.DecryptOK {
			t.Errorf("artifact %s: size=%v sha256=%v decrypt=%v", a.Entry.ID, a.SizeOK, a.SumOK, a.DecryptOK)
		}
		if a.Err != nil {
			t.Errorf("artifact %s: unexpected err %v", a.Entry.ID, a.Err)
		}
		if !strings.HasPrefix(a.Path, res.Dir) {
			t.Errorf("artifact path %q outside snapshot dir %q", a.Path, res.Dir)
		}
	}
}

func TestVerifyDetectsCorruptArtifact(t *testing.T) {
	cfg, res := createSnapshot(t, stdResponder)
	path := filepath.Join(res.Dir, res.Artifacts[0].File)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 0xff
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	vres := verifySnapshot(t, cfg, res)
	if vres.OK() {
		t.Fatal("corrupted snapshot reported OK")
	}
	st := vres.Artifacts[0]
	if !st.SizeOK {
		t.Error("size check should pass: byte flip keeps the length")
	}
	if st.SumOK {
		t.Error("sha256 should fail on a corrupted file")
	}
}

func TestVerifyDetectsTruncatedArtifact(t *testing.T) {
	cfg, res := createSnapshot(t, stdResponder)
	path := filepath.Join(res.Dir, res.Artifacts[0].File)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data[:len(data)/2], 0o600); err != nil {
		t.Fatal(err)
	}

	vres := verifySnapshot(t, cfg, res)
	if vres.OK() {
		t.Fatal("truncated snapshot reported OK")
	}
	if vres.Artifacts[0].SizeOK {
		t.Error("size check should fail on truncated file")
	}
}

func TestVerifyReportsNoKeyOnRecipientMismatch(t *testing.T) {
	cfg, res := createSnapshot(t, stdResponder)

	other := writeIdentity(t)
	cfg.Encryption.Identity = other

	vres := verifySnapshot(t, cfg, res)
	if !vres.NoKey {
		t.Fatal("expected NO KEY on recipient mismatch")
	}
	if vres.OK() {
		t.Fatal("a snapshot with no matching key must never verify OK")
	}
	if vres.Recipient == vres.KeyID {
		t.Error("recipients should differ after identity swap")
	}
	st := vres.Artifacts[0]
	if !st.NoKey {
		t.Error("artifact should be marked NoKey")
	}
	if st.DecryptOK {
		t.Error("decrypt must be skipped when no key matches")
	}
	if !st.SizeOK || !st.SumOK {
		t.Error("size and sha256 must still verify without the matching key")
	}
}

func TestVerifyMissingManifest(t *testing.T) {
	cfg, res := createSnapshot(t, stdResponder)
	if err := os.Remove(filepath.Join(res.Dir, backup.ManifestName)); err != nil {
		t.Fatal(err)
	}

	_, err := (&backup.Verifier{Cfg: cfg}).Verify(context.Background(), "vps-01", res.Snapshot)
	if err == nil {
		t.Fatal("expected error on missing manifest")
	}
	if !strings.Contains(err.Error(), "read manifest") {
		t.Errorf("error should mention the manifest: %v", err)
	}
}

func TestVerifyMissingArtifact(t *testing.T) {
	cfg, res := createSnapshot(t, stdResponder)
	if err := os.Remove(filepath.Join(res.Dir, res.Artifacts[0].File)); err != nil {
		t.Fatal(err)
	}

	vres := verifySnapshot(t, cfg, res)
	if vres.OK() {
		t.Fatal("snapshot with a missing artifact reported OK")
	}
	st := vres.Artifacts[0]
	if st.Err == nil {
		t.Fatal("expected an open error for the missing artifact")
	}
	if !errors.Is(st.Err, os.ErrNotExist) {
		t.Errorf("want os.ErrNotExist, got %v", st.Err)
	}
	if st.SizeOK {
		t.Error("size must not pass for a missing file")
	}
}

func TestVerifyUnknownServerAndSnapshot(t *testing.T) {
	cfg, _ := createSnapshot(t, stdResponder)
	v := &backup.Verifier{Cfg: cfg}

	if _, err := v.Verify(context.Background(), "ghost", "anything"); err == nil {
		t.Fatal("expected unknown-server error")
	}
	if _, err := v.Verify(context.Background(), "vps-01", "2020-01-01T00-00-00"); err == nil {
		t.Fatal("expected no-such-snapshot error")
	}
}
