# Roadmap

## Done

- PostgreSQL and files
- Storage: local disk, S3-compatible (AWS S3, Cloudflare R2, Backblaze B2,
  MinIO), or both, plus `storage sync`
- zstd + age encryption, SHA-256 manifest per snapshot
- `backup run`, `backup list`, `backup verify`, `retention apply`
- Many servers in parallel, progress table, summary
- Connect timeout, keepalive, clean `Ctrl-C`
- Back up with the public key only (`encryption.recipient`)

## Next

1. Docker: Postgres and paths inside containers, then named volumes
2. `backup restore` to a local folder
3. `server check`, `status`, `--json`, failure notifications
4. Resumable uploads: skip artifacts already in S3

## Agent mode

An optional `bunker agent run` on each server, for when no machine of yours is
always on, or a server cannot be reached over SSH:

- backs the server up on its own schedule and pushes straight to S3
- gets only the public key and upload-only S3 access, so a hacked server can
  neither read nor delete old backups
- writes a status file, so `bunker status` on your laptop shows every server's
  health

SSH pull stays the default.

## More databases

| Database | Tool |
|---|---|
| MySQL / MariaDB | `mysqldump` |
| MongoDB | `mongodump` |
| Redis | `redis-cli --rdb` |
| SQLite | `sqlite3 .backup` |

## Not planned

- Our own archive, compression or crypto format
- A web UI
- Arbitrary commands on servers or inside containers

Ideas welcome as [GitHub issues](https://github.com/kushal1o1/bunker/issues).
