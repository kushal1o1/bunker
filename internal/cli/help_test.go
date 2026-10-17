package cli

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// commandsDoc is the hand-written command reference, relative to this package.
const commandsDoc = "../../docs/COMMANDS.md"

// leafCommands returns every runnable bunker command, skipping cobra's
// built-ins. Group commands (backup, server, retention) only hold
// subcommands and need no examples of their own.
func leafCommands(root *cobra.Command) []*cobra.Command {
	var out []*cobra.Command
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if sub.Name() == "help" || sub.Name() == "completion" {
				continue
			}
			if sub.Runnable() {
				out = append(out, sub)
			}
			walk(sub)
		}
	}
	walk(root)
	return out
}

// name is the command path without the leading "bunker", e.g. "backup run".
func name(c *cobra.Command) string {
	return strings.TrimPrefix(c.CommandPath(), "bunker ")
}

// TestCommandsFollowHelpStandard enforces the help standard: every command
// has Short, Long and Example, Short is lowercase with no trailing period .
func TestCommandsFollowHelpStandard(t *testing.T) {
	cmds := leafCommands(newRootCmd())
	if len(cmds) == 0 {
		t.Fatal("found no commands")
	}
	for _, c := range cmds {
		n := name(c)
		if c.Short == "" || c.Long == "" || c.Example == "" {
			t.Errorf("%s: Short, Long and Example are all required (got short=%t long=%t example=%t)",
				n, c.Short != "", c.Long != "", c.Example != "")
		}
		if s := c.Short; s != "" && (strings.ToLower(s[:1]) != s[:1] || strings.HasSuffix(s, ".")) {
			t.Errorf("%s: Short %q must start lowercase and not end with a period", n, s)
		}
		if strings.Contains(c.Short+c.Long+c.Example, "—") {
			t.Errorf("%s: help text contains an em dash", n)
		}
	}
}

// TestCommandsAreDocumented fails when a command has no "## <command>"
// section in docs/COMMANDS.md, so a new command cannot ship without docs.
func TestCommandsAreDocumented(t *testing.T) {
	data, err := os.ReadFile(commandsDoc)
	if err != nil {
		t.Fatalf("read %s: %v", commandsDoc, err)
	}
	doc := string(data)
	for _, c := range leafCommands(newRootCmd()) {
		n := name(c)
		heading := regexp.MustCompile(`(?m)^## ` + regexp.QuoteMeta(n) + `\s*$`)
		if !heading.MatchString(doc) {
			t.Errorf("%s: no \"## %s\" section in docs/COMMANDS.md", n, n)
		}
	}
}
