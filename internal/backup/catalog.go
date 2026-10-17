package backup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// SnapshotStatus says whether a stored snapshot is complete. It reflects the
// manifest only; proving the artifacts are readable is Verify's job.
type SnapshotStatus int

const (
	// Complete: manifest.json is present and passes ReadManifest.
	Complete SnapshotStatus = iota
	// NoManifest: the directory has no manifest. Run deletes a failed
	// snapshot, so this is what a killed process leaves behind.
	NoManifest
	// BadManifest: a manifest exists but cannot be parsed or is incomplete.
	BadManifest
)

func (s SnapshotStatus) String() string {
	switch s {
	case Complete:
		return "complete"
	case NoManifest:
		return "no manifest"
	case BadManifest:
		return "bad manifest"
	default:
		return "unknown"
	}
}

// SnapshotInfo describes one snapshot directory in local storage.
type SnapshotInfo struct {
	ID       string
	Created  time.Time // parsed from ID, UTC
	Status   SnapshotStatus
	Manifest *Manifest // nil unless Complete
	Err      error     // why the manifest is bad, for BadManifest
}

// Size is the total recorded artifact size, or 0 without a valid manifest.
func (s SnapshotInfo) Size() int64 {
	if s.Manifest == nil {
		return 0
	}
	return totalSize(s.Manifest.Artifacts)
}

// ListSnapshots returns the snapshots stored for one server, newest first.
// Only directories named like a snapshot ID are considered; anything else in
// the server folder is ignored. A server with no folder yet has no snapshots,
// which is not an error.
func ListSnapshots(storageDir, server string) ([]SnapshotInfo, error) {
	dir := filepath.Join(storageDir, server)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}

	var out []SnapshotInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		created, err := time.Parse(snapshotLayout, e.Name())
		if err != nil {
			continue
		}

		info := SnapshotInfo{ID: e.Name(), Created: created}
		man, err := ReadManifest(ManifestPath(filepath.Join(dir, e.Name())))
		switch {
		case err == nil:
			info.Status, info.Manifest = Complete, man
		case errors.Is(err, fs.ErrNotExist):
			info.Status = NoManifest
		default:
			info.Status, info.Err = BadManifest, err
		}
		out = append(out, info)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// DeleteSnapshot removes one snapshot directory. The ID must be a real
// snapshot ID, so a bad value can never turn into a path outside the
// server's folder (e.g. "..", "" or "../other").
func DeleteSnapshot(storageDir, server, snapshot string) error {
	if _, err := time.Parse(snapshotLayout, snapshot); err != nil {
		return fmt.Errorf("refusing to delete %q: not a snapshot id", snapshot)
	}
	if server == "" || server != filepath.Base(server) || server == "." || server == ".." {
		return fmt.Errorf("refusing to delete from server %q: not a plain name", server)
	}
	if err := os.RemoveAll(SnapshotDir(storageDir, server, snapshot)); err != nil {
		return fmt.Errorf("delete %s/%s: %w", server, snapshot, err)
	}
	return nil
}
