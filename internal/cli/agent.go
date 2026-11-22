package cli

import (
	"errors"

	"github.com/kushal1o1/bunker/internal/agent"
	"github.com/kushal1o1/bunker/internal/backup"
	"github.com/kushal1o1/bunker/internal/execx"
	"github.com/spf13/cobra"
)

// agentStore opens the agent's S3 store; tests swap in a fake.
var agentStore = func(cfg *agent.Config) (backup.Store, agent.StatusWriter, error) {
	s, err := backup.NewS3Store(*cfg.Storage.S3)
	if err != nil {
		return nil, nil, err
	}
	return s, s, nil
}

func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "back up this machine and push to s3",
	}
	cmd.AddCommand(newAgentRunCmd())
	return cmd
}

func newAgentRunCmd() *cobra.Command {
	var once bool
	cmd := &cobra.Command{
		Use:   "run",
		Short: "back up this machine to s3",
		Long: `Back up the databases and paths in agent.yaml on this machine, encrypt them
with encryption.recipient (the public key) and upload the snapshot to S3.
After every run, <name>/status.json in S3 records the result.

The agent never holds the private key and never deletes old snapshots: verify,
retention and restore run on your laptop. The config is
` + agent.DefaultConfigPath + ` unless -c is given.

--once runs one backup and exits with 1 if it failed.`,
		Example: `  # one backup now
  bunker agent run --once

  # with another config file
  bunker agent run --once -c ./agent.yaml`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !once {
				return errors.New("scheduled runs are not available yet: use --once (from cron or a systemd timer)")
			}
			p := configPath
			if !cmd.Flags().Changed("config") {
				p = agent.DefaultConfigPath
			}
			cfg, err := agent.Load(p)
			if err != nil {
				return err
			}
			store, status, err := agentStore(cfg)
			if err != nil {
				return err
			}
			r := &agent.Runner{
				Cfg:     cfg,
				Store:   store,
				Status:  status,
				Exec:    execx.Local{},
				Log:     cmd.OutOrStdout(),
				Version: version,
			}
			_, err = r.RunOnce(cmd.Context())
			return err
		},
	}
	cmd.Flags().BoolVar(&once, "once", false, "run one backup and exit")
	return cmd
}
