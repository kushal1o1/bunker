package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/kushal1o1/bunker/internal/config"
	"github.com/spf13/cobra"
)

func loadConfig() (*config.Config, error) {
	return config.Load(configPath)
}

func newServerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server",
		Short: "manage configured servers",
	}
	cmd.AddCommand(newServerListCmd())
	return cmd
}

func newServerListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "list configured servers",
		Long: `Print every server in bunker.yaml with its host, SSH user and port, and
what it backs up, after defaults are applied. Reads the config only and
connects to nothing, so it is the first thing to run when a config is wrong.`,
		Example: `  bunker server list

  # a different config file
  bunker -c prod.yaml server list`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return printServers(cfg)
		},
	}
}

func printServers(cfg *config.Config) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tHOST\tSSH\tDB\tPATHS")
	for _, name := range cfg.ServerNames() {
		s := cfg.Servers[name]

		db := "-"
		if s.Backup.Postgres != nil {
			db = strings.Join(s.Backup.Postgres.Databases, ", ")
		}
		paths := "-"
		if len(s.Backup.Paths) > 0 {
			paths = strings.Join(s.Backup.Paths, ", ")
		}

		fmt.Fprintf(w, "%s\t%s\t%s (port %d)\t%s\t%s\n",
			name, s.Host, s.SSH.User, s.SSH.Port, db, paths)
	}
	return w.Flush()
}
