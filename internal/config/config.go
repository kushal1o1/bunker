package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
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
	DefaultKeepAlive   = 15 * time.Second
	DefaultIdentity    = "~/.config/bunker/key.txt"
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
	// No storage at all means local ~/backups. With only s3 set, there is no
	// local copy, so dir must stay empty rather than get the default.
	if c.Storage.Dir == "" && c.Storage.S3 == nil {
		c.Storage.Dir = DefaultStorageDir
	}
	// Resolve the storage root to an absolute path once, here, so every
	// consumer (snapshot writing, the dry-run banner) sees the same real
	// location. A bare "~/backups" left unexpanded would be treated as the
	// relative path "./~/backups" and silently write into the CWD.
	if dir := resolvePath(c.Storage.Dir); dir != "" {
		c.Storage.Dir = dir
	}
	if s3 := c.Storage.S3; s3 != nil {
		s3.Endpoint = strings.TrimRight(strings.TrimSpace(s3.Endpoint), "/")
		s3.Bucket = strings.TrimSpace(s3.Bucket)
		if p := strings.Trim(strings.TrimSpace(s3.Prefix), "/"); p != "" {
			s3.Prefix = p + "/"
		} else {
			s3.Prefix = ""
		}
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
	c.Encryption.Recipient = strings.TrimSpace(c.Encryption.Recipient)

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
		if s.SSH.KeepAliveInterval == 0 {
			s.SSH.KeepAliveInterval = DefaultKeepAlive
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
	if s3 := c.Storage.S3; s3 != nil {
		if err := validateS3(s3); err != nil {
			return err
		}
	}
	if r := c.Encryption.Recipient; r != "" {
		if _, err := age.ParseX25519Recipient(r); err != nil {
			return fmt.Errorf("encryption.recipient %q is not an age public key (age1...): %w", r, err)
		}
	}
	for name, s := range c.Servers {
		if err := validateServer(name, s); err != nil {
			return err
		}
	}
	return nil
}

func validateS3(s *model.S3Spec) error {
	if s.Endpoint == "" {
		return errors.New("storage.s3.endpoint is required (e.g. https://s3.eu-central-1.amazonaws.com)")
	}
	u, err := url.Parse(s.Endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("storage.s3.endpoint must be a URL starting with https:// or http://: got %q", s.Endpoint)
	}
	if u.Path != "" || u.RawQuery != "" {
		return fmt.Errorf("storage.s3.endpoint must not contain a path: got %q (put the bucket in storage.s3.bucket)", s.Endpoint)
	}
	if s.Bucket == "" {
		return errors.New("storage.s3.bucket is required")
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
	if s.SSH.KeepAliveInterval < time.Second {
		return fmt.Errorf("server %q: ssh.keepalive_interval must be at least 1s, written with a unit (e.g. 15s, 1m): got %s", name, s.SSH.KeepAliveInterval)
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
