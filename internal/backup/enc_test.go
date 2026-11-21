package backup_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/kushal1o1/bunker/internal/backup"
	"github.com/kushal1o1/bunker/internal/model"
)

func TestResolveRecipient(t *testing.T) {
	identity := writeIdentity(t)
	pub, err := backup.FindIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	other, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "no-key.txt")

	cases := []struct {
		name    string
		enc     model.EncryptionSpec
		want    string
		wantErr string
	}{
		{"identity only", model.EncryptionSpec{Identity: identity}, pub, ""},
		{"recipient, no identity file", model.EncryptionSpec{Identity: missing, Recipient: pub}, pub, ""},
		{"recipient matches identity", model.EncryptionSpec{Identity: identity, Recipient: pub}, pub, ""},
		{"recipient differs from identity", model.EncryptionSpec{Identity: identity, Recipient: other.Recipient().String()}, "", "does not match"},
		{"neither", model.EncryptionSpec{Identity: missing}, "", "bunker keygen"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := backup.ResolveRecipient(tc.enc)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != tc.want {
				t.Errorf("recipient = %s, want %s", got, tc.want)
			}
		})
	}
}

// A backup machine with only the public key can back up; the laptop with
// the private key can still read and verify the result.
func TestRunWithRecipientOnly(t *testing.T) {
	fs := startFakeServer(t, func(cmd string) ([]byte, int) {
		return []byte("DATA-" + cmd), 0
	})
	keyPath, khPath := writeAuthKeyFiles(t, fs)
	identity := writeIdentity(t)
	pub, err := backup.FindIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	storageDir := filepath.Join(t.TempDir(), "backups")
	missing := filepath.Join(t.TempDir(), "not-on-this-box.txt")

	cfg := loadTestConfig(t, writeConfig(t, fs, keyPath, khPath, missing, storageDir, ""))
	cfg.Encryption.Recipient = pub
	res, err := runnerFor(cfg).Run(t.Context(), "vps-01")
	if err != nil {
		t.Fatalf("Run with recipient only: %v", err)
	}
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("run created an identity file")
	}

	man, err := backup.ReadManifest(filepath.Join(res.Dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if man.Recipient != pub {
		t.Errorf("manifest recipient = %s, want %s", man.Recipient, pub)
	}
	for _, art := range res.Artifacts {
		if plain := decryptDecompress(t, filepath.Join(res.Dir, art.File), identity); !strings.HasPrefix(string(plain), "DATA-") {
			t.Errorf("%s: decrypted content wrong", art.ID)
		}
	}

	cfg.Encryption.Identity = identity
	vr, err := (&backup.Verifier{Cfg: cfg}).Verify(t.Context(), "vps-01", res.Snapshot)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !vr.OK() {
		t.Errorf("verify failed: %+v", vr)
	}
}
