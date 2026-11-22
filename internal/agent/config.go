// Package agent is `bunker agent`: back up the machine it runs on and push
// the snapshot straight to S3. It holds only the age public key and upload
// credentials, so it can write backups but never read or delete them.
package agent

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"

	"filippo.io/age"
	"github.com/kushal1o1/bunker/internal/backup"
	"github.com/kushal1o1/bunker/internal/config"
	"github.com/kushal1o1/bunker/internal/model"
	"gopkg.in/yaml.v3"
)

// DefaultConfigPath is where `bunker agent` looks without -c.
const DefaultConfigPath = "/etc/bunker/agent.yaml"

// Config is agent.yaml.
type Config struct {
	Agent      Spec           `yaml:"agent"`
	Backup     BackupSpec     `yaml:"backup"`
	Storage    StorageSpec    `yaml:"storage"`
	Encryption EncryptionSpec `yaml:"encryption"`
}

type Spec struct {
	// Name is this server's folder in S3. The laptop lists, verifies and
	// prunes it under the same name.
	Name string `yaml:"name"`
	// Schedule is a cron expression for `bunker agent run`.
	Schedule string `yaml:"schedule"`
}

type BackupSpec struct {
	Postgres []PostgresTarget `yaml:"postgres"`
	Paths    []string         `yaml:"paths"`
}

// PostgresTarget is one Postgres server and the databases to dump from it.
type PostgresTarget struct {
	// Host is a hostname (a container on bunker-net) or a socket directory.
	// Empty means pg_dump's default, the local socket.
	Host string   `yaml:"host"`
	Port int      `yaml:"port"`
	User string   `yaml:"user"`
	DBs  []string `yaml:"dbs"`
}

type StorageSpec struct {
	S3 *model.S3Spec `yaml:"s3"`
	// Dir is only here to reject it with a clear message.
	Dir string `yaml:"dir"`
}

type EncryptionSpec struct {
	Recipient string `yaml:"recipient"`
	// Identity is only here to reject it with a clear message.
	Identity string `yaml:"identity"`
}

// Load reads, normalizes and validates agent.yaml.
func Load(p string) (*Config, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("read agent config: %w", err)
	}
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	c.normalize()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return &c, nil
}

func (c *Config) normalize() {
	c.Agent.Name = strings.TrimSpace(c.Agent.Name)
	c.Agent.Schedule = strings.TrimSpace(c.Agent.Schedule)
	c.Encryption.Recipient = strings.TrimSpace(c.Encryption.Recipient)
	if c.Storage.S3 != nil {
		config.NormalizeS3(c.Storage.S3)
	}
	for i := range c.Backup.Postgres {
		t := &c.Backup.Postgres[i]
		t.Host = strings.TrimSpace(t.Host)
		t.User = strings.TrimSpace(t.User)
	}
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Validate checks the rules that keep an agent safe and its snapshots
// readable by the laptop.
func (c *Config) Validate() error {
	if !nameRe.MatchString(c.Agent.Name) {
		return fmt.Errorf("agent.name must be letters, digits, '.', '_' or '-' (it is this server's folder in S3): got %q", c.Agent.Name)
	}
	if c.Encryption.Identity != "" {
		return errors.New("encryption.identity is not allowed: an agent must never hold the private key; set encryption.recipient (bunker keygen --public on your laptop)")
	}
	if c.Encryption.Recipient == "" {
		return errors.New("encryption.recipient is required (bunker keygen --public on your laptop)")
	}
	if _, err := age.ParseX25519Recipient(c.Encryption.Recipient); err != nil {
		return fmt.Errorf("encryption.recipient %q is not an age public key (age1...): %w", c.Encryption.Recipient, err)
	}
	if c.Storage.Dir != "" {
		return errors.New("storage.dir is not allowed: an agent stores in S3 only")
	}
	if c.Storage.S3 == nil {
		return errors.New("storage.s3 is required")
	}
	if err := config.ValidateS3(c.Storage.S3); err != nil {
		return err
	}
	if len(c.Backup.Postgres) == 0 && len(c.Backup.Paths) == 0 {
		return errors.New("backup must list at least one postgres target or path")
	}
	if err := c.validatePostgres(); err != nil {
		return err
	}
	return c.validatePaths()
}

func (c *Config) validatePostgres() error {
	folders := map[string]string{}
	for i, t := range c.Backup.Postgres {
		where := fmt.Sprintf("backup.postgres[%d]", i)
		if t.Host != "" {
			where += " (" + t.Host + ")"
		}
		if t.Port < 0 || t.Port > 65535 {
			return fmt.Errorf("%s: port out of range: %d", where, t.Port)
		}
		if len(t.DBs) == 0 {
			return fmt.Errorf("%s: dbs is required", where)
		}
		seen := map[string]bool{}
		for _, db := range t.DBs {
			if db == "" || strings.ContainsAny(db, "/\\") || db == "." || db == ".." || strings.TrimSpace(db) != db {
				return fmt.Errorf("%s: invalid database name %q", where, db)
			}
			if seen[db] {
				return fmt.Errorf("%s: database %q is listed twice", where, db)
			}
			seen[db] = true
		}
		f := t.Folder()
		if prev, ok := folders[f]; ok {
			return fmt.Errorf("%s: same Postgres as %s (both would be saved under postgres/%s/)", where, prev, f)
		}
		folders[f] = where
	}
	return nil
}

func (c *Config) validatePaths() error {
	seen := map[string]bool{}
	for _, p := range c.Backup.Paths {
		if !path.IsAbs(p) {
			return fmt.Errorf("backup.paths: %q must be an absolute path", p)
		}
		clean := path.Clean(p)
		if clean == "/" {
			return errors.New("backup.paths: refusing to archive / (list the folders you need)")
		}
		if seen[clean] {
			return fmt.Errorf("backup.paths: %q is listed twice", p)
		}
		seen[clean] = true
	}
	return nil
}

// Folder is the target's folder in a snapshot: postgres/<folder>/<db>. It
// comes from the host (a socket path becomes dashes), plus the port when one
// is set, so two Postgres servers never share a folder.
func (t PostgresTarget) Folder() string {
	name := strings.Trim(strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		}
		return '-'
	}, t.Host), "-.")
	if name == "" {
		name = "local"
	}
	if t.Port != 0 {
		name += "-" + strconv.Itoa(t.Port)
	}
	return name
}

// Artifacts is what one run backs up, in config order.
func (c *Config) Artifacts() []backup.Artifact {
	var arts []backup.Artifact
	for _, t := range c.Backup.Postgres {
		for _, db := range t.DBs {
			arts = append(arts, &backup.PostgresArtifact{
				DB: db, Host: t.Host, Port: t.Port, User: t.User, Group: t.Folder(),
			})
		}
	}
	for _, p := range c.Backup.Paths {
		arts = append(arts, &backup.FilesArtifact{RemotePath: path.Clean(p)})
	}
	return arts
}
