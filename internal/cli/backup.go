package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/kushal1o1/bunker/internal/backup"
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
	var runAll, dryRun bool
	cmd := &cobra.Command{
		Use:   "run",
		Short: "run backup for one or all servers",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBackup(cmd, args, runAll, dryRun)
		},
	}
	cmd.Flags().BoolVar(&runAll, "all", false, "backup every configured server")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan without backing up")
	return cmd
}

func runBackup(cmd *cobra.Command, args []string, runAll, dryRun bool) error {
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

	if dryRun {
		return printPlan(cfg, targets)
	}
	// Here we attach pipeline runner
	runner := &backup.Runner{
		Cfg:  cfg,
		Logf: func(format string, args ...any) { fmt.Printf(format+"\n", args...) },
	}

	for _, name := range targets {
		fmt.Printf("Backing up %s (%s)\n", name, cfg.Servers[name].Host)
		res, err := runner.Run(cmd.Context(), name)
		if err != nil {
			return err
		}
		fmt.Printf("  done: %s\n", res.Dir)
		fmt.Println()
	}
	return nil
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
				fmt.Fprintf(w, "    - postgres/%-30s\tpg_dump | zstd | age\n", db)
			}
		}
		for _, p := range s.Backup.Paths {
			fmt.Fprintf(w, "    - files/%-28s\ttar | zstd | age\n", strings.TrimPrefix(p, "/"))
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("Storage: " + cfg.Storage.Dir)
	fmt.Println("Encryption identity: " + cfg.Encryption.Identity)
	return nil
}