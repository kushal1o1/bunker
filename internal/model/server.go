package model

import "time"

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
	// ConnectTimeout bounds the TCP connect and the SSH handshake, e.g. "10s".
	ConnectTimeout time.Duration `json:"connect_timeout,omitempty" yaml:"connect_timeout,omitempty"`
	// KeepAliveInterval is how often a connected server is checked; after 3
	// unanswered checks it is treated as dead, e.g. "15s".
	KeepAliveInterval time.Duration `json:"keepalive_interval,omitempty" yaml:"keepalive_interval,omitempty"`
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

// StorageSpec says where snapshots go: Dir for a local folder, S3 for a
// bucket, or both. Setting a location is what turns it on.
type StorageSpec struct {
	Dir string  `json:"dir,omitempty" yaml:"dir,omitempty"`
	S3  *S3Spec `json:"s3,omitempty" yaml:"s3,omitempty"`
}

// S3Spec is an S3-compatible bucket. Credentials never live here: they are
// read from the environment.
type S3Spec struct {
	// Endpoint is a URL with scheme, e.g. https://s3.eu-central-1.amazonaws.com.
	// http:// is allowed for a local MinIO.
	Endpoint string `json:"endpoint" yaml:"endpoint"`
	Bucket   string `json:"bucket" yaml:"bucket"`
	Region   string `json:"region,omitempty" yaml:"region,omitempty"`
	// Prefix is prepended to every key, e.g. "prod/". Normalized to end in /.
	Prefix string `json:"prefix,omitempty" yaml:"prefix,omitempty"`
}

type RetentionSpec struct {
	Daily   int `json:"daily" yaml:"daily"`
	Weekly  int `json:"weekly" yaml:"weekly"`
	Monthly int `json:"monthly" yaml:"monthly"`
}
