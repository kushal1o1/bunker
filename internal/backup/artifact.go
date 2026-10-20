package backup

import (
	"fmt"
	"path"
	"strconv"
	"strings"
)

// Artifact is one unit of backup data from a server. Every kind produces an
// encrypted, compressed archive file from bytes streamed over SSH. New kinds
// (MySQL, Mongo, arbitrary commands, ...) plug in by implementing this
// interface; the pipeline does not change.
type Artifact interface {
	ID() string
	// FilePath is the archive's path relative to the snapshot directory.
	FilePath() string
	// Command is the remote, read-only command whose stdout we stream.
	Command() string
}

type PostgresArtifact struct {
	DB string
	// Optional pg_dump connection overrides passed as -h/-p/-U.
	User, Host string
	Port       int
}

func (a *PostgresArtifact) ID() string {
	return "postgres/" + a.DB
}

func (a *PostgresArtifact) FilePath() string {
	return "postgres/" + a.DB + ".dump.zst.age"
}

func (a *PostgresArtifact) Command() string {
	args := []string{"pg_dump", "-Fc"}
	if a.Host != "" {
		args = append(args, "-h", a.Host)
	}
	if a.Port != 0 {
		args = append(args, "-p", strconv.Itoa(a.Port))
	}
	if a.User != "" {
		args = append(args, "-U", a.User)
	}
	args = append(args, a.DB)
	return strings.Join(quoteEach(args), " ")
}

type FilesArtifact struct {
	// RemotePath is the absolute path on the server to archive.
	RemotePath string
}

func (a *FilesArtifact) ID() string {
	return "files/" + strings.TrimPrefix(a.RemotePath, "/")
}

func (a *FilesArtifact) FilePath() string {
	safe := strings.ReplaceAll(strings.TrimPrefix(a.RemotePath, "/"), "/", "-")
	return "files/" + safe + ".tar.zst.age"
}

func (a *FilesArtifact) Command() string {
	parent, base := path.Dir(a.RemotePath), path.Base(a.RemotePath)
	return fmt.Sprintf("tar -C %s -cf - %s", quote(parent), quote(base))
}

func quote(s string) string {
	return strconv.Quote(s)
}

func quoteEach(s []string) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[i] = quote(v)
	}
	return out
}
