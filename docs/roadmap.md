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
- `bunker status`: each server's newest snapshot and its agent's last run
- [Agent mode](agent.md): `bunker agent run` on the server, pushing straight
  to S3, on a plain host or in Docker, without root

## Next

1. `backup restore` to a local folder
2. `server check`, `--json`, failure notifications
3. Resumable uploads: skip artifacts already in S3

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
