package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kushal1o1/bunker/internal/agent"
	"github.com/kushal1o1/bunker/internal/backup"
)

func TestAgentCheckFailsWhenS3CannotOpen(t *testing.T) {
	old := agentStore
	agentStore = func(*agent.Config) (backup.Store, agent.ServerFiles, error) {
		return nil, nil, errors.New("S3 credentials not found")
	}
	t.Cleanup(func() { agentStore = old })

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent.yaml")
	yml := "agent: {name: web-01}\nbackup: {paths: [" + dir + "]}\n" +
		"storage: {s3: {endpoint: https://s3.example.com, bucket: b}}\n" +
		"encryption: {recipient: age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p}\n"
	if err := os.WriteFile(cfgPath, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"-c", cfgPath, "agent", "check"})
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "1 check(s) failed") {
		t.Fatalf("err = %v, want one failed check", err)
	}
	for _, want := range []string{"Checking " + cfgPath + " (agent web-01)", "OK    path", "FAIL  s3", "S3 credentials not found"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestAgentInstallRejectsUserWithDocker(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"agent", "install", "--dry-run", "--docker", t.TempDir(), "--user", "bunker"})
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "--user is for the host agent") {
		t.Fatalf("err = %v, want --user refused with --docker", err)
	}
}

func TestAgentMissingDefaultConfigHintsAtC(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "agent.yaml")
	prev := defaultAgentConfig
	defaultAgentConfig = missing
	t.Cleanup(func() { defaultAgentConfig = prev })

	for _, args := range [][]string{{"agent", "run"}, {"agent", "check"}, {"agent", "install", "--dry-run"}} {
		cmd := newRootCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		err := cmd.ExecuteContext(context.Background())
		if err == nil || !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "use -c <path>") {
			t.Errorf("%v: err = %v, want the path and the -c hint", args, err)
		}
	}

	// With -c the path was chosen on purpose: no hint.
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"-c", missing, "agent", "check"})
	err := cmd.ExecuteContext(context.Background())
	if err == nil || strings.Contains(err.Error(), "use -c") {
		t.Errorf("with -c: err = %v, want no hint", err)
	}
}
