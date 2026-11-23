package agent_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kushal1o1/bunker/internal/agent"
)

const installYAML = `agent: {name: web-01}
backup: {paths: [/var/www/media]}
storage: {s3: {endpoint: https://s3.example.com, bucket: b}}
encryption: {recipient: age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p}
`

// installer returns an Installer writing under a temp root that looks like a
// systemd machine with cron, and the commands it ran.
func installer(t *testing.T) (*agent.Installer, *[]string, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"run/systemd/system", "etc/cron.d"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var ran []string
	var out bytes.Buffer
	in := &agent.Installer{
		Root: root,
		Out:  &out,
		Run: func(argv ...string) (string, error) {
			ran = append(ran, strings.Join(argv, " "))
			return "", nil
		},
		LookupUser: func(name string) error {
			if name != "bunker" {
				return errors.New("unknown user")
			}
			return nil
		},
	}
	return in, &ran, &out
}

func agentConfig(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(p, []byte(installYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func read(t *testing.T, in *agent.Installer, p string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(in.Root, p))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func systemdOpts(cfg string) agent.SystemdOptions {
	return agent.SystemdOptions{Config: cfg, Binary: "/usr/local/bin/bunker", User: "bunker", At: "01:30"}
}

func TestInstallSystemd(t *testing.T) {
	in, ran, _ := installer(t)
	cfg := agentConfig(t)
	if err := in.InstallSystemd(systemdOpts(cfg)); err != nil {
		t.Fatal(err)
	}
	service := read(t, in, agent.ServicePath)
	for _, want := range []string{
		"User=bunker",
		"ExecStart=/usr/local/bin/bunker agent run -c " + cfg,
		"EnvironmentFile=-" + filepath.Join(filepath.Dir(cfg), "agent.env"),
		"Type=oneshot", "NoNewPrivileges=yes", "ProtectSystem=strict",
	} {
		if !strings.Contains(service, want) {
			t.Errorf("service missing %q:\n%s", want, service)
		}
	}
	timer := read(t, in, agent.TimerPath)
	for _, want := range []string{"OnCalendar=*-*-* 01:30:00", "Persistent=true", "WantedBy=timers.target"} {
		if !strings.Contains(timer, want) {
			t.Errorf("timer missing %q:\n%s", want, timer)
		}
	}
	got := strings.Join(*ran, "\n")
	want := "systemctl daemon-reload\nsystemctl enable --now bunker-agent.timer\nsystemctl list-timers bunker-agent.timer --no-pager"
	if got != want {
		t.Errorf("ran:\n%s\nwant:\n%s", got, want)
	}
}

func TestInstallWarnsAboutBinaryOutsideUsr(t *testing.T) {
	in, _, out := installer(t)
	o := systemdOpts(agentConfig(t))
	o.Binary = "/home/me/Downloads/bunker"
	if err := in.InstallSystemd(o); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "warning: /home/me/Downloads/bunker is outside /usr and /opt") {
		t.Errorf("no warning:\n%s", out.String())
	}
}

func TestInstallRefusesToReplace(t *testing.T) {
	in, ran, _ := installer(t)
	cfg := agentConfig(t)
	if err := in.InstallSystemd(systemdOpts(cfg)); err != nil {
		t.Fatal(err)
	}
	*ran = nil
	err := in.InstallSystemd(systemdOpts(cfg))
	if err == nil || !strings.Contains(err.Error(), "already installed") {
		t.Fatalf("second install: err = %v, want already installed", err)
	}
	if len(*ran) != 0 {
		t.Errorf("ran %v after refusing", *ran)
	}
	in.Force = true
	o := systemdOpts(cfg)
	o.At = "04:15"
	if err := in.InstallSystemd(o); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, in, agent.TimerPath), "04:15:00") {
		t.Error("--force did not replace the timer")
	}
}

func TestInstallSystemdRefusals(t *testing.T) {
	cases := []struct {
		name, want string
		change     func(*agent.Installer, *agent.SystemdOptions)
	}{
		{"unknown user", "create it first", func(_ *agent.Installer, o *agent.SystemdOptions) { o.User = "nobody-here" }},
		{"bad time", "want a daily time", func(_ *agent.Installer, o *agent.SystemdOptions) { o.At = "25:00" }},
		{"bad config", "agent.name", func(_ *agent.Installer, o *agent.SystemdOptions) {
			p := filepath.Join(filepath.Dir(o.Config), "bad.yaml")
			os.WriteFile(p, []byte(strings.Replace(installYAML, "web-01", "../x", 1)), 0o644)
			o.Config = p
		}},
		{"no systemd", "systemd is not running", func(in *agent.Installer, _ *agent.SystemdOptions) {
			os.RemoveAll(filepath.Join(in.Root, "run"))
		}},
		{"path with space", "without spaces", func(_ *agent.Installer, o *agent.SystemdOptions) { o.Binary = "/opt/my tools/bunker" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, ran, _ := installer(t)
			o := systemdOpts(agentConfig(t))
			tc.change(in, &o)
			err := in.InstallSystemd(o)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
			if _, err := os.Stat(filepath.Join(in.Root, agent.ServicePath)); err == nil {
				t.Error("wrote the service despite the error")
			}
			if len(*ran) != 0 {
				t.Errorf("ran %v despite the error", *ran)
			}
		})
	}
}

func TestInstallDryRunChangesNothing(t *testing.T) {
	in, ran, out := installer(t)
	in.DryRun = true
	if err := in.InstallSystemd(systemdOpts(agentConfig(t))); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(in.Root, agent.ServicePath)); err == nil {
		t.Error("dry run wrote the service")
	}
	if len(*ran) != 0 {
		t.Errorf("dry run ran %v", *ran)
	}
	for _, want := range []string{"would write " + agent.ServicePath, "ExecStart=", "would run systemctl enable --now bunker-agent.timer"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry run output missing %q:\n%s", want, out.String())
		}
	}
}

func TestInstallCron(t *testing.T) {
	in, ran, _ := installer(t)
	dir := t.TempDir()
	for name, body := range map[string]string{"docker-compose.yml": "services: {}\n", "agent.yaml": installYAML} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := in.InstallCron(dir, "02:05"); err != nil {
		t.Fatal(err)
	}
	cron := read(t, in, agent.CronPath)
	want := "5 2 * * * root cd " + dir + " && flock -n /run/bunker-agent.lock docker compose run --rm bunker-agent"
	if !strings.Contains(cron, want) {
		t.Errorf("cron file missing %q:\n%s", want, cron)
	}
	if len(*ran) != 0 {
		t.Errorf("cron install ran %v", *ran)
	}

	empty := t.TempDir()
	if err := in.InstallCron(empty, "02:05"); err == nil || !strings.Contains(err.Error(), "no docker-compose.yml") {
		t.Errorf("folder without compose: err = %v", err)
	}
}

func TestUninstall(t *testing.T) {
	in, ran, out := installer(t)
	if err := in.InstallSystemd(systemdOpts(agentConfig(t))); err != nil {
		t.Fatal(err)
	}
	cron := filepath.Join(in.Root, agent.CronPath)
	if err := os.WriteFile(cron, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(in.Root, "etc/systemd/system/other.service")
	if err := os.WriteFile(other, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	*ran = nil

	if err := in.Uninstall(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{agent.ServicePath, agent.TimerPath, agent.CronPath} {
		if _, err := os.Stat(filepath.Join(in.Root, p)); err == nil {
			t.Errorf("%s still there", p)
		}
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("uninstall removed a file that is not ours")
	}
	if got := strings.Join(*ran, "\n"); got != "systemctl disable --now bunker-agent.timer\nsystemctl daemon-reload" {
		t.Errorf("ran:\n%s", got)
	}

	out.Reset()
	if err := in.Uninstall(); err != nil {
		t.Fatalf("second uninstall: %v", err)
	}
	if !strings.Contains(out.String(), "Nothing to remove") {
		t.Errorf("second uninstall output: %s", out.String())
	}
}

func TestParseAt(t *testing.T) {
	good := map[string][2]int{"03:00": {3, 0}, "3:00": {3, 0}, "23:59": {23, 59}, "00:00": {0, 0}}
	for in, want := range good {
		h, m, err := agent.ParseAt(in)
		if err != nil || h != want[0] || m != want[1] {
			t.Errorf("ParseAt(%q) = %d, %d, %v", in, h, m, err)
		}
	}
	for _, in := range []string{"", "3", "24:00", "12:60", "12:5", "ab:cd", "-1:00", "03:00:00", "+3:00", "-0:00", "3:+5"} {
		if _, _, err := agent.ParseAt(in); err == nil {
			t.Errorf("ParseAt(%q) accepted", in)
		}
	}
}
