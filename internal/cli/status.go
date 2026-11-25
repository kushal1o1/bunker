package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
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

	// Servers are checked in parallel, and each row is printed as soon as it
	// and every row above it are ready, so the table fills in config order.
	// Fixed column widths make that possible without buffering the table.
	nameWidth := len("SERVER")
	for _, name := range targets {
		nameWidth = max(nameWidth, len(name))
	}
	out := cmd.OutOrStdout()
	printRow := func(r statusRow) {
		fmt.Fprintf(out, "%-*s  %-5s  %-19s  %-6s  %-9s  %s\n", nameWidth, r.name, r.mode, r.snap, r.age, r.size, r.lastRun)
	}
	printRow(statusRow{name: "SERVER", mode: "MODE", snap: "LAST SNAPSHOT", age: "AGE", size: "SIZE", lastRun: "LAST RUN"})

	// A progress line on a terminal only; cron and pipes get just the table.
	progress := func(int) {}
	clearLine := func() {}
	if isTerminal() {
		errOut := cmd.ErrOrStderr()
		progress = func(done int) { fmt.Fprintf(errOut, "\rchecking %d/%d servers...", done, len(targets)) }
		clearLine = func() { fmt.Fprint(errOut, "\r\033[K") }
	}

	rows := make([]statusRow, len(targets))
	finished := make([]bool, len(targets))
	doneCh := make(chan int)
	sem := make(chan struct{}, cfg.Concurrency)
	for i, name := range targets {
		go func() {
			sem <- struct{}{}
			rows[i] = serverStatus(ctx, cfg.Servers[name].Agent, stores, name, now)
			<-sem
			doneCh <- i
		}()
	}

	attention, next := 0, 0
	progress(0)
	for done := 1; done <= len(targets); done++ {
		finished[<-doneCh] = true
		if finished[next] {
			clearLine()
			for next < len(targets) && finished[next] {
				printRow(rows[next])
				if rows[next].bad {
					attention++
				}
				next++
			}
		}
		if done < len(targets) {
			progress(done)
		}
	}
	if attention > 0 {
		return fmt.Errorf("%d server(s) need attention", attention)
	}
	return nil
}

type statusRow struct {
	name, mode, snap, age, size, lastRun string
	bad                                  bool
}

// serverStatus is one server's row. bad means it needs attention: no
// complete snapshot, a failed agent run, or snapshots that cannot be listed.
func serverStatus(ctx context.Context, isAgent bool, stores *backup.Stores, name string, now time.Time) statusRow {
	r := statusRow{name: name, mode: "pull", snap: "none", age: "-", size: "-", lastRun: "-"}
	if isAgent {
		r.mode = "agent"
	}
	snaps, err := listMerged(ctx, stores, name)
	if err != nil {
		r.snap, r.lastRun, r.bad = "-", "cannot list snapshots: "+clip(oneLine(err), 100), true
		return r
	}
	found := false
	for _, s := range snaps {
		if s.complete() {
			r.snap, r.age, r.size = s.id, humanAge(now.Sub(s.created)), backup.HumanBytes(s.size())
			found = true
			break
		}
	}
	r.bad = !found
	if isAgent {
		var failed bool
		r.lastRun, failed = agentLastRun(ctx, stores.Remote, name, now)
		r.bad = r.bad || failed
	}
	return r
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
