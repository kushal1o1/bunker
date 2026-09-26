package model

type ServerSpec struct {
	Host   string     `json:"host" yaml:"host"`
	SSH    SSHConfig  `json:"ssh" yaml:"ssh"`
	Backup BackupSpec `json:"backup" yaml:"backup"`
}

type SSHConfig struct {
	User string `json:"user" yaml:"user"`
	Port int    `json:"port" yaml:"port"`
	Key  string `json:"key" yaml:"key"`
	// KnownHosts is the path to the known_hosts file used to verify the server key.
	KnownHosts string `json:"known_hosts" yaml:"known_hosts"`
}

type BackupSpec struct {
	Postgres *PostgresSpec `json:"postgres,omitempty" yaml:"postgres,omitempty"`
	Paths    []string      `json:"paths,omitempty" yaml:"paths,omitempty"`
}

type PostgresSpec struct {
	Databases []string `json:"dbs" yaml:"dbs"`
	// User, Host and Port override how pg_dump connects on the server
	// (e.g. TCP + ~/.pgpass instead of the local socket).
	User string `json:"user,omitempty" yaml:"user,omitempty"`
	Host string `json:"host,omitempty" yaml:"host,omitempty"`
	Port int    `json:"port,omitempty" yaml:"port,omitempty"`
}

type EncryptionSpec struct {
	// Identity is the path to the age identity (secret key) used for encryption.
	Identity string `json:"identity" yaml:"identity"`
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