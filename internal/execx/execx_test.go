package execx

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestShellJoin(t *testing.T) {
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"pg_dump", "-Fc", "-U", "bunker", "db"}, "pg_dump -Fc -U bunker db"},
		{[]string{"tar", "-C", "/var/www", "-cf", "-", "up loads"}, "tar -C /var/www -cf - 'up loads'"},
		{[]string{"echo", "$HOME", "`id`", "a;b"}, "echo '$HOME' '`id`' 'a;b'"},
		{[]string{"echo", "it's"}, `echo 'it'\''s'`},
		{[]string{"echo", ""}, "echo ''"},
	}
	for _, tc := range cases {
		if got := ShellJoin(tc.argv); got != tc.want {
			t.Errorf("ShellJoin(%q) = %q, want %q", tc.argv, got, tc.want)
		}
	}
}

// ShellJoin output, run by a real shell, must give back the same argv.
func TestShellJoinRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	args := []string{"plain", "with space", "$HOME", "`id`", "it's", "a;b|c", "", "*"}
	script := `for a in "$@"; do printf '%s\n' "[$a]"; done`
	line := "sh -c " + ShellJoin([]string{script}) + " sh " + ShellJoin(args)
	out, err := exec.Command("sh", "-c", line).Output()
	if err != nil {
		t.Fatal(err)
	}
	var want strings.Builder
	for _, a := range args {
		want.WriteString("[" + a + "]\n")
	}
	if string(out) != want.String() {
		t.Errorf("round trip:\n got %q\nwant %q", out, want.String())
	}
}

func needSh(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
}

func TestLocalStreamsStdout(t *testing.T) {
	needSh(t)
	p, err := Local{}.Exec(t.Context(), []string{"sh", "-c", "printf data; printf noise >&2"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	out, err := io.ReadAll(p.Stdout())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if string(out) != "data" {
		t.Errorf("stdout = %q, want %q", out, "data")
	}
}

func TestLocalReportsExitCodeAndStderr(t *testing.T) {
	needSh(t)
	p, err := Local{}.Exec(t.Context(), []string{"sh", "-c", "echo 'pg_dump: connection refused' >&2; exit 3"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	io.Copy(io.Discard, p.Stdout())
	err = p.Wait()
	if err == nil || !strings.Contains(err.Error(), "sh exit 3: pg_dump: connection refused") {
		t.Fatalf("Wait = %v, want exit code and stderr", err)
	}
}

func TestLocalMissingCommand(t *testing.T) {
	_, err := Local{}.Exec(t.Context(), []string{"bunker-no-such-command"})
	if err == nil || !strings.Contains(err.Error(), "start bunker-no-such-command") {
		t.Fatalf("err = %v, want a start error naming the command", err)
	}
}

func TestLocalRunsWithoutShell(t *testing.T) {
	needSh(t)
	// Passed as one argument, never parsed: no expansion, no second command.
	p, err := Local{}.Exec(t.Context(), []string{"printf", "%s", "$HOME; echo hacked"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()
	out, _ := io.ReadAll(p.Stdout())
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	if string(out) != "$HOME; echo hacked" {
		t.Errorf("stdout = %q, want the argument unchanged", out)
	}
}

func TestLocalCancelStopsCommand(t *testing.T) {
	needSh(t)
	ctx, cancel := context.WithCancel(t.Context())
	p, err := Local{}.Exec(ctx, []string{"sh", "-c", "printf start; exec sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()

	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	_, rerr := io.ReadAll(p.Stdout())
	werr := p.Wait()
	if time.Since(start) > 5*time.Second {
		t.Fatal("cancel did not stop the command")
	}
	if rerr == nil || !strings.Contains(rerr.Error(), "cancelled") {
		t.Errorf("read err = %v, want cancelled (a cut stream must not look complete)", rerr)
	}
	if werr == nil || !strings.Contains(werr.Error(), "cancelled") {
		t.Errorf("Wait = %v, want cancelled", werr)
	}
}

func TestLocalKillWithoutWait(t *testing.T) {
	needSh(t)
	p, err := Local{}.Exec(t.Context(), []string{"sleep", "30"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- p.Kill() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Kill: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Kill hung")
	}
	if err := p.Kill(); err != nil {
		t.Errorf("second Kill: %v", err)
	}
}

func TestLimitedBufferKeepsPrefixAndCountsDropped(t *testing.T) {
	l := LimitedBuffer{Limit: 10}
	if _, err := l.Write([]byte("0123456789ABCDEF")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !strings.HasPrefix(l.String(), "0123456789") {
		t.Errorf("String() = %q, want it to start with the first 10 bytes", l.String())
	}
	if l.Dropped() != 6 {
		t.Errorf("dropped = %d, want 6", l.Dropped())
	}
	if !strings.Contains(l.String(), "truncated") {
		t.Errorf("String() = %q, want a truncation note", l.String())
	}
}
