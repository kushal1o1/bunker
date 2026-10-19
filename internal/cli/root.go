package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

var version = "0.2.0-dev"

var configPath string

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "bunker",
		Short:         "Backup orchestrator",
		Long:          "bunker - Backup orchestrator\n\nSSH into multiple VPS, dump PostgreSQL databases and media, then compress, encrypt, store and verify backups.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVarP(&configPath, "config", "c", "bunker.yaml", "config file path")
	root.PersistentFlags().BoolVar(&noHints, "no-hints", false, "do not print the suggested next command")
	root.AddCommand(newServerCmd(), newBackupCmd(), newRetentionCmd(), newVersionCmd(), newKeygenCmd())
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "version",
		Short:   "print version",
		Long:    "Print the bunker version. Same as bunker --version.",
		Example: `  bunker version`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Printf("bunker %s\n", version)
			return err
		},
	}
}

func Execute(args []string) error {
	return ExecuteContext(context.Background(), args)
}

func ExecuteContext(ctx context.Context, args []string) error {
	cmd := newRootCmd()
	cmd.SetArgs(args)
	return cmd.ExecuteContext(ctx)
}
