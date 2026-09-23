package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

const version = "0.1.0-dev"

var configPath string

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     "bunker",
		Short:   "Backup orchestrator",
		Long:    "bunker - Backup orchestrator\n\nSSH into multiple VPS, dump PostgreSQL databases and media, then compress, encrypt, store and verify backups.",
		Version: version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVarP(&configPath, "config", "c", "bunker.yaml", "config file path")
	root.AddCommand(newServerCmd(), newBackupCmd(), newVersionCmd())
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "print version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Printf("bunker %s\n", version)
			return err
		},
	}
}

func Execute(args []string) error {
	cmd := newRootCmd()
	cmd.SetArgs(args)
	return cmd.Execute()
}