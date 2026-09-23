package model

type ServerSpec struct {
	Host   string     `json:"host" yaml:"host"`
	SSH    SSHConfig  `json:"ssh" yaml:"ssh"`
	Backup BackupSpec `json:"backup" yaml:"backup"`
}

type SSHConfig struct {
	User string `json:"user" yaml:"user"`
	Port int    `json:"port" yaml:"port"`
}

type BackupSpec struct {
	Postgres *PostgresSpec `json:"postgres,omitempty" yaml:"postgres,omitempty"`
	Media    []string      `json:"media,omitempty" yaml:"media,omitempty"`
}

type PostgresSpec struct {
	Databases []string `json:"dbs" yaml:"dbs"`
}

type StorageSpec struct {
	Type string `json:"type" yaml:"type"`
	Dir  string `json:"dir" yaml:"dir"`
}

type RetentionSpec struct {
	Daily   int `json:"daily" yaml:"daily"`
	Weekly  int `json:"weekly" yaml:"weekly"`
	Monthly int `json:"monthly" yaml:"monthly"`
}