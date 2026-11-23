package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kushal1o1/bunker/internal/backup"
	"github.com/kushal1o1/bunker/internal/execx"
)

// Level is how bad one check result is. Only Fail makes `agent check` exit 1.
type Level int

const (
	OK Level = iota
	Warn
	Fail
)

func (l Level) String() string {
	return [...]string{"OK", "WARN", "FAIL"}[l]
}

// CheckResult is one line of `bunker agent check`.
type CheckResult struct {
	Level  Level
	Name   string
	Detail string
}

// probeTimeout bounds each pg_dump probe, so an unreachable host fails the
// check instead of hanging it.
const probeTimeout = 30 * time.Second

// maxPathProblems is how many unreadable entries are reported per path.
const maxPathProblems = 5

// Checker confirms an agent can do its job, without taking a backup: every
// database answers with the configured login, every path is readable, and
// S3 accepts an upload. Everything it runs is read-only.
type Checker struct {
	Cfg     *Config
	Exec    execx.Executor
	Files   ServerFiles
	Where   string // the S3 location, for messages
	Version string
}

// Run returns one result per check, in a fixed order.
func (c *Checker) Run(ctx context.Context) []CheckResult {
	var out []CheckResult
	add := func(l Level, name, format string, args ...any) {
		out = append(out, CheckResult{Level: l, Name: name, Detail: fmt.Sprintf(format, args...)})
	}

	add(OK, "config", "valid; encrypts to %s", c.Cfg.Encryption.Recipient)
	out = append(out, checkUser())

	if len(c.Cfg.Backup.Postgres) > 0 {
		out = append(out, c.checkPostgres(ctx)...)
		if r, ok := checkPgpass(); ok {
			out = append(out, r)
		}
	}
	for _, p := range c.Cfg.Backup.Paths {
		out = append(out, checkPath(p))
	}
	out = append(out, c.checkS3(ctx))
	return out
}

func checkUser() CheckResult {
	name := strconv.Itoa(os.Geteuid())
	if u, err := user.Current(); err == nil {
		name = u.Username + " (uid " + u.Uid + ")"
	}
	if os.Geteuid() == 0 {
		return CheckResult{Warn, "user", name + ": running as root; run the agent as a dedicated user with read-only grants"}
	}
	return CheckResult{OK, "user", name}
}

func (c *Checker) checkPostgres(ctx context.Context) []CheckResult {
	version, err := c.output(ctx, []string{"pg_dump", "--version"})
	if err != nil {
		return []CheckResult{{Fail, "pg_dump", "not usable: " + oneLine(err.Error())}}
	}
	out := []CheckResult{{OK, "pg_dump", strings.TrimSpace(version)}}

	for _, a := range c.Cfg.Artifacts() {
		pg, ok := a.(*backup.PostgresArtifact)
		if !ok {
			continue
		}
		name := pg.Group + "/" + pg.DB
		if _, err := c.output(ctx, pg.ProbeArgv()); err != nil {
			out = append(out, CheckResult{Fail, "postgres", name + ": " + oneLine(err.Error())})
			continue
		}
		out = append(out, CheckResult{OK, "postgres", name})
	}
	return out
}

// output runs argv to the end and returns its stdout. It is only used for
// small outputs (a version, a schema), and is bounded by probeTimeout.
func (c *Checker) output(ctx context.Context, argv []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	p, err := c.Exec.Exec(ctx, argv)
	if err != nil {
		return "", err
	}
	defer p.Kill()
	var b strings.Builder
	_, rerr := io.Copy(&b, p.Stdout())
	if err := p.Wait(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("no answer within %s", probeTimeout)
		}
		return "", err
	}
	return b.String(), rerr
}

// checkPgpass looks at the password file pg_dump will use. pg_dump silently
// ignores one that others can read, which shows up later as a puzzling
// login failure. ok is false when no file is in play (peer auth).
func checkPgpass() (CheckResult, bool) {
	p, explicit := os.Getenv("PGPASSFILE"), true
	if p == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return CheckResult{}, false
		}
		p, explicit = filepath.Join(home, ".pgpass"), false
	}
	info, err := os.Stat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist) && !explicit:
		return CheckResult{}, false
	case err != nil:
		return CheckResult{Fail, "pgpass", "PGPASSFILE=" + p + ": " + oneLine(err.Error())}, true
	case info.Mode().Perm()&0o077 != 0:
		return CheckResult{Warn, "pgpass", fmt.Sprintf("%s is readable by others (%#o): pg_dump ignores it; chmod 600 %s", p, info.Mode().Perm(), p)}, true
	}
	// The mode can be right while the owner is wrong (a file mounted into a
	// container keeps its host owner): then pg_dump cannot read it at all.
	f, err := os.Open(p)
	if err != nil {
		uid := os.Geteuid()
		return CheckResult{Fail, "pgpass", fmt.Sprintf("%s cannot be read by uid %d: %s (make it owned by uid %d; for a file mounted into a container, chown %d the file on the host)", p, uid, oneLine(err.Error()), uid, uid)}, true
	}
	f.Close()
	return CheckResult{OK, "pgpass", p}, true
}

// checkPath walks p the way tar will, and reports entries it cannot read.
func checkPath(p string) CheckResult {
	var files int
	var problems []string
	err := filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == p {
				return err
			}
			problems = append(problems, oneLine(err.Error()))
		} else if d.Type().IsRegular() {
			files++
			if f, err := os.Open(path); err != nil {
				problems = append(problems, oneLine(err.Error()))
			} else {
				f.Close()
			}
		}
		if len(problems) >= maxPathProblems {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return CheckResult{Fail, "path", p + ": " + oneLine(err.Error())}
	}
	if len(problems) > 0 {
		more := ""
		if len(problems) >= maxPathProblems {
			more = " (stopped at the first " + strconv.Itoa(maxPathProblems) + ")"
		}
		return CheckResult{Fail, "path", p + ": cannot read" + more + ": " + strings.Join(problems, "; ")}
	}
	return CheckResult{OK, "path", fmt.Sprintf("%s (%d file(s))", p, files)}
}

func (c *Checker) checkS3(ctx context.Context) CheckResult {
	if c.Files == nil {
		return CheckResult{Fail, "s3", "not configured"}
	}
	data, _ := json.MarshalIndent(map[string]any{
		"agent":   c.Cfg.Agent.Name,
		"version": c.Version,
		"checked": time.Now().UTC(),
	}, "", "  ")
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if err := c.Files.PutServerFile(ctx, c.Cfg.Agent.Name, backup.CheckName, append(data, '\n')); err != nil {
		return CheckResult{Fail, "s3", oneLine(err.Error())}
	}
	return CheckResult{OK, "s3", "uploaded " + strings.TrimSuffix(c.Where, "/") + "/" + c.Cfg.Agent.Name + "/" + backup.CheckName}
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
