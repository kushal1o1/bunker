package cli

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

// noHints is set by the --no-hints persistent flag.
var noHints bool

// isTerminal reports whether stderr is an interactive terminal. Hints are for
// people; cron and CI output stays free of them. A var so tests can force it.
var isTerminal = func() bool {
	fi, err := os.Stderr.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// nextRun is the real command behind a dry run: same targets, no --dry-run.
func nextRun(targets []string, all bool) []string {
	if all {
		return []string{"backup", "run", "--all"}
	}
	return append([]string{"backup", "run"}, targets...)
}

// nextVerify checks the snapshot a backup run just wrote.
func nextVerify(server, snapshot string) []string {
	return []string{"backup", "verify", server, snapshot}
}

// printHint writes one copy-pasteable follow-up command to stderr, keeping
// stdout clean for pipes. It is silent off a terminal, with --no-hints, or
// with BUNKER_NO_HINTS set. A non-default -c is carried into the hint so the
// command runs against the same config.
func printHint(cmd *cobra.Command, args []string) {
	if noHints || os.Getenv("BUNKER_NO_HINTS") != "" || !isTerminal() {
		return
	}
	parts := []string{"bunker"}
	if f := cmd.Flag("config"); f != nil && f.Changed {
		parts = append(parts, "-c", f.Value.String())
	}
	parts = append(parts, args...)
	for i, p := range parts {
		parts[i] = shellQuote(p)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "\nNext: %s\n", strings.Join(parts, " "))
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_./:=@%+-]+$`)

// shellQuote returns s unchanged when it is safe to paste into a POSIX shell,
// otherwise single-quoted with embedded quotes escaped.
func shellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
