package cli

import (
	"context"
	"errors"
	"fmt"
	"text/tabwriter"

	"github.com/kushal1o1/bunker/internal/agent"
	"github.com/kushal1o1/bunker/internal/backup"
	"github.com/kushal1o1/bunker/internal/execx"
	"github.com/spf13/cobra"
)

// agentStore opens the agent's S3 store; tests swap in a fake.
var agentStore = func(cfg *agent.Config) (backup.Store, agent.ServerFiles, error) {
	s, err := backup.NewS3Store(*cfg.Storage.S3)
	if err != nil {
		return nil, nil, err
	}
	return s, s, nil
}

// agentConfigPath is -c when given, else the agent's default location.
func agentConfigPath(cmd *cobra.Command) string {
	if cmd.Flags().Changed("config") {
		return configPath
	}
	return agent.DefaultConfigPath
}

func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "back up this machine and push to s3",
	}
	cmd.AddCommand(newAgentRunCmd(), newAgentCheckCmd())
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
			cfg, err := agent.Load(agentConfigPath(cmd))
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

func newAgentCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "confirm this machine is ready for agent backups",
		Long: `Check everything an agent run needs, without taking a backup: the config,
the user it runs as, pg_dump and a login to every database (with
--schema-only, so no data is read), the pgpass file's mode, read access to
every file under each path, and an upload to S3 (it writes <name>/check.json).

Run it as the same user, or in the same container, as the agent. Exits 1 if
any check fails; warnings do not fail.`,
		Example: `  # before the first run
  bunker agent check

  # inside the agent container
  docker compose run --rm bunker-agent agent check`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p := agentConfigPath(cmd)
			cfg, err := agent.Load(p)
			if err != nil {
				return err
			}
			var files agent.ServerFiles
			_, f, err := agentStore(cfg)
			if err != nil {
				files = failedFiles{err}
			} else {
				files = f
			}
			c := &agent.Checker{
				Cfg:     cfg,
				Exec:    execx.Local{},
				Files:   files,
				Where:   "s3://" + cfg.Storage.S3.Bucket + "/" + cfg.Storage.S3.Prefix,
				Version: version,
			}
			results := c.Run(cmd.Context())

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Checking %s (agent %s)\n\n", p, cfg.Agent.Name)
			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			failed := 0
			for _, r := range results {
				if r.Level == agent.Fail {
					failed++
				}
				fmt.Fprintf(w, "  %s\t%s\t%s\n", r.Level, r.Name, r.Detail)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			fmt.Fprintln(out)
			if failed > 0 {
				return fmt.Errorf("%d check(s) failed", failed)
			}
			fmt.Fprintln(out, "All checks passed.")
			return nil
		},
	}
}

// failedFiles reports why S3 could not be opened as the s3 check's result.
type failedFiles struct{ err error }

func (f failedFiles) PutServerFile(context.Context, string, string, []byte) error { return f.err }
