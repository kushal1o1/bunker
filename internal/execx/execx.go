// Package execx is where backup commands run: over SSH in pull mode, or on
// this machine in agent mode. The pipeline only sees Executor and Process,
// so artifacts, compression, encryption and storage do not care which.
package execx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// Executor starts one command and streams its stdout. argv is never passed
// through a shell by Local; remote executors quote it with ShellJoin.
type Executor interface {
	Exec(ctx context.Context, argv []string) (Process, error)
}

// Process is a running command.
//
// Read Stdout to the end before calling Wait. Wait returns the exit status,
// with stderr in the error when the command failed. Kill stops the command
// and is safe to call at any time, also after Wait.
type Process interface {
	Stdout() io.Reader
	Wait() error
	Kill() error
}

// ShellJoin quotes each argument for a POSIX shell and joins them with
// spaces. Plain arguments stay as they are; anything else is single-quoted,
// so $, backticks and spaces in a path are never expanded by the shell.
func ShellJoin(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = shellQuote(a)
	}
	return strings.Join(out, " ")
}

func shellQuote(s string) string {
	if s != "" && strings.Trim(s, safeChars) == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

const safeChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-"

// Local runs commands on this machine, without a shell.
type Local struct{}

func (Local) Exec(ctx context.Context, argv []string) (Process, error) {
	if len(argv) == 0 {
		return nil, errors.New("exec: empty command")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("cancelled: %w", err)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	p := &localProcess{ctx: ctx, cmd: cmd, name: argv[0]}
	p.stderr.Limit = MaxStderr
	cmd.Stderr = &p.stderr

	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	p.out = out
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", argv[0], err)
	}
	p.stop = context.AfterFunc(ctx, func() { _ = cmd.Process.Kill() })
	return p, nil
}

type localProcess struct {
	ctx    context.Context
	cmd    *exec.Cmd
	name   string
	out    io.Reader
	stderr LimitedBuffer
	stop   func() bool

	waitOnce sync.Once
	waitErr  error
}

// Stdout ends with the cancel reason instead of a clean EOF when ctx was
// cancelled, so a cut-off stream never looks complete.
func (p *localProcess) Stdout() io.Reader { return localReader{p} }

type localReader struct{ p *localProcess }

func (r localReader) Read(b []byte) (int, error) {
	n, err := r.p.out.Read(b)
	if err != nil {
		if cerr := r.p.ctx.Err(); cerr != nil {
			return n, fmt.Errorf("cancelled: %w", cerr)
		}
	}
	return n, err
}

func (p *localProcess) Wait() error {
	p.waitOnce.Do(func() {
		err := p.cmd.Wait()
		p.stop()
		p.waitErr = p.describe(err)
	})
	return p.waitErr
}

func (p *localProcess) describe(err error) error {
	if cerr := p.ctx.Err(); cerr != nil {
		return fmt.Errorf("cancelled: %w", cerr)
	}
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(p.stderr.String())
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if detail != "" {
			return fmt.Errorf("%s exit %d: %s", p.name, exitErr.ExitCode(), detail)
		}
		return fmt.Errorf("%s exit %d", p.name, exitErr.ExitCode())
	}
	return fmt.Errorf("%s failed: %v (%s)", p.name, err, detail)
}

// Kill stops the command if it is still running and reaps it.
func (p *localProcess) Kill() error {
	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	_ = p.Wait()
	return nil
}
