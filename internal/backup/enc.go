package backup

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"filippo.io/age"
	"github.com/kushal1o1/bunker/internal/model"
	"github.com/kushal1o1/bunker/internal/pathexp"
)

// LoadRecipient parses the age identity file and returns the corresponding
// public recipient used for encryption.
func LoadRecipient(identityPath string) (*age.X25519Recipient, error) {
	id, err := LoadIdentity(identityPath)
	if err != nil {
		return nil, err
	}
	return id.Recipient(), nil
}

// ResolveRecipient returns the public key backups are encrypted to.
//
// Without encryption.recipient it is the public half of the identity file,
// as before. With it, the identity file is optional: when it is present on
// this machine it must belong to the same key, so a typo in recipient cannot
// produce backups that the key you hold cannot open.
func ResolveRecipient(enc model.EncryptionSpec) (*age.X25519Recipient, error) {
	identityPath := pathexp.Expand(enc.Identity)
	if enc.Recipient == "" {
		return LoadRecipient(identityPath)
	}
	rcpt, err := age.ParseX25519Recipient(enc.Recipient)
	if err != nil {
		return nil, fmt.Errorf("encryption.recipient: %w", err)
	}
	if _, err := os.Stat(identityPath); errors.Is(err, fs.ErrNotExist) {
		return rcpt, nil
	}
	id, err := LoadIdentity(identityPath)
	if err != nil {
		return nil, err
	}
	if got := id.Recipient().String(); got != rcpt.String() {
		return nil, fmt.Errorf("encryption.recipient %s does not match the identity at %s (public key %s): "+
			"backups would not open with that key; fix one of them", rcpt, identityPath, got)
	}
	return rcpt, nil
}

// LoadIdentity parses the age identity file and returns the private
// identity. Verification and restore need this: LoadRecipient keeps only the
// public half, which can encrypt but can never read a backup back.
//
// TODO: support more than one identity in the file (age lets you keep old
// and new keys side by side so a snapshot written before a key rotation
// still verifies). Today only ids[0] is considered.
func LoadIdentity(identityPath string) (*age.X25519Identity, error) {
	data, err := os.ReadFile(identityPath)
	if err != nil {
		return nil, fmt.Errorf("read age identity %q (run 'bunker keygen' to create it): %w", identityPath, err)
	}
	ids, err := age.ParseIdentities(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("parse age identity %q: %w", identityPath, err)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("age identity %q contains no identities", identityPath)
	}
	id, ok := ids[0].(*age.X25519Identity)
	if !ok {
		return nil, fmt.Errorf("unsupported identity type in %q", identityPath)
	}
	return id, nil
}

// GenerateIdentity writes a fresh age keypair to path (0600) and returns the
// public key. It refuses to touch an existing file.
func GenerateIdentity(path string) (string, error) {
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("identity already exists at %q (remove it first or use --force)", path)
	}
	return ReplaceIdentity(path)
}

// ReplaceIdentity writes a fresh age keypair to path (0600), replacing any
// file already there, and returns the public key. The new key goes to a
// temporary file that is renamed over the old one, so a failed write never
// leaves the path without a key.
func ReplaceIdentity(path string) (string, error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", fmt.Errorf("generate age identity: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create config dir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".key-*.tmp")
	if err != nil {
		return "", fmt.Errorf("write identity: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	if err := writeKey(tmp, id.String()+"\n"); err != nil {
		return "", fmt.Errorf("write identity: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", fmt.Errorf("write identity: %w", err)
	}
	return id.Recipient().String(), nil
}

// writeKey writes data to f with mode 0600, syncs and closes it.
func writeKey(f *os.File, data string) error {
	err := f.Chmod(0o600)
	if err == nil {
		_, err = f.WriteString(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// FindIdentity returns the public key of the identity at identityPath.
func FindIdentity(identityPath string) (string, error) {
	id, err := LoadIdentity(identityPath)
	if err != nil {
		return "", err
	}
	return id.Recipient().String(), nil
}
