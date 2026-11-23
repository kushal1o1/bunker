package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/kushal1o1/bunker/internal/backup"
	"github.com/kushal1o1/bunker/internal/retention"
	"github.com/spf13/cobra"
)

func newRetentionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "retention",
		Short: "prune old snapshots by the retention policy",
	}
	cmd.AddCommand(newRetentionApplyCmd())
	return cmd
}

func newRetentionApplyCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "apply [server...]",
		Short: "show which snapshots the policy keeps; delete the rest with --yes",
		Long: `Apply the retention policy (daily / weekly / monthly in bunker.yaml) to
stored snapshots. Without --yes it only prints KEEP / DELETE per snapshot and
removes nothing. The newest snapshot is always kept. A snapshot with no
manifest.json that is more than a day old is a run that never finished (a
crash, or an agent that cannot delete its own leftovers) and is deleted too;
a younger one may still be running and is skipped. A snapshot whose manifest
is corrupt is never deleted, so a human looks at it first. With both local
and S3 storage, a snapshot is deleted from both.`,
		Example: `  # preview every server
  bunker retention apply

  # delete what the policy drops for one server
  bunker retention apply my-vps --yes`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRetention(cmd, args, yes)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "actually delete; without it nothing is removed")
	return cmd
}

// retentionRow is one line of the table: a policy decision for a complete
// snapshot, or a skip for one without a valid manifest.
type retentionRow struct {
	id     string
	size   string
	action string
	why    string
}

// abandonedAfter is how old a snapshot without a manifest must be before
// retention treats it as a run that never finished. No run takes a day; a
// younger one may still be uploading.
const abandonedAfter = 24 * time.Hour

// abandoned reports whether a snapshot is leftover from a run that never
// finished: no copy has a manifest at all, and it is older than
// abandonedAfter. A corrupt manifest is not abandoned: it needs a human.
func abandoned(s snapRow, now time.Time) bool {
	for _, c := range []*backup.SnapshotInfo{s.local, s.remote} {
		if c != nil && c.Status != backup.NoManifest {
			return false
		}
	}
	return now.Sub(s.created) > abandonedAfter
}

// runRetention previews by default. Only complete snapshots take part in the
// policy. Snapshots without a manifest are deleted once abandoned; younger
// ones and those with a corrupt manifest are shown as skip.
func runRetention(cmd *cobra.Command, args []string, yes bool) error {
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

	policy := retention.Policy{
		Daily:   cfg.Retention.Daily,
		Weekly:  cfg.Retention.Weekly,
		Monthly: cfg.Retention.Monthly,
	}
	fmt.Printf("Policy: daily %d, weekly %d, monthly %d\n", policy.Daily, policy.Weekly, policy.Monthly)

	var toDelete, failed int
	var freed int64
	for _, name := range targets {
		fmt.Println()
		snaps, err := listMerged(ctx, stores, name)
		if err != nil {
			return err
		}
		if len(snaps) == 0 {
			fmt.Printf("%s: no snapshots\n", name)
			continue
		}

		// A snapshot takes part when at least one copy is complete; one
		// with no complete copy anywhere is left for a human to look at.
		var keep, del int
		var rows []retentionRow
		// remove marks row for deletion, and deletes s with --yes.
		remove := func(row *retentionRow, s snapRow) {
			del++
			toDelete++
			freed += s.size()
			row.action = "delete"
			if !yes {
				return
			}
			if err := deleteEverywhere(ctx, stores, name, s); err != nil {
				failed++
				row.action, row.why = "FAILED", err.Error()
			} else {
				row.action = "deleted"
			}
		}

		byID := map[string]snapRow{}
		var items []retention.Item
		now := time.Now()
		for _, s := range snaps {
			if s.complete() {
				byID[s.id] = s
				items = append(items, retention.Item{ID: s.id, Time: s.created})
				continue
			}
			row := retentionRow{id: s.id, size: "-", action: "skip", why: s.best().Status.String()}
			switch {
			case abandoned(s, now):
				row.why = "no manifest, older than a day: a run that never finished"
				remove(&row, s)
			case s.best().Status == backup.NoManifest:
				row.why = "no manifest yet: may still be running"
			}
			rows = append(rows, row)
		}

		for _, d := range retention.Apply(policy, items) {
			s := byID[d.ID]
			row := retentionRow{id: d.ID, size: backup.HumanBytes(s.size()), action: "keep", why: strings.Join(d.Reasons, ", ")}
			if d.Keep {
				keep++
			} else {
				remove(&row, s)
			}
			rows = append(rows, row)
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].id > rows[j].id })

		fmt.Printf("%s: keep %d, delete %d\n", name, keep, del)
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "  SNAPSHOT\tSIZE\tACTION\tWHY")
		for _, r := range rows {
			fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", r.id, r.size, r.action, r.why)
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}

	fmt.Println()
	switch {
	case toDelete == 0:
		fmt.Println("Nothing to delete.")
	case !yes:
		fmt.Printf("Preview only: %d snapshot(s), %s would be deleted. Nothing was removed.\n", toDelete, backup.HumanBytes(freed))
		printHint(cmd, append(append([]string{"retention", "apply"}, args...), "--yes"))
	case failed > 0:
		return fmt.Errorf("retention: %d of %d deletion(s) failed", failed, toDelete)
	default:
		fmt.Printf("Deleted %d snapshot(s), freed %s.\n", toDelete, backup.HumanBytes(freed))
	}
	return nil
}

// deleteEverywhere removes every copy of a snapshot. Copies are deleted
// independently, so one failing store does not leave the other untouched.
func deleteEverywhere(ctx context.Context, stores *backup.Stores, server string, s snapRow) error {
	var errs []error
	if s.local != nil {
		errs = append(errs, stores.Local.Delete(ctx, server, s.id))
	}
	if s.remote != nil {
		errs = append(errs, stores.Remote.Delete(ctx, server, s.id))
	}
	return errors.Join(errs...)
}
