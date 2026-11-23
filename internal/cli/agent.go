package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	cmd.AddCommand(newAgentRunCmd(), newAgentCheckCmd(), newAgentInstallCmd(), newAgentUninstallCmd())
	return cmd
}

func newAgentRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "back up this machine to s3 once",
		Long: `Back up the databases and paths in agent.yaml on this machine, encrypt them
with encryption.recipient (the public key) and upload the snapshot to S3.
After every run, <name>/status.json in S3 records the result. Exits 1 if the
backup failed.

It runs once and exits. Schedule it with a systemd timer or cron on the
server, as pull mode is scheduled on your machine.

The agent never holds the private key and never deletes old snapshots: verify,
retention and restore run on your laptop. The config is
` + agent.DefaultConfigPath + ` unless -c is given.`,
		Example: `  # one backup now
  bunker agent run

  # with another config file
  bunker agent run -c ./agent.yaml`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
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

func newAgentInstallCmd() *cobra.Command {
	var at, runAs, dockerDir string
	var dryRun, force bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "schedule the agent with a systemd timer or cron",
		Long: `Set up only the schedule for the agent, nothing else.

Host agent: write bunker-agent.service and bunker-agent.timer to
/etc/systemd/system (running this binary with agent.yaml as --user, reading
agent.env next to agent.yaml), then enable the timer.

Container agent (--docker <dir>): write /etc/cron.d/bunker-agent, which runs
docker compose run --rm bunker-agent in <dir>.

Both run daily at --at. agent.yaml is checked first. It does not create users,
set permissions or write agent.yaml or agent.env: see the Agent mode docs.
The files are plain systemd and cron files: edit them for any other schedule.
Existing files are never replaced without --force. Needs root, except with
--dry-run.`,
		Example: `  # host agent, every day at 03:00
  sudo bunker agent install

  # another time, and see the files first
  sudo bunker agent install --at 01:30 --dry-run

  # container agent in /opt/bunker-agent
  sudo bunker agent install --docker /opt/bunker-agent`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !dryRun && os.Geteuid() != 0 {
				return errors.New("agent install writes to /etc: run it with sudo (or use --dry-run)")
			}
			in := &agent.Installer{Out: cmd.OutOrStdout(), DryRun: dryRun, Force: force}
			if dockerDir != "" {
				if cmd.Flags().Changed("user") {
					return errors.New("--user is for the host agent; the container agent runs as uid 10001 (set user: in docker-compose.yml)")
				}
				dir, err := filepath.Abs(dockerDir)
				if err != nil {
					return err
				}
				return in.InstallCron(dir, at)
			}
			cfgPath, err := filepath.Abs(agentConfigPath(cmd))
			if err != nil {
				return err
			}
			bin, err := os.Executable()
			if err != nil {
				return err
			}
			if real, err := filepath.EvalSymlinks(bin); err == nil {
				bin = real
			}
			return in.InstallSystemd(agent.SystemdOptions{Config: cfgPath, Binary: bin, User: runAs, At: at})
		},
	}
	cmd.Flags().StringVar(&at, "at", "03:00", "daily time to run, HH:MM (server local time)")
	cmd.Flags().StringVar(&runAs, "user", "bunker", "user the backup runs as (host agent); must exist")
	cmd.Flags().StringVar(&dockerDir, "docker", "", "schedule the container agent in this folder with cron")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the files and commands, change nothing")
	cmd.Flags().BoolVar(&force, "force", false, "replace existing service, timer or cron files")
	return cmd
}

func newAgentUninstallCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "remove the agent's systemd timer or cron file",
		Long: `Undo bunker agent install: disable and stop the timer, then remove
bunker-agent.service, bunker-agent.timer and /etc/cron.d/bunker-agent,
whichever exist. agent.yaml, agent.env and the snapshots in S3 are not
touched. Needs root, except with --dry-run.`,
		Example: `  sudo bunker agent uninstall

  # see what it would remove
  bunker agent uninstall --dry-run`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !dryRun && os.Geteuid() != 0 {
				return errors.New("agent uninstall removes files in /etc: run it with sudo (or use --dry-run)")
			}
			in := &agent.Installer{Out: cmd.OutOrStdout(), DryRun: dryRun}
			return in.Uninstall()
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be removed, change nothing")
	return cmd
}
