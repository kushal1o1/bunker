package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kushal1o1/bunker/internal/backup"
)

// keygenConfig writes a config whose identity lives in a temp dir.
func keygenConfig(t *testing.T) (cfgPath, keyPath string) {
	t.Helper()
	dir := t.TempDir()
	keyPath = filepath.Join(dir, "keys", "key.txt")
	cfgPath = filepath.Join(dir, "bunker.yaml")
	cfg := "servers:\n  web:\n    host: 127.0.0.1\n    backup:\n      paths: [/srv/data]\nencryption:\n  identity: " + keyPath + "\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath, keyPath
}

func runKeygen(t *testing.T, cfgPath string, flags ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"-c", cfgPath, "keygen"}, flags...))
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func TestKeygenPublicPrintsOnlyTheKey(t *testing.T) {
	cfgPath, keyPath := keygenConfig(t)
	if _, err := runKeygen(t, cfgPath, "--public"); err == nil || !strings.Contains(err.Error(), "bunker keygen") {
		t.Fatalf("--public without a key: err = %v, want a hint to run keygen", err)
	}

	if _, err := runKeygen(t, cfgPath); err != nil {
		t.Fatal(err)
	}
	want, err := backup.FindIdentity(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	out, err := runKeygen(t, cfgPath, "--public")
	if err != nil {
		t.Fatal(err)
	}
	if out != want+"\n" {
		t.Errorf("--public printed %q, want only %q", out, want+"\n")
	}
}

func TestKeygenForceReplacesAndShowsBothKeys(t *testing.T) {
	cfgPath, keyPath := keygenConfig(t)
	if _, err := runKeygen(t, cfgPath); err != nil {
		t.Fatal(err)
	}
	oldPub, _ := backup.FindIdentity(keyPath)

	out, err := runKeygen(t, cfgPath, "--force")
	if err != nil {
		t.Fatal(err)
	}
	newPub, _ := backup.FindIdentity(keyPath)
	if newPub == oldPub {
		t.Fatal("--force kept the same key")
	}
	for _, want := range []string{"replaced age identity", "public key: " + newPub, "old public key: " + oldPub} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key mode = %v, want 0600", info.Mode().Perm())
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(keyPath), ".key-*")); len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

func TestKeygenForceReplacesUnreadableKey(t *testing.T) {
	cfgPath, keyPath := keygenConfig(t)
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := runKeygen(t, cfgPath, "--force")
	if err != nil {
		t.Fatalf("--force on a broken key: %v", err)
	}
	if !strings.Contains(out, "old public key: unknown") {
		t.Errorf("output = %q, want it to say the old key was unreadable", out)
	}
	if _, err := backup.FindIdentity(keyPath); err != nil {
		t.Errorf("new key unreadable: %v", err)
	}
}

func TestKeygenForceAndPublicConflict(t *testing.T) {
	cfgPath, _ := keygenConfig(t)
	if _, err := runKeygen(t, cfgPath, "--force", "--public"); err == nil {
		t.Fatal("--force with --public: want an error")
	}
}
