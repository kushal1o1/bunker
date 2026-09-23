package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/kushal1o1/bunker/internal/config"
	"github.com/spf13/cobra"
)

func newBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "manage backups",
	}
	cmd.AddCommand(newBackupRunCmd())
	return cmd
}

func newBackupRunCmd() *cobra.Command {
	var runAll bool
	cmd := &cobra.Command{
		Use:   "run",
		Short: "run backup for one or all servers",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBackup(args, runAll)
		},
	}
	cmd.Flags().BoolVar(&runAll, "all", false, "backup every configured server")
	return cmd
}

func runBackup(args []string, runAll bool) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	var targets []string
	switch {
	case runAll && len(args) > 0:
		return errors.New("cannot combine --all with specific server names")
	case runAll:
		targets = cfg.ServerNames()
	case len(args) == 0:
		return errors.New("missing target: supply a server name or --all")
	default:
		targets = args
	}

	for _, name := range targets {
		if _, ok := cfg.Servers[name]; !ok {
			return fmt.Errorf("unknown server %q (try: bunker server list)", name)
		}
	}

	return printPlan(cfg, targets)
}

func printPlan(cfg *config.Config, targets []string) error {
	plural := "s"
	if len(targets) == 1 {
		plural = ""
	}
	fmt.Printf("Backup plan (%d server%s):\n", len(targets), plural)

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for _, name := range targets {
		s := cfg.Servers[name]
		fmt.Fprintf(w, "  %s (%s)\n", name, s.Host)

		if s.Backup.Postgres != nil {
			for _, db := range s.Backup.Postgres.Databases {
				fmt.Fprintf(w, "    - postgres/%-22s\tpg_dump | zstd | age\n", db)
			}
		}
		for _, dir := range s.Backup.Media {
			fmt.Fprintf(w, "    - media/%-23s\ttar | zstd | age\n", strings.TrimPrefix(dir, "/"))
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("This is a dry-run stub.:)")
	return nil
}