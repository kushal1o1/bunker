package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kushal1o1/bunker/internal/model"
	"github.com/kushal1o1/bunker/internal/pathexp"
	"gopkg.in/yaml.v3"
)

const (
	DefaultConfigPath  = "bunker.yaml"
	DefaultConcurrency = 3
	DefaultPort        = 22
	DefaultSSHUser     = "root"
	DefaultSSHKey      = "~/.ssh/id_ed25519"
	DefaultKnownHosts  = "~/.ssh/known_hosts"
	DefaultSSHTimeout  = 10 * time.Second
	DefaultIdentity    = "~/.config/bunker/key.txt"
	DefaultStorageType = "local"
	DefaultStorageDir  = "~/backups"
	DefaultRetentionD  = 7
	DefaultRetentionW  = 4
	DefaultRetentionM  = 6
)

type Config struct {
	Servers     map[string]model.ServerSpec `json:"servers" yaml:"servers"`
	Storage     model.StorageSpec           `json:"storage" yaml:"storage"`
	Retention   model.RetentionSpec         `json:"retention" yaml:"retention"`
	Encryption  model.EncryptionSpec        `json:"encryption" yaml:"encryption"`
	Concurrency int                         `json:"concurrency" yaml:"concurrency"`

	Path string `json:"-"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	cfg.Path = path
	cfg.setDefaults()
	return &cfg, cfg.Validate()
}

func (c *Config) setDefaults() {
	if c.Concurrency == 0 {
		c.Concurrency = DefaultConcurrency
	}
	if c.Storage.Type == "" {
		c.Storage.Type = DefaultStorageType
	}
	if c.Storage.Dir == "" {
		c.Storage.Dir = DefaultStorageDir
	}
	// Resolve the storage root to an absolute path once, here, so every
	// consumer (snapshot writing, the dry-run banner) sees the same real
	// location. A bare "~/backups" left unexpanded would be treated as the
	// relative path "./~/backups" and silently write into the CWD.
	if dir := resolvePath(c.Storage.Dir); dir != "" {
		c.Storage.Dir = dir
	}
	if c.Retention.Daily == 0 {
		c.Retention.Daily = DefaultRetentionD
	}
	if c.Retention.Weekly == 0 {
		c.Retention.Weekly = DefaultRetentionW
	}
	if c.Retention.Monthly == 0 {
		c.Retention.Monthly = DefaultRetentionM
	}
	if c.Encryption.Identity == "" {
		c.Encryption.Identity = DefaultIdentity
	}

	for name := range c.Servers {
		s := c.Servers[name]
		if s.SSH.User == "" {
			s.SSH.User = DefaultSSHUser
		}
		if s.SSH.Port == 0 {
			s.SSH.Port = DefaultPort
		}
		if s.SSH.Key == "" {
			s.SSH.Key = DefaultSSHKey
		}
		if s.SSH.KnownHosts == "" {
			s.SSH.KnownHosts = DefaultKnownHosts
		}
		if s.SSH.ConnectTimeout == 0 {
			s.SSH.ConnectTimeout = DefaultSSHTimeout
		}
		c.Servers[name] = s
	}
}

// resolvePath expands a leading ~ and returns an absolute, cleaned path. It
// returns "" if the home directory is unavailable, leaving the caller's value
// untouched so validation can report the real problem.
func resolvePath(p string) string {
	p = pathexp.Expand(strings.TrimSpace(p))
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

func (c *Config) Validate() error {
	if len(c.Servers) == 0 {
		return errors.New("config must define at least one server")
	}
	if c.Concurrency < 1 {
		return errors.New("concurrency must be >= 1")
	}
	if strings.TrimSpace(c.Storage.Dir) == "" {
		return errors.New("storage.dir could not be resolved to an absolute path")
	}
	for name, s := range c.Servers {
		if err := validateServer(name, s); err != nil {
			return err
		}
	}
	return nil
}

func validateServer(name string, s model.ServerSpec) error {
	if strings.TrimSpace(s.Host) == "" {
		return fmt.Errorf("server %q: host is required", name)
	}
	if s.SSH.Port < 1 || s.SSH.Port > 65535 {
		return fmt.Errorf("server %q: ssh port out of range: %d", name, s.SSH.Port)
	}
	// yaml.v3 already rejects a bare number like "10"; this catches values
	// written with a unit that are too small to ever connect, like "500ms".
	if s.SSH.ConnectTimeout < time.Second {
		return fmt.Errorf("server %q: ssh.connect_timeout must be at least 1s, written with a unit (e.g. 10s, 1m): got %s", name, s.SSH.ConnectTimeout)
	}
	if s.Backup.Postgres == nil && len(s.Backup.Paths) == 0 {
		return fmt.Errorf("server %q: backup must define at least one postgres db or file path", name)
	}
	if s.Backup.Postgres != nil {
		for _, db := range s.Backup.Postgres.Databases {
			if strings.TrimSpace(db) == "" {
				return fmt.Errorf("server %q: postgres db name cannot be empty", name)
			}
		}
	}
	for _, p := range s.Backup.Paths {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("server %q: backup path cannot be empty", name)
		}
	}
	return nil
}

func (c *Config) ServerNames() []string {
	names := make([]string, 0, len(c.Servers))
	for name := range c.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
