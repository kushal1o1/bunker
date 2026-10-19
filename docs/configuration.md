# Configuring bunker.yaml

bunker reads one YAML file that describes your servers, what to back up from
each of them, and where the encrypted snapshots go.

- By default it loads `bunker.yaml` from the current directory. Use
  `-c <path>` (or `--config <path>`) on any command to load a different file.
- A leading `~` in any path field is expanded to your home directory.
- Unknown keys are an error. A typo such as `dbz:` or `known_host:` stops
  bunker with a parse error instead of being silently ignored.

## Full example

Two servers, every field set or left to its default:

```yaml
servers:
  web-01:                          # name used in commands: bunker backup run web-01
    host: 203.0.113.10
    ssh:
      user: bunker
      port: 22
      key: ~/.ssh/bunker_ed25519
      known_hosts: ~/.ssh/known_hosts
      connect_timeout: 10s         # give up on a host that does not answer
      keepalive_interval: 15s      # drop a server that freezes mid-backup
    backup:
      postgres:
        dbs: [shop_db, blog_db]    # several databases on one server
      paths:                       # several folders on one server
        - /var/www/shop/media
        - /var/www/blog/uploads
        - /etc/nginx

  db-01:
    host: db.example.com
    # no ssh block: user root, port 22, ~/.ssh/id_ed25519, ~/.ssh/known_hosts, 10s timeout
    backup:
      postgres:
        dbs: [analytics]
        user: bunker_backup        # pg_dump -U bunker_backup
        host: 127.0.0.1            # pg_dump -h 127.0.0.1 (TCP instead of socket)
        port: 5432                 # pg_dump -p 5432

storage:
  type: local
  dir: ~/backups

encryption:
  identity: ~/.config/bunker/key.txt

retention:
  daily: 7
  weekly: 4
  monthly: 6

concurrency: 3
```

## Field reference

### servers

`servers` is a map. Each key is the server's name, which you pass to commands
such as `bunker backup run <name>` and `bunker backup verify <name> <snapshot>`.
It is also the folder name under `storage.dir`.

| Field | Type | Default | Required | Notes |
|---|---|---|---|---|
| `host` | string | | yes | IP address or hostname to SSH into |
| `ssh.user` | string | `root` | no | SSH login user |
| `ssh.port` | int | `22` | no | must be between 1 and 65535 |
| `ssh.key` | path | `~/.ssh/id_ed25519` | no | private key used to log in |
| `ssh.known_hosts` | path | `~/.ssh/known_hosts` | no | the server's host key must already be in this file |
| `ssh.connect_timeout` | duration | `10s` | no | how long to wait for the server to answer, for the TCP connect and again for the SSH handshake; write it with a unit (`30s`, `1m`), at least `1s` |
| `ssh.keepalive_interval` | duration | `15s` | no | how often a connected server is checked; after 3 checks in a row go unanswered the server is dropped; at least `1s` |
| `backup.postgres.dbs` | list of strings | | see below | databases to dump with `pg_dump -Fc` |
| `backup.postgres.user` | string | | no | passed as `pg_dump -U` |
| `backup.postgres.host` | string | | no | passed as `pg_dump -h`; leave empty to use the local socket |
| `backup.postgres.port` | int | | no | passed as `pg_dump -p` |
| `backup.paths` | list of paths | | see below | absolute folders or files on the server to archive with `tar` |

Each server needs at least one entry in `backup.postgres.dbs` or in
`backup.paths`. It can have both.

When `postgres.user`, `host` and `port` are left out, `pg_dump` connects
through the local Unix socket as the SSH user (peer auth). Set them when the
database role differs from the SSH user or listens only on TCP; the password
then comes from `~/.pgpass` on the server.

### storage

| Field | Type | Default | Required | Notes |
|---|---|---|---|---|
| `storage.type` | string | `local` | no | only `local` is implemented |
| `storage.dir` | path | `~/backups` | no | root folder for snapshots; a relative path is resolved against the directory you run bunker from |

Snapshots are written to `<storage.dir>/<server name>/<timestamp>/`.

### encryption

| Field | Type | Default | Required | Notes |
|---|---|---|---|---|
| `encryption.identity` | path | `~/.config/bunker/key.txt` | no | age identity created by `bunker keygen`; its public key encrypts backups, its private key decrypts them for verify |

### retention

| Field | Type | Default | Required | Notes |
|---|---|---|---|---|
| `retention.daily` | int | `7` | no | days to keep the newest snapshot of |
| `retention.weekly` | int | `4` | no | weeks (Monday to Sunday) to keep the newest snapshot of |
| `retention.monthly` | int | `6` | no | months to keep the newest snapshot of |

These are used by `bunker retention apply`. Setting a value to `0` means "use the
default". See [Retention](retention.md) for exactly how the policy decides.

### concurrency

| Field | Type | Default | Required | Notes |
|---|---|---|---|---|
| `concurrency` | int | `3` | no | how many servers `backup run` backs up at the same time; must be 1 or more |

## Multiple servers

Add one key per server under `servers`. Each server has its own SSH settings
and its own backup list:

```yaml
servers:
  web-01:
    host: 203.0.113.10
    ssh:
      user: bunker
    backup:
      paths: [/var/www/shop/media]

  web-02:
    host: 203.0.113.20
    ssh:
      user: bunker
    backup:
      postgres:
        dbs: [crm_db]
```

Back up one, several, or all of them:

```bash
bunker backup run web-01
bunker backup run web-01 web-02
bunker backup run --all
```

`--all` goes through the servers in alphabetical order by name. Use
`bunker server list` to see what is configured.

## Multiple paths on one server

List as many paths as you need under `backup.paths`. Each path becomes its own
encrypted archive in the snapshot, named after the path with `/` replaced by
`-`:

```yaml
backup:
  paths:
    - /var/www/shop/media
    - /var/www/blog/uploads
    - /etc/nginx
```

| Path on the server | File in the snapshot |
|---|---|
| `/var/www/shop/media` | `files/var-www-shop-media.tar.zst.age` |
| `/var/www/blog/uploads` | `files/var-www-blog-uploads.tar.zst.age` |
| `/etc/nginx` | `files/etc-nginx.tar.zst.age` |

On the server bunker runs `tar -C <parent> -cf - <name>`, so the archive
contains the folder itself (for example `media/...`), not the full path.

Things to watch for:

- Use absolute paths without a trailing slash: `/var/www/shop/media`, not
  `/var/www/shop/media/`.
- The SSH user needs read permission on every file inside each path.
- Two paths that differ only in `/` versus `-` map to the same file name
  (`/srv/a-b/c` and `/srv/a/b-c` both become `files/srv-a-b-c.tar.zst.age`),
  so the second would overwrite the first. Avoid listing both.

## Multiple databases on one server

Put every database in `dbs`. Each one is dumped separately to
`postgres/<db>.dump.zst.age`:

```yaml
backup:
  postgres:
    dbs: [shop_db, blog_db, analytics]
```

All databases on one server share the same `user`, `host` and `port`
overrides.

## Timeouts

A host that does not exist by name fails at once (`no such host`), and a
machine with port 22 closed fails at once (`connection refused`). A wrong IP,
a machine that is down, or a firewall that silently drops packets never
answers at all, so bunker waits `ssh.connect_timeout` (10s by default) and then
reports:

```
ssh root@203.0.113.99:22: no response after 10s (host down, wrong IP, or a firewall dropping packets)
```

The same timeout covers the SSH handshake: a port that accepts the connection
but has no SSH server behind it fails with `no SSH handshake within 10s`
instead of hanging. Other servers in the same run are not held up, and
`Ctrl-C` stops a pending connect immediately.

Raise it for servers on slow or distant links:

```yaml
servers:
  far-away:
    host: 198.51.100.7
    ssh:
      connect_timeout: 30s
```

### A server that freezes mid-backup

Once connected, bunker checks every `ssh.keepalive_interval` (15s by default)
that the server is still there. If 3 checks in a row get no answer, about a
minute with the default, the server is dropped, its partial snapshot is
deleted, and the run reports:

```
ssh root@203.0.113.10:22: server stopped responding (no answer for 45s)
```

A command that is simply slow, like `pg_dump` working through a large table,
is never cut off: the server answers the checks while the command is busy.
Only a host that has stopped answering (power loss, network gone) is dropped.

## Validation rules

bunker refuses to start when:

- `servers` is empty
- a server has no `host`
- `ssh.port` is outside 1 to 65535
- `ssh.connect_timeout` or `ssh.keepalive_interval` is less than `1s`, or a
  number without a unit
- a server has neither `postgres.dbs` nor `paths`
- a database name or a path is an empty string
- `concurrency` is less than 1
- the file contains a key bunker does not know
