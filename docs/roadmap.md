# Roadmap

## Done

- PostgreSQL and files, to local disk
- zstd + age encryption, SHA-256 manifest per snapshot
- `backup run`, `backup list`, `backup verify`, `retention apply`
- Many servers in parallel, progress table, summary
- Connect timeout, keepalive, clean `Ctrl-C`

## Next

1. S3-compatible storage (AWS S3, Cloudflare R2, Backblaze B2, MinIO)
2. Back up with the public key only, so the backup machine cannot read backups
3. Docker: Postgres and paths inside containers, then named volumes
4. `backup restore` to a local folder
5. `server check`, `status`, `--json`, failure notifications

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
