package backup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/kushal1o1/bunker/internal/model"
)

const ManifestName = "manifest.json"

type ArtifactEntry struct {
	ID     string `json:"id"`
	File   string `json:"file"`
	Size   int64  `json:"size_bytes"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Server       string          `json:"server"`
	Snapshot     string          `json:"snapshot"`
	CreatedAt    string          `json:"created_at"`
	Recipient    string          `json:"recipient"`
	Artifacts    []ArtifactEntry `json:"artifacts"`
}

func NewSnapshotID(t time.Time) string {
	return t.UTC().Format("2006-01-02T15-04-05")
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