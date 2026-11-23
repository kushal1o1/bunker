package agent_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kushal1o1/bunker/internal/agent"
	"github.com/kushal1o1/bunker/internal/backup"
	"github.com/kushal1o1/bunker/internal/execx"
)

// dataDir makes a folder with two readable files.
func dataDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	for _, f := range []string{"a.jpg", "sub/b.jpg"} {
		p := filepath.Join(d, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func runCheck(t *testing.T, cfg *agent.Config, files agent.ServerFiles) map[string][]agent.CheckResult {
	t.Helper()
	c := &agent.Checker{Cfg: cfg, Exec: execx.Local{}, Files: files, Where: "s3://b/", Version: "test"}
	byName := map[string][]agent.CheckResult{}
	for _, r := range c.Run(t.Context()) {
		byName[r.Name] = append(byName[r.Name], r)
	}
	return byName
}

func levels(rs []agent.CheckResult) string {
	var s []string
	for _, r := range rs {
		s = append(s, r.Level.String())
	}
	return strings.Join(s, ",")
}

func TestCheckAllGood(t *testing.T) {
	fakeTools(t, "")
	t.Setenv("PGPASSFILE", "")
	t.Setenv("HOME", t.TempDir()) // no ~/.pgpass
	cfg, _ := setup(t)
	cfg.Backup.Paths = []string{dataDir(t)}
	files := &memStatus{}

	got := runCheck(t, cfg, files)
	for name, want := range map[string]string{"config": "OK", "pg_dump": "OK", "postgres": "OK,OK", "path": "OK", "s3": "OK"} {
		if levels(got[name]) != want {
			t.Errorf("%s = %s %+v, want %s", name, levels(got[name]), got[name], want)
		}
	}
	if _, ok := got["pgpass"]; ok {
		t.Errorf("pgpass reported without a pgpass file: %+v", got["pgpass"])
	}
	if !strings.Contains(got["pg_dump"][0].Detail, "18.1") {
		t.Errorf("pg_dump detail = %q, want the version", got["pg_dump"][0].Detail)
	}
	if !strings.Contains(got["path"][0].Detail, "(2 file(s))") {
		t.Errorf("path detail = %q", got["path"][0].Detail)
	}
	if files.data["web-01/"+backup.CheckName] == nil {
		t.Error("check.json was not uploaded")
	}
	if files.data["web-01/"+backup.StatusName] != nil {
		t.Error("check must not touch status.json")
	}
}

func TestCheckReportsFailures(t *testing.T) {
	fakeTools(t, "app")
	cfg, _ := setup(t)
	cfg.Backup.Paths = []string{filepath.Join(t.TempDir(), "missing")}

	got := runCheck(t, cfg, failingFiles{})
	if levels(got["postgres"]) != "FAIL,FAIL" || !strings.Contains(got["postgres"][0].Detail, "connection to server failed") {
		t.Errorf("postgres = %+v, want both failing with pg_dump's error", got["postgres"])
	}
	if levels(got["path"]) != "FAIL" {
		t.Errorf("path = %+v, want FAIL", got["path"])
	}
	if levels(got["s3"]) != "FAIL" || !strings.Contains(got["s3"][0].Detail, "Access Denied") {
		t.Errorf("s3 = %+v, want FAIL with the upload error", got["s3"])
	}
}

type failingFiles struct{}

func (failingFiles) PutServerFile(context.Context, string, string, []byte) error {
	return errors.New("Access Denied")
}

func TestCheckWithoutPgDump(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	cfg, _ := setup(t)
	cfg.Backup.Paths = nil

	got := runCheck(t, cfg, &memStatus{})
	if levels(got["pg_dump"]) != "FAIL" {
		t.Errorf("pg_dump = %+v, want FAIL", got["pg_dump"])
	}
	if len(got["postgres"]) != 0 {
		t.Errorf("databases probed without pg_dump: %+v", got["postgres"])
	}
}

func TestCheckUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	fakeTools(t, "")
	cfg, _ := setup(t)
	d := dataDir(t)
	secret := filepath.Join(d, "sub", "b.jpg")
	if err := os.Chmod(secret, 0); err != nil {
		t.Fatal(err)
	}
	cfg.Backup.Paths = []string{d}

	got := runCheck(t, cfg, &memStatus{})
	if levels(got["path"]) != "FAIL" || !strings.Contains(got["path"][0].Detail, "b.jpg") {
		t.Errorf("path = %+v, want FAIL naming the file", got["path"])
	}
}

func TestCheckPgpassMode(t *testing.T) {
	fakeTools(t, "")
	cfg, _ := setup(t)
	cfg.Backup.Paths = nil
	pgpass := filepath.Join(t.TempDir(), "pgpass")
	if err := os.WriteFile(pgpass, []byte("shop-postgres:5432:*:bunker:x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGPASSFILE", pgpass)

	if got := runCheck(t, cfg, &memStatus{}); levels(got["pgpass"]) != "WARN" || !strings.Contains(got["pgpass"][0].Detail, "chmod 600") {
		t.Errorf("0644 pgpass = %+v, want WARN with the fix", got["pgpass"])
	}
	if err := os.Chmod(pgpass, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runCheck(t, cfg, &memStatus{}); levels(got["pgpass"]) != "OK" {
		t.Errorf("0600 pgpass = %+v, want OK", got["pgpass"])
	}
	t.Setenv("PGPASSFILE", filepath.Join(t.TempDir(), "nope"))
	if got := runCheck(t, cfg, &memStatus{}); levels(got["pgpass"]) != "FAIL" {
		t.Errorf("missing PGPASSFILE = %+v, want FAIL", got["pgpass"])
	}
}

func TestCheckPgpassWrongOwner(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	fakeTools(t, "")
	cfg, _ := setup(t)
	cfg.Backup.Paths = nil
	pgpass := filepath.Join(t.TempDir(), "pgpass")
	if err := os.WriteFile(pgpass, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Right mode, but this user cannot read it: what a file owned by another
	// uid looks like.
	if err := os.Chmod(pgpass, 0o200); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGPASSFILE", pgpass)
	got := runCheck(t, cfg, &memStatus{})
	if levels(got["pgpass"]) != "FAIL" || !strings.Contains(got["pgpass"][0].Detail, "chown") {
		t.Errorf("unreadable pgpass = %+v, want FAIL with the chown fix", got["pgpass"])
	}
}
