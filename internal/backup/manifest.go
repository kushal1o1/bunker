package backup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kushal1o1/bunker/internal/model"
)

const ManifestName = "manifest.json"

// snapshotLayout is the time format of snapshot IDs and directory names. It
// sorts lexically in time order.
const snapshotLayout = "2006-01-02T15-04-05"

type ArtifactEntry struct {
	ID     string `json:"id"`
	File   string `json:"file"`
	Size   int64  `json:"size_bytes"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Server    string          `json:"server"`
	Snapshot  string          `json:"snapshot"`
	CreatedAt string          `json:"created_at"`
	Recipient string          `json:"recipient"`
	Artifacts []ArtifactEntry `json:"artifacts"`
}

func NewSnapshotID(t time.Time) string {
	return t.UTC().Format(snapshotLayout)
}

func SnapshotDir(storageDir, server, snapshot string) string {
	return filepath.Join(storageDir, server, snapshot)
}

func ManifestPath(snapshotDir string) string {
	return filepath.Join(snapshotDir, ManifestName)
}

func WriteManifest(path string, m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// ReadManifest loads a snapshot manifest and validates the fields that
// verification depends on.
//
// A present-and-valid manifest is the completeness marker: Run writes it
// last (after every artifact has landed), and a failed run deletes the whole
// snapshot dir. So a manifest that is missing or fails these checks means the
// snapshot is unusable rather than merely incomplete.
func ReadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	if m.Server == "" || m.Snapshot == "" || m.Recipient == "" {
		return nil, fmt.Errorf("manifest %s: missing server, snapshot or recipient", path)
	}
	if len(m.Artifacts) == 0 {
		return nil, fmt.Errorf("manifest %s: no artifacts recorded", path)
	}
	for i, a := range m.Artifacts {
		if a.ID == "" || a.File == "" || a.SHA256 == "" {
			return nil, fmt.Errorf("manifest %s: artifact %d is missing id, file or sha256", path, i)
		}
	}
	return &m, nil
}

func buildArtifacts(spec model.ServerSpec) []Artifact {
	var arts []Artifact
	if spec.Backup.Postgres != nil {
		for _, db := range spec.Backup.Postgres.Databases {
			arts = append(arts, &PostgresArtifact{
				DB:   db,
				User: spec.Backup.Postgres.User,
				Host: spec.Backup.Postgres.Host,
				Port: spec.Backup.Postgres.Port,
			})
		}
	}
	for _, p := range spec.Backup.Paths {
		arts = append(arts, &FilesArtifact{RemotePath: p})
	}
	return arts
}
