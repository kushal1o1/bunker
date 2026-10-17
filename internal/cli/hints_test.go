package cli

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/spf13/cobra"
)

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"kaskiserver":         "kaskiserver",
		"2026-10-17T09-12-03": "2026-10-17T09-12-03",
		"~/conf/prod.yaml":    "'~/conf/prod.yaml'",
		"my server":           "'my server'",
		"it's":                `'it'\''s'`,
		"":                    "''",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNextRun(t *testing.T) {
	if got, want := nextRun([]string{"a", "b"}, false), []string{"backup", "run", "a", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("nextRun names = %v, want %v", got, want)
	}
	if got, want := nextRun([]string{"a", "b"}, true), []string{"backup", "run", "--all"}; !reflect.DeepEqual(got, want) {
		t.Errorf("nextRun --all = %v, want %v", got, want)
	}
}

func TestNextVerify(t *testing.T) {
	got := nextVerify("web-01", "2026-10-17T09-12-03")
	want := []string{"backup", "verify", "web-01", "2026-10-17T09-12-03"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("nextVerify = %v, want %v", got, want)
	}
}

func TestPrintHint(t *testing.T) {
	args := nextVerify("web-01", "snap")
	cases := []struct {
		name    string
		tty     bool
		noHints bool
		env     string
		config  string // set -c when non-empty
		wantOut string
	}{
		{name: "terminal", tty: true, wantOut: "\nNext: bunker backup verify web-01 snap\n"},
		{name: "custom config", tty: true, config: "prod.yaml", wantOut: "\nNext: bunker -c prod.yaml backup verify web-01 snap\n"},
		{name: "not a terminal", tty: false},
		{name: "--no-hints", tty: true, noHints: true},
		{name: "BUNKER_NO_HINTS", tty: true, env: "1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origTTY, origNoHints := isTerminal, noHints
			t.Cleanup(func() { isTerminal, noHints = origTTY, origNoHints })
			isTerminal = func() bool { return tc.tty }
			noHints = tc.noHints
			t.Setenv("BUNKER_NO_HINTS", tc.env)

			cmd := &cobra.Command{Use: "test"}
			cmd.Flags().StringP("config", "c", "bunker.yaml", "")
			if tc.config != "" {
				if err := cmd.Flags().Set("config", tc.config); err != nil {
					t.Fatal(err)
				}
			}
			var buf bytes.Buffer
			cmd.SetErr(&buf)

			printHint(cmd, args)
			if got := buf.String(); got != tc.wantOut {
				t.Errorf("output = %q, want %q", got, tc.wantOut)
			}
		})
	}
}
