package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/kushal1o1/bunker/internal/model"
	"gopkg.in/yaml.v3"
)

const (
	DefaultConfigPath  = "bunker.yaml"
	DefaultConcurrency = 3
	DefaultPort        = 22
	DefaultSSHUser     = "root"
	DefaultStorageType = "local"
	DefaultStorageDir  = "./backups"
	DefaultRetentionD  = 7
	DefaultRetentionW  = 4
	DefaultRetentionM  = 6
)

type Config struct {
	Servers     map[string]model.ServerSpec `json:"servers" yaml:"servers"`
	Storage     model.StorageSpec           `json:"storage" yaml:"storage"`
	Retention   model.RetentionSpec         `json:"retention" yaml:"retention"`
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
	if c.Retention.Daily == 0 {
		c.Retention.Daily = DefaultRetentionD
	}
	if c.Retention.Weekly == 0 {
		c.Retention.Weekly = DefaultRetentionW
	}
	if c.Retention.Monthly == 0 {
		c.Retention.Monthly = DefaultRetentionM
	}

	for name := range c.Servers {
		s := c.Servers[name]
		if s.SSH.User == "" {
			s.SSH.User = DefaultSSHUser
		}
		if s.SSH.Port == 0 {
			s.SSH.Port = DefaultPort
		}
		c.Servers[name] = s
	}
}

func (c *Config) Validate() error {
	if len(c.Servers) == 0 {
		return errors.New("config must define at least one server")
	}
	if c.Concurrency < 1 {
		return errors.New("concurrency must be >= 1")
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
	if s.Backup.Postgres == nil && len(s.Backup.Media) == 0 {
		return fmt.Errorf("server %q: backup must define at least one postgres db or media path", name)
	}
	if s.Backup.Postgres != nil {
		for _, db := range s.Backup.Postgres.Databases {
			if strings.TrimSpace(db) == "" {
				return fmt.Errorf("server %q: postgres db name cannot be empty", name)
			}
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