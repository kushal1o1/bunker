package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/kushal1o1/bunker/internal/agent"
	"github.com/kushal1o1/bunker/internal/backup"
	"github.com/spf13/cobra"
)

// serverFileReader reads an agent's status.json. *backup.S3Store has it.
type serverFileReader interface {
	GetServerFile(ctx context.Context, server, name string) ([]byte, error)
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status [server...]",
		Short: "show each server's latest snapshot and last agent run",
		Long: `One row per server: its newest complete snapshot, how old it is and its
size, and for servers with agent: true the result of the agent's last run
(read from <name>/status.json in S3). Nothing on the servers is contacted.

Exits 1 when a server needs attention: an agent's last run failed, or a
server has no complete snapshot. Run it from cron to get told.`,
		Example: `  # every server
  bunker status

  # some servers
  bunker status web-01 db-02`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd, args)
		},
	}
}

func runStatus(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	targets, err := targetServers(cfg, args)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	stores, err := openStores(ctx, cfg, false)
	if err != nil {
		return err
	}
	now := time.Now()

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SERVER\tMODE\tLAST SNAPSHOT\tAGE\tSIZE\tLAST RUN")
	attention := 0
	for _, name := range targets {
		isAgent := cfg.Servers[name].Agent
		mode, lastRun := "pull", "-"

		snap, age, size := "none", "-", "-"
		rows, err := listMerged(ctx, stores, name)
		if err != nil {
			return err
		}
		found := false
		for _, r := range rows {
			if r.complete() {
				snap, age, size = r.id, humanAge(now.Sub(r.created)), backup.HumanBytes(r.size())
				found = true
				break
			}
		}

		bad := !found
		if isAgent {
			mode = "agent"
			var failed bool
			lastRun, failed = agentLastRun(ctx, stores.Remote, name, now)
			bad = bad || failed
		}
		if bad {
			attention++
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", name, mode, snap, age, size, lastRun)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if attention > 0 {
		return fmt.Errorf("%d server(s) need attention", attention)
	}
	return nil
}

// agentLastRun describes an agent's last run from its status.json. failed
// is true when the run failed or the status cannot be read; a status that
// does not exist yet (the agent never ran) is not a failure by itself.
func agentLastRun(ctx context.Context, st backup.Store, name string, now time.Time) (desc string, failed bool) {
	r, ok := st.(serverFileReader)
	if !ok {
		return "-", false
	}
	data, err := r.GetServerFile(ctx, name, backup.StatusName)
	if errors.Is(err, fs.ErrNotExist) {
		return "no report yet", false
	}
	if err != nil {
		return "cannot read status: " + oneLine(err), true
	}
	var s agent.Status
	if err := json.Unmarshal(data, &s); err != nil {
		return "bad " + backup.StatusName + ": " + oneLine(err), true
	}
	when := humanAge(now.Sub(s.Finished)) + " ago"
	if s.OK {
		return "ok " + when, false
	}
	return "FAILED " + when + ": " + clip(oneLine(errors.New(s.Error)), 100), true
}

// humanAge is a short duration for a table: 45s, 12m, 6h, 2d4h.
func humanAge(d time.Duration) string {
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	days := int(d.Hours()) / 24
	if h := int(d.Hours()) % 24; h > 0 {
		return fmt.Sprintf("%dd%dh", days, h)
	}
	return fmt.Sprintf("%dd", days)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "..."
}
