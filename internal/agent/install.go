package agent

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// Where `bunker agent install` writes, relative to Installer.Root.
const (
	ServicePath = "/etc/systemd/system/bunker-agent.service"
	TimerPath   = "/etc/systemd/system/bunker-agent.timer"
	CronPath    = "/etc/cron.d/bunker-agent"
	timerUnit   = "bunker-agent.timer"
)

// header starts every file install writes, so a reader knows where it came
// from and that editing it is fine.
const header = "# Written by `bunker agent install`. Edit it freely; `bunker agent uninstall` removes it.\n"

// Installer sets up only the schedule for an agent: a systemd service and
// timer, or a cron file for the container agent. Users, permissions,
// agent.yaml and agent.env are the admin's job (see docs/agent.md).
type Installer struct {
	// Root is prepended to every path written or checked: "/" normally, a
	// temp dir in tests.
	Root string
	// Run runs a command (systemctl) and returns its combined output. Nil
	// means run it for real.
	Run func(argv ...string) (string, error)
	// LookupUser reports whether a user exists. Nil means os/user.Lookup.
	LookupUser func(name string) error
	Out        io.Writer
	DryRun     bool
	Force      bool
}

// SystemdOptions is a host agent's schedule.
type SystemdOptions struct {
	Config string // agent.yaml, absolute
	Binary string // the bunker binary, absolute
	User   string // runs the backup; must exist
	At     string // daily time, HH:MM
}

// InstallSystemd writes the service and timer and enables the timer.
func (in *Installer) InstallSystemd(o SystemdOptions) error {
	hour, minute, err := ParseAt(o.At)
	if err != nil {
		return err
	}
	for _, p := range []string{o.Config, o.Binary} {
		if !filepath.IsAbs(p) || strings.ContainsAny(p, " \t\"'\\%") {
			return fmt.Errorf("%q: needs an absolute path without spaces, quotes or %%", p)
		}
	}
	cfg, err := Load(o.Config)
	if err != nil {
		return err
	}
	fmt.Fprintf(in.out(), "Config %s: ok (agent %s)\n", o.Config, cfg.Agent.Name)

	if !in.exists("/run/systemd/system") {
		return errors.New("systemd is not running on this machine: schedule `bunker agent run` with cron instead (see docs/agent.md)")
	}
	if err := in.lookupUser(o.User); err != nil {
		return fmt.Errorf("user %q: %w (create it first, see docs/agent.md)", o.User, err)
	}
	if !strings.HasPrefix(o.Binary, "/usr/") && !strings.HasPrefix(o.Binary, "/opt/") {
		fmt.Fprintf(in.out(), "warning: %s is outside /usr and /opt: user %s may not be able to run it. Move it to /usr/local/bin and run install from there, or edit ExecStart.\n", o.Binary, o.User)
	}

	envFile := filepath.Join(filepath.Dir(o.Config), "agent.env")
	service := header + fmt.Sprintf(`[Unit]
Description=bunker agent: back up this server to S3
Wants=network-online.target
After=network-online.target

[Service]
Type=oneshot
User=%s
ExecStart=%s agent run -c %s
# The upload-only S3 key. "-": a missing file is reported by the agent.
EnvironmentFile=-%s
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=read-only
PrivateTmp=yes
`, o.User, o.Binary, o.Config, envFile)

	timer := header + fmt.Sprintf(`[Unit]
Description=Run the bunker agent every day at %02d:%02d

[Timer]
OnCalendar=*-*-* %02d:%02d:00
# Run a missed backup after a reboot.
Persistent=true
# Spread servers out so they do not all upload at once.
RandomizedDelaySec=10m

[Install]
WantedBy=timers.target
`, hour, minute, hour, minute)

	files := []file{{ServicePath, service}, {TimerPath, timer}}
	if err := in.write(files); err != nil {
		return err
	}
	if err := in.run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := in.run("systemctl", "enable", "--now", timerUnit); err != nil {
		return err
	}
	if in.DryRun {
		return nil
	}
	if out, err := in.runner()("systemctl", "list-timers", timerUnit, "--no-pager"); err == nil {
		fmt.Fprintf(in.out(), "\n%s\n", strings.TrimSpace(out))
	}
	fmt.Fprintln(in.out(), "\nRun one now to test: sudo systemctl start bunker-agent && journalctl -u bunker-agent")
	return nil
}

// InstallCron writes /etc/cron.d/bunker-agent, which runs the container
// agent in dir with docker compose every day.
func (in *Installer) InstallCron(dir, at string) error {
	hour, minute, err := ParseAt(at)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(dir) || strings.ContainsAny(dir, " \t\"'\\%") {
		return fmt.Errorf("--docker %q: needs an absolute path without spaces, quotes or %%", dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); err != nil {
		return fmt.Errorf("--docker %s: no docker-compose.yml there (copy examples/agent/docker/ first)", dir)
	}
	cfg, err := Load(filepath.Join(dir, "agent.yaml"))
	if err != nil {
		return err
	}
	fmt.Fprintf(in.out(), "Config %s: ok (agent %s)\n", filepath.Join(dir, "agent.yaml"), cfg.Agent.Name)
	if !in.exists("/etc/cron.d") {
		return errors.New("/etc/cron.d does not exist: is cron installed?")
	}

	cron := header + fmt.Sprintf(`SHELL=/bin/sh
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
# Every day at %02d:%02d. flock skips a run while the previous one is still going.
%d %d * * * root cd %s && flock -n /run/bunker-agent.lock docker compose run --rm bunker-agent >> /var/log/bunker-agent.log 2>&1
`, hour, minute, minute, hour, dir)

	if err := in.write([]file{{CronPath, cron}}); err != nil {
		return err
	}
	if !in.DryRun {
		fmt.Fprintln(in.out(), "\nRun one now to test: cd "+dir+" && sudo docker compose run --rm bunker-agent")
	}
	return nil
}

// Uninstall disables the timer and removes what install wrote. agent.yaml,
// agent.env and everything in S3 stay.
func (in *Installer) Uninstall() error {
	var systemd, found bool
	for _, p := range []string{TimerPath, ServicePath} {
		if in.exists(p) {
			systemd, found = true, true
		}
	}
	if in.exists(TimerPath) {
		if err := in.run("systemctl", "disable", "--now", timerUnit); err != nil {
			return err
		}
	}
	for _, p := range []string{ServicePath, TimerPath, CronPath} {
		if !in.exists(p) {
			continue
		}
		found = true
		if in.DryRun {
			fmt.Fprintf(in.out(), "would remove %s\n", p)
			continue
		}
		if err := os.Remove(in.path(p)); err != nil {
			return err
		}
		fmt.Fprintf(in.out(), "removed %s\n", p)
	}
	if !found {
		fmt.Fprintln(in.out(), "Nothing to remove: no bunker agent service, timer or cron file.")
		return nil
	}
	if systemd {
		if err := in.run("systemctl", "daemon-reload"); err != nil {
			return err
		}
	}
	if !in.DryRun {
		fmt.Fprintln(in.out(), "The agent's config, env file and snapshots in S3 were not touched.")
	}
	return nil
}

// ParseAt parses a daily time like "03:00".
func ParseAt(at string) (hour, minute int, err error) {
	h, m, ok := strings.Cut(at, ":")
	if ok && len(h) >= 1 && len(h) <= 2 && len(m) == 2 && digits(h) && digits(m) {
		hour, err1 := strconv.Atoi(h)
		minute, err2 := strconv.Atoi(m)
		if err1 == nil && err2 == nil && hour >= 0 && hour <= 23 && minute >= 0 && minute <= 59 {
			return hour, minute, nil
		}
	}
	return 0, 0, fmt.Errorf("--at %q: want a daily time like 03:00", at)
}

func digits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

type file struct{ path, content string }

// write refuses to replace any existing file without Force, checking all of
// them before writing one, so a refusal leaves nothing half-installed.
func (in *Installer) write(files []file) error {
	if !in.Force {
		var existing []string
		for _, f := range files {
			if in.exists(f.path) {
				existing = append(existing, f.path)
			}
		}
		if len(existing) > 0 {
			return fmt.Errorf("already installed: %s (use --force to replace, or bunker agent uninstall)", strings.Join(existing, ", "))
		}
	}
	for _, f := range files {
		if in.DryRun {
			fmt.Fprintf(in.out(), "\nwould write %s:\n%s", f.path, f.content)
			continue
		}
		p := in.path(f.path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(f.content), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(in.out(), "wrote %s\n", f.path)
	}
	return nil
}

func (in *Installer) run(argv ...string) error {
	line := strings.Join(argv, " ")
	if in.DryRun {
		fmt.Fprintf(in.out(), "would run %s\n", line)
		return nil
	}
	if out, err := in.runner()(argv...); err != nil {
		return fmt.Errorf("%s: %w: %s", line, err, strings.TrimSpace(out))
	}
	fmt.Fprintf(in.out(), "ran %s\n", line)
	return nil
}

func (in *Installer) runner() func(argv ...string) (string, error) {
	if in.Run != nil {
		return in.Run
	}
	return func(argv ...string) (string, error) {
		out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
		return string(out), err
	}
}

func (in *Installer) lookupUser(name string) error {
	if in.LookupUser != nil {
		return in.LookupUser(name)
	}
	_, err := user.Lookup(name)
	return err
}

func (in *Installer) path(p string) string {
	root := in.Root
	if root == "" {
		root = "/"
	}
	return filepath.Join(root, p)
}

func (in *Installer) exists(p string) bool {
	_, err := os.Stat(in.path(p))
	return !errors.Is(err, fs.ErrNotExist)
}

func (in *Installer) out() io.Writer {
	if in.Out == nil {
		return io.Discard
	}
	return in.Out
}
