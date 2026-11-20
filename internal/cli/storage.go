package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/kushal1o1/bunker/internal/backup"
	"github.com/kushal1o1/bunker/internal/config"
	"github.com/spf13/cobra"
)

// storesFromConfig is backup.OpenStores; tests swap in in-memory stores.
var storesFromConfig = backup.OpenStores

// openStores builds the configured stores. With check set, it also confirms
// the S3 bucket is reachable, so a run that writes fails once, up front,
// instead of once per server.
func openStores(ctx context.Context, cfg *config.Config, check bool) (*backup.Stores, error) {
	stores, err := storesFromConfig(cfg)
	if err != nil {
		return nil, err
	}
	if check && stores.Remote != nil {
		if c, ok := stores.Remote.(interface{ Check(context.Context) error }); ok {
			if err := c.Check(ctx); err != nil {
				return nil, err
			}
		}
	}
	return stores, nil
}

// snapRow is one snapshot as seen across every configured store. local and
// remote are nil where that store has no copy.
type snapRow struct {
	id            string
	created       time.Time
	local, remote *backup.SnapshotInfo
}

// best is the copy to read metadata from: a complete one if any.
func (r snapRow) best() *backup.SnapshotInfo {
	for _, s := range []*backup.SnapshotInfo{r.local, r.remote} {
		if s != nil && s.Status == backup.Complete {
			return s
		}
	}
	if r.local != nil {
		return r.local
	}
	return r.remote
}

// complete reports whether at least one copy has a valid manifest.
func (r snapRow) complete() bool { return r.best().Status == backup.Complete }

func (r snapRow) size() int64 { return r.best().Size() }

// listMerged lists one server's snapshots in every store, newest first.
func listMerged(ctx context.Context, stores *backup.Stores, server string) ([]snapRow, error) {
	byID := map[string]*snapRow{}
	add := func(st backup.Store, remote bool) error {
		if st == nil {
			return nil
		}
		snaps, err := st.List(ctx, server)
		if err != nil {
			return err
		}
		for i := range snaps {
			s := &snaps[i]
			row, ok := byID[s.ID]
			if !ok {
				row = &snapRow{id: s.ID, created: s.Created}
				byID[s.ID] = row
			}
			if remote {
				row.remote = s
			} else {
				row.local = s
			}
		}
		return nil
	}
	if err := add(stores.Local, false); err != nil {
		return nil, err
	}
	if err := add(stores.Remote, true); err != nil {
		return nil, err
	}

	rows := make([]snapRow, 0, len(byID))
	for _, r := range byID {
		rows = append(rows, *r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].id > rows[j].id })
	return rows, nil
}

// copyStatus is one store's cell in a list table.
func copyStatus(s *backup.SnapshotInfo) string {
	if s == nil {
		return "-"
	}
	return s.Status.String()
}

// targetServers returns the named servers, or all of them, checking names.
func targetServers(cfg *config.Config, args []string) ([]string, error) {
	targets := dedupe(args)
	if len(targets) == 0 {
		targets = cfg.ServerNames()
	}
	for _, name := range targets {
		if _, ok := cfg.Servers[name]; !ok {
			return nil, fmt.Errorf("unknown server %q (try: bunker server list)", name)
		}
	}
	return targets, nil
}

// --- storage sync ------------------------------------------------------------

func newStorageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "storage",
		Short: "manage where snapshots are stored",
	}
	cmd.AddCommand(newStorageSyncCmd())
	return cmd
}

func newStorageSyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync [server...]",
		Short: "upload local snapshots that are missing from s3",
		Long: `Copy every complete local snapshot that S3 does not have yet, for example
after an upload failed or S3 was added to an existing setup. Artifacts go
first and manifest.json last, so a copy only counts once it is whole.
Incomplete local snapshots are skipped. Needs both storage.dir and storage.s3.`,
		Example: `  # every configured server
  bunker storage sync

  # one server
  bunker storage sync my-vps`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSync(cmd, args)
		},
	}
}

func runSync(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if cfg.Storage.Dir == "" || cfg.Storage.S3 == nil {
		return errors.New("storage sync copies local snapshots to S3: set both storage.dir and storage.s3 in bunker.yaml")
	}
	targets, err := targetServers(cfg, args)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	stores, err := openStores(ctx, cfg, true)
	if err != nil {
		return err
	}
	fmt.Printf("Syncing %s -> %s\n", stores.Local.Where(), stores.Remote.Where())

	var uploaded, failed int
	var bytes int64
	for _, name := range targets {
		fmt.Println()
		rows, err := listMerged(ctx, stores, name)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			fmt.Printf("%s: no snapshots\n", name)
			continue
		}

		fmt.Println(name)
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "  SNAPSHOT\tSIZE\tACTION")
		for _, r := range rows {
			action := ""
			switch {
			case r.local == nil:
				action = "s3 only"
			case r.local.Status != backup.Complete:
				action = "skip (local " + r.local.Status.String() + ")"
			case r.remote != nil && r.remote.Status == backup.Complete:
				action = "already in s3"
			default:
				if err := backup.CopySnapshot(ctx, stores.Local, stores.Remote, *r.local, name, nil); err != nil {
					failed++
					action = "FAILED: " + oneLine(err)
				} else {
					uploaded++
					bytes += r.local.Size()
					action = "uploaded"
				}
			}
			size := "-"
			if r.complete() {
				size = backup.HumanBytes(r.size())
			}
			fmt.Fprintf(w, "  %s\t%s\t%s\n", r.id, size, action)
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}

	fmt.Println()
	if failed > 0 {
		return fmt.Errorf("storage sync: %d snapshot(s) failed to upload", failed)
	}
	if uploaded == 0 {
		fmt.Println("Everything is already in S3.")
		return nil
	}
	fmt.Printf("Uploaded %d snapshot(s), %s.\n", uploaded, backup.HumanBytes(bytes))
	return nil
}
