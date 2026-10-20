package backup

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"filippo.io/age"
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
// public key.
func GenerateIdentity(path string) (string, error) {
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("identity already exists at %q (remove it first or use --force)", path)
	}

	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", fmt.Errorf("generate age identity: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(id.String()+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write identity: %w", err)
	}
	return id.Recipient().String(), nil
}

func FindIdentity(identityPath string) (string, error) {
	data, err := os.ReadFile(identityPath)
	if err != nil {
		return "", err
	}
	ids, err := age.ParseIdentities(bytes.NewReader(data))
	if err != nil || len(ids) == 0 {
		return "", fmt.Errorf("parse identity %q: %w", identityPath, err)
	}
	id, ok := ids[0].(*age.X25519Identity)
	if !ok {
		return "", fmt.Errorf("unsupported identity type in %q", identityPath)
	}
	return id.Recipient().String(), nil
}

func RemoveIdentity(identityPath string) error {
	return os.Remove(identityPath)
}
