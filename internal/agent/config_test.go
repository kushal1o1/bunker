package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testRecipient = "age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p"

const validConfig = `
agent:
  name: web-01
  schedule: "0 3 * * *"
backup:
  postgres:
    - host: shop-postgres
      dbs: [app]
      user: bunker
    - host: /var/run/postgresql
      dbs: [legacy_db]
  paths: [/data/shop/uploads/]
storage:
  s3:
    endpoint: https://s3.example.com/
    bucket: my-bunker
    prefix: /prod
encryption:
  recipient: ` + testRecipient + `
`

func load(t *testing.T, yml string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(p, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestLoadValidConfig(t *testing.T) {
	cfg, err := load(t, validConfig)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.S3.Endpoint != "https://s3.example.com" || cfg.Storage.S3.Prefix != "prod/" {
		t.Errorf("s3 not normalized: %+v", cfg.Storage.S3)
	}
	var got []string
	for _, a := range cfg.Artifacts() {
		got = append(got, a.FilePath())
	}
	want := []string{
		"postgres/shop-postgres/app.dump.zst.age",
		"postgres/var-run-postgresql/legacy_db.dump.zst.age",
		"files/data-shop-uploads.tar.zst.age",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("artifacts:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestLoadRejects(t *testing.T) {
	cases := []struct {
		name, from, to, want string
	}{
		{"private key", "encryption:\n", "encryption:\n  identity: ~/.config/bunker/key.txt\n", "never hold the private key"},
		{"no recipient", "  recipient: " + testRecipient, "", "encryption.recipient is required"},
		{"bad recipient", testRecipient, "age1nope", "not an age public key"},
		{"local storage", "storage:\n", "storage:\n  dir: /backups\n", "S3 only"},
		{"no s3", "  s3:\n    endpoint: https://s3.example.com/\n    bucket: my-bunker\n    prefix: /prod\n", "", "storage.s3 is required"},
		{"bad name", "name: web-01", "name: ../web", "agent.name"},
		{"retention", "storage:\n", "retention:\n  daily: 7\nstorage:\n", "field retention not found"},
		{"no dbs", "      dbs: [legacy_db]\n", "", "dbs is required"},
		{"db twice", "dbs: [app]", "dbs: [app, app]", "listed twice"},
		{"db with slash", "dbs: [app]", "dbs: [a/b]", "invalid database name"},
		{"same postgres", "host: /var/run/postgresql", "host: shop-postgres", "same Postgres"},
		{"relative path", "/data/shop/uploads/", "data", "absolute path"},
		{"root path", "/data/shop/uploads/", "/", "refusing to archive /"},
		{"path twice", "[/data/shop/uploads/]", "[/data/shop/uploads/, /data/shop/uploads]", "listed twice"},
		{"bad port", "      user: bunker\n", "      port: 70000\n", "port out of range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(validConfig, tc.from) {
				t.Fatalf("test setup: %q not in config", tc.from)
			}
			_, err := load(t, strings.Replace(validConfig, tc.from, tc.to, 1))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestLoadNeedsSomethingToBackUp(t *testing.T) {
	yml := "agent: {name: web-01}\nstorage: {s3: {endpoint: https://s3.example.com, bucket: b}}\nencryption: {recipient: " + testRecipient + "}\n"
	if _, err := load(t, yml); err == nil || !strings.Contains(err.Error(), "at least one") {
		t.Fatalf("err = %v, want an empty-backup error", err)
	}
}

func TestFolder(t *testing.T) {
	cases := []struct {
		tgt  PostgresTarget
		want string
	}{
		{PostgresTarget{Host: "shop-postgres"}, "shop-postgres"},
		{PostgresTarget{Host: "shop-postgres", Port: 5433}, "shop-postgres-5433"},
		{PostgresTarget{Host: "/var/run/postgresql"}, "var-run-postgresql"},
		{PostgresTarget{}, "local"},
		{PostgresTarget{Port: 5433}, "local-5433"},
		{PostgresTarget{Host: "10.0.0.5"}, "10.0.0.5"},
	}
	for _, tc := range cases {
		if got := tc.tgt.Folder(); got != tc.want {
			t.Errorf("Folder(%+v) = %q, want %q", tc.tgt, got, tc.want)
		}
	}
}
