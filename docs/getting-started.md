# Getting Started

## 1. Install

From the Go toolchain (Go 1.27 or newer):

```bash
go install github.com/kushal1o1/bunker/cmd/bunker@latest
```

Or download a prebuilt binary from the [Releases](https://github.com/kushal1o1/bunker/releases)
page.

## 2. Create the encryption key

```bash
bunker keygen
```

This writes the private identity to `~/.config/bunker/key.txt` (the default)
and prints the public key. Keep the file safe - every backup is encrypted to
it, and if you lose it, your backups are unreadable forever.

## 3. Set up a dedicated user on the server

Create a system user on each VPS, for example `bunker`, and add your SSH public
key to its `authorized_keys`. The user needs:

- **Postgres**: a login role with read-only access (on PostgreSQL 15+ simply
  `GRANT pg_read_all_data TO <role>`)
- **Files**: read permission on the folders you want to back up

No password is required - peer auth over the local socket is enough when the
role name matches the SSH user.

## 4. Configure bunker.yaml

Minimal config for one server:

```yaml
servers:
  my-vps:
    host: 203.0.113.10
    ssh:
      user: bunker
    backup:
      postgres:
        dbs: [mydb]
      paths:
        - /var/www/my-project/uploads

storage:
  dir: ~/backups

encryption:
  identity: ~/.config/bunker/key.txt
```

## 5. Take a backup

```bash
bunker backup run my-vps
```

The snapshot is written to `~/backups/my-vps/<timestamp>/`. Each snapshot is a
directory; nothing is ever overwritten. List your snapshots with:

```bash
ls ~/backups/my-vps/
```

## 6. Verify a snapshot

```bash
bunker backup verify my-vps 2026-10-08T13-32-59
```

This re-checks every artifact size, SHA-256, and decryption. Exit code `0`
means the snapshot is fully readable.

## 7. Restoring

There is no `restore` command yet(In this version ); recover by hand from the files in the
snapshot directory:

```bash
cd ~/backups/my-vps/<timestamp>

# Postgres dump: decrypt + decompress, then restore the database
age -d -i ~/.config/bunker/key.txt \
  < postgres/mydb.dump.zst.age \
  | zstd -d > mydb.dump
createdb newdb
pg_restore -d newdb mydb.dump

# File archive: pipe straight into tar
age -d -i ~/.config/bunker/key.txt \
  < files/var-www-uploads.tar.zst.age \
  | zstd -d | tar -C /tmp/restore -xf -
```