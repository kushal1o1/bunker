# Getting Started

## 1. Install

bunker is a single binary with no dependencies. It runs on your machine, not
on the servers you back up.

### Prebuilt binary

Every [release](https://github.com/kushal1o1/bunker/releases) has these files:

| System | File |
|---|---|
| Linux, x86_64 | `bunker-linux-amd64` |
| Linux, ARM64 (Raspberry Pi 4/5, Graviton) | `bunker-linux-arm64` |
| macOS, Intel | `bunker-darwin-amd64` |
| macOS, Apple Silicon (M1 and later) | `bunker-darwin-arm64` |
| Windows, x86_64 | `bunker-windows-amd64.exe` |

Not sure which one? Run `uname -sm`: `x86_64` means amd64, `aarch64` or
`arm64` means arm64.

**Linux**

```bash
FILE=bunker-linux-amd64    # or bunker-linux-arm64
curl -LO https://github.com/kushal1o1/bunker/releases/latest/download/$FILE
curl -LO https://github.com/kushal1o1/bunker/releases/latest/download/checksums.txt
sha256sum --check --ignore-missing checksums.txt    # must print: OK
chmod +x $FILE
sudo mv $FILE /usr/local/bin/bunker
```

**macOS**

```bash
FILE=bunker-darwin-arm64   # or bunker-darwin-amd64 on Intel
curl -LO https://github.com/kushal1o1/bunker/releases/latest/download/$FILE
curl -LO https://github.com/kushal1o1/bunker/releases/latest/download/checksums.txt
shasum -a 256 --check --ignore-missing checksums.txt    # must print: OK
chmod +x $FILE
xattr -d com.apple.quarantine $FILE 2>/dev/null    # let macOS run a downloaded binary
sudo mv $FILE /usr/local/bin/bunker
```

**Windows**

Download `bunker-windows-amd64.exe` from the
[latest release](https://github.com/kushal1o1/bunker/releases/latest), rename it
to `bunker.exe`, and run it from PowerShell:

```powershell
.\bunker.exe version
```

Always check the checksum. If the check fails, or a "binary" is only a few
bytes long, the download went wrong: delete it and download again.

### With Go

Go 1.27 or newer:

```bash
go install github.com/kushal1o1/bunker/cmd/bunker@latest
```

`@latest` installs the newest stable release and skips pre-releases such as
`v0.2.0-dev`. To get a specific version, name it:

```bash
go install github.com/kushal1o1/bunker/cmd/bunker@v0.2.0-dev
```

### From source

```bash
git clone https://github.com/kushal1o1/bunker.git
cd bunker
go build -o bunker ./cmd/bunker
```

### Check it worked

```bash
bunker version
```

```
bunker v0.2.0-dev
```

If the shell says `command not found`, `/usr/local/bin` (or `$(go env GOPATH)/bin`
for `go install`) is not on your `PATH`.

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

See [Configuring bunker.yaml](configuration.md) for every field, multiple
servers and multiple paths.

## 5. Take a backup

```bash
bunker backup run my-vps
```

The snapshot is written to `~/backups/my-vps/<timestamp>/`. Each snapshot is a
directory; nothing is ever overwritten. List your snapshots with:

```bash
bunker backup list my-vps
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