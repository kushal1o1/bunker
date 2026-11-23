package backup

import (
	"path"
	"strconv"
	"strings"
)

// Artifact is one unit of backup data from a server. Every kind produces an
// encrypted, compressed archive file from the stdout of one command. New
// kinds (MySQL, Mongo, ...) plug in by implementing this interface; the
// pipeline does not change.
type Artifact interface {
	ID() string
	// FilePath is the archive's path relative to the snapshot directory.
	FilePath() string
	// Argv is the read-only command whose stdout we stream. It is run without
	// a shell locally, and quoted with execx.ShellJoin over SSH.
	Argv() []string
}

type PostgresArtifact struct {
	DB string
	// Optional pg_dump connection overrides passed as -h/-p/-U.
	User, Host string
	Port       int
	// Group, when set, is a folder between "postgres/" and the database, so
	// several Postgres servers can each have a database with the same name.
	// The agent sets it; pull mode leaves it empty.
	Group string
}

func (a *PostgresArtifact) name() string {
	if a.Group != "" {
		return "postgres/" + a.Group + "/" + a.DB
	}
	return "postgres/" + a.DB
}

func (a *PostgresArtifact) ID() string { return a.name() }

func (a *PostgresArtifact) FilePath() string { return a.name() + ".dump.zst.age" }

func (a *PostgresArtifact) Argv() []string {
	// -w: never prompt for a password. A missing password must fail the
	// run, not leave pg_dump waiting on a terminal nobody is watching.
	args := []string{"pg_dump", "-Fc", "-w"}
	if a.Host != "" {
		args = append(args, "-h", a.Host)
	}
	if a.Port != 0 {
		args = append(args, "-p", strconv.Itoa(a.Port))
	}
	if a.User != "" {
		args = append(args, "-U", a.User)
	}
	return append(args, a.DB)
}

// ProbeArgv is Argv with --schema-only: the same connection and login as a
// real backup, still read-only, but no data. `bunker agent check` uses it.
func (a *PostgresArtifact) ProbeArgv() []string {
	argv := a.Argv()
	return append([]string{argv[0], argv[1], "--schema-only"}, argv[2:]...)
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

func (a *FilesArtifact) Argv() []string {
	parent, base := path.Dir(a.RemotePath), path.Base(a.RemotePath)
	return []string{"tar", "-C", parent, "-cf", "-", base}
}
