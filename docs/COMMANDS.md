# Commands

Every command also has built-in help with the same examples:
`bunker <command> --help`.

## Global flags

| Flag | Default | Meaning |
|---|---|---|
| `-c, --config <path>` | `bunker.yaml` | config file to load |
| `-h, --help` | | help for any command |
| `-v, --version` | | print version |
| `--no-hints` | `false` | do not print the suggested next command |

Every command that reads config accepts `-c`. Paths inside the config support
`~` expansion.

### Hints

After `backup run --dry-run`, a single-server `backup run` (or its failed
upload), and a
`retention apply` preview that would delete something, bunker prints one
suggested next command, ready to paste:

```
Next: bunker backup verify my-vps 2026-10-08T13-32-59
```

Hints go to stderr and only appear in an interactive terminal, so cron jobs
and piped output never see them. If you passed `-c`, the hint keeps it. Turn
hints off with `--no-hints` or by setting `BUNKER_NO_HINTS=1`.

---

## Command list

| Command | Purpose |
|---|---|
| [`bunker version`](#version) | print version |
| [`bunker keygen [--force \| --public]`](#keygen) | generate the age encryption identity, or print its public key |
| [`bunker server list`](#server-list) | show configured servers |
| [`bunker backup run [server...]`](#backup-run) | run a backup |
| [`bunker backup list [server...]`](#backup-list) | list stored snapshots |
| [`bunker backup verify <server> <snapshot>`](#backup-verify) | verify a snapshot's checksums, archive integrity and decryption |
| [`bunker retention apply [server...]`](#retention-apply) | preview, or with `--yes` delete, snapshots the retention policy drops |
| [`bunker storage sync [server...]`](#storage-sync) | upload local snapshots that are missing from S3 |
| `bunker completion <shell>` | generate a shell completion script |
| `bunker help [command]` | help for any command |

---

## version

Print the bunker version. Same as `bunker --version`.

```bash
bunker version
```

No flags.

```
bunker 0.2.0-dev
```

---

## keygen

Create the age keypair that encrypts every backup.

```bash
# first-time setup
bunker keygen

# print the public key, for encryption.recipient
bunker keygen --public

# rotate the key (old snapshots can no longer be decrypted)
bunker keygen --force
```

| Flag | Default | Meaning |
|---|---|---|
| `--force` | `false` | replace an existing identity |
| `--public` | `false` | print only the public key of the existing identity |

```
generated age identity at /home/you/.config/bunker/key.txt
public key: age152wesvp3vuxl3vef6hvedu897zx8pqua4ztm8mn373r75lpzdutq90h5v9
```

`--public` prints just the key, so you can copy it into `encryption.recipient`:

```
$ bunker keygen --public
age152wesvp3vuxl3vef6hvedu897zx8pqua4ztm8mn373r75lpzdutq90h5v9
```

With `--force` the new and the old public key are both printed:

```
replaced age identity at /home/you/.config/bunker/key.txt
public key: age13t6lq...
old public key: age152wes... (snapshots made with it need the old key to verify)
```

Notes:

- The identity is written to `encryption.identity` from `bunker.yaml` with
  mode `0600`, inside a `0700` directory.
- Without `--force`, an existing identity is never overwritten. `--force`
  also replaces a key file that cannot be read.
- **`--force` makes every existing snapshot undecryptable.** The private key is
  not stored anywhere else.
- `keygen` reads the config first, so it needs a valid `bunker.yaml`.

---

## server list

Show every configured server, sorted by name, after defaults are applied.

```bash
bunker server list

# a different config file
bunker -c prod.yaml server list
```

No flags.

```
NAME    HOST          SSH                DB                PATHS
my-vps  203.0.113.10  bunker (port 22)   mydb              /var/www/my-project/uploads
web-01  203.0.113.20  root (port 22)     shop_db, blog_db  -
```

Notes:

- `DB` lists `backup.postgres.dbs`, `PATHS` lists `backup.paths`; `-` means
  none.
- Reads the config only and connects to nothing, so it is the first thing to
  run when a config is wrong.

---

## backup run

Take a new snapshot of one or more servers.

```bash
# preview what would be backed up, no SSH
bunker backup run my-vps --dry-run

# back up one server
bunker backup run my-vps

# back up every configured server
bunker backup run --all
```

| Flag | Default | Meaning |
|---|---|---|
| `--all` | `false` | back up every configured server |
| `--dry-run` | `false` | print the plan and exit: no SSH, no writes |

Dry run:

```
Backup plan (1 server):
  my-vps (203.0.113.10)
    - postgres/mydb                            pg_dump | zstd | age
    - files/var/www/my-project/uploads         tar | zstd | age

Storage: /home/you/backups
Encrypt to: age1r7jyfzpdx5x2e70xy8uawy5yx0fk64dfdgxnafkjfsc6eufk64mquw26d2 (from /home/you/.config/bunker/key.txt)
```

Real run in a terminal: a live table, one row per server, redrawn in place
(on stderr):

```
✓ kaskiserver  done                        2 artifacts  53.0 MB  8.1s
⟳ web-01       files/var/www/shop/media    2/3                   3.2s
○ web-02       waiting
✗ db-01        failed: ssh: dial tcp 192.0.2.5:22: connection refused  1s
```

Real run from cron or a pipe: one line per event (on stdout), prefixed with the
server, so the log stays readable when servers run in parallel:

```
[kaskiserver] connecting 203.0.113.10
[web-01] connecting 203.0.113.20
[kaskiserver] ok postgres/mydb 41.0 MB 6.2s
[db-01] FAILED: ssh: dial tcp 192.0.2.5:22: connection refused
[kaskiserver] ok files/var/www/my-project/uploads 12.0 MB 1.8s
[kaskiserver] snapshot 2026-10-17T09-12-03 (53.0 MB) in 8.1s
```

Both end with a summary:

```
3/4 backed up in 12.3s
  ✓ kaskiserver  2026-10-17T09-12-03  53.0 MB
  ✓ web-01       2026-10-17T09-12-04  20.0 MB
  ✓ web-02       2026-10-17T09-12-09  7.0 MB
  ✗ db-01        ssh: dial tcp 192.0.2.5:22: connection refused
```

Notes:

- On the server bunker only runs `pg_dump -Fc` and `tar -cf -` and reads their
  output. Compression, encryption and the SHA-256 checksums happen on your
  machine.
- Up to `concurrency` servers (from `bunker.yaml`, default 3) are backed up at
  the same time. Within one server, databases go first, then paths, in config
  order.
- Every server is attempted. A failure is reported in the summary and does not
  stop the others.
- A server that does not answer fails after `ssh.connect_timeout` (10s by
  default), and one that freezes mid-backup is dropped after about a minute,
  instead of hanging the run. See [Timeouts](configuration.md#timeouts).
- With `storage.s3` set, each snapshot is uploaded too: streamed straight to
  the bucket when S3 is the only store, or copied after the local snapshot is
  written when both are set. The table shows `uploading to s3 2/3`. See
  [Storage](configuration.md#storage-local-s3-or-both).
- The bucket and credentials are checked before any server is contacted.
- Exit code `0` when every server succeeded, `1` if any failed, so cron or a
  monitor can alert on it. A failed upload also exits `1`, but the local
  snapshot is complete and kept: run `bunker storage sync` to upload it.
- `Ctrl-C` (or SIGTERM) stops at once, even in the middle of a transfer,
  closes the SSH connections and deletes the partial
  snapshots, so a half-written snapshot never looks complete. Servers that had
  not started yet show as `cancelled`.
- Naming the same server twice backs it up once.
- `--all` cannot be combined with server names, and running with neither is an
  error.

---

## backup list

List stored snapshots, newest first. With no server names, every configured
server is listed.

```bash
# every configured server
bunker backup list

# one or more servers
bunker backup list my-vps web-01
```

No flags.

```
my-vps (3 snapshots, 84.0 MB)
  SNAPSHOT             CREATED (UTC)        ARTIFACTS  SIZE    STATUS
  2026-10-17T09-12-03  2026-10-17 09:12:03  2          42.0 MB  complete
  2026-10-16T09-10-00  2026-10-16 09:10:00  2          42.0 MB  complete
  2026-10-15T09-00-11  -                    -          -       no manifest

web-01: no snapshots
```

With both `storage.dir` and `storage.s3`, the STATUS column becomes one column
per store, so you can see where each snapshot is:

```
my-vps (3 snapshots, 126.0 MB)
  SNAPSHOT             CREATED (UTC)        ARTIFACTS  SIZE    LOCAL     S3
  2026-10-17T09-12-03  2026-10-17 09:12:03  2          42.0 MB  complete  complete
  2026-10-16T09-10-00  2026-10-16 09:10:00  2          42.0 MB  complete  -
  2026-10-01T09-00-00  2026-10-01 09:00:00  2          42.0 MB  -         complete
```

| Status | Meaning |
|---|---|
| `complete` | `manifest.json` is present and valid. This does not mean verified; run `bunker backup verify` for that. |
| `no manifest` | the folder has no manifest, usually left by a run that was killed before it finished |
| `bad manifest` | a manifest exists but cannot be read or is missing fields |

Notes:

- Never connects over SSH and needs no key. With S3, it reads the bucket.
- The size in the header adds up complete snapshots only.

---

## backup verify

Prove a stored snapshot is intact and readable.

```bash
# find a snapshot, then verify it
bunker backup list my-vps
bunker backup verify my-vps 2026-10-17T09-12-03

# check the S3 copy instead of the local one
bunker backup verify my-vps 2026-10-17T09-12-03 --remote
```

| Flag | Default | Meaning |
|---|---|---|
| `--remote` | `false` | verify the copy in S3 when storage has both `dir` and `s3` |

```
Verifying my-vps/2026-10-17T09-12-03
  manifest recipient  age1r7jyfzpdx5x2e70xy8uawy5yx0fk64dfdgxnafkjfsc6eufk64mquw26d2
  local identity      age1r7jyfzpdx5x2e70xy8uawy5yx0fk64dfdgxnafkjfsc6eufk64mquw26d2
  ARTIFACT                                      SIZE    SHA256  DECRYPT
  postgres/mydb.dump.zst.age                    41.0 MB  ok      ok
  files/var-www-my-project-uploads.tar.zst.age  12.0 MB  ok      ok

  verified 2/2 artifact(s) - 2026-10-17T09-12-03 OK
```

Each artifact goes through three checks:

| Check | Meaning |
|---|---|
| size | the file exists and its length matches the manifest |
| SHA-256 | the stored file's hash matches the manifest |
| decrypt | it decrypts with your identity and fully decompresses |

Notes:

- Exit code `0` only when every check passes. Otherwise one reason line is
  printed per failed artifact and the exit code is `1`.
- **`NO KEY` is not corruption.** It means the snapshot was encrypted to a
  different public key than your current identity (for example after
  `keygen --force`). Size and SHA-256 are still checked; only decrypt is
  skipped.
- Reads the local copy, or S3 when it is the only store. An S3 snapshot is
  streamed and checked as it downloads, never saved to disk.
- Every artifact is read once: size, SHA-256 and decryption are checked in the
  same pass.
- Never connects over SSH and never writes anything.

---

## retention apply

Apply the retention policy to stored snapshots.

```bash
# preview every server
bunker retention apply

# delete what the policy drops for one server
bunker retention apply my-vps --yes
```

| Flag | Default | Meaning |
|---|---|---|
| `--yes` | `false` | actually delete; without it nothing is removed |

```
Policy: daily 7, weekly 4, monthly 6

my-vps: keep 4, delete 1
  SNAPSHOT             SIZE    ACTION  WHY
  2026-10-17T18-00-00  42.0 MB  keep    daily 1, weekly 1, monthly 1
  2026-10-17T06-00-00  42.0 MB  delete
  2026-10-16T09-00-00  42.0 MB  keep    daily 2

Preview only: 1 snapshot(s), 42.0 MB would be deleted. Nothing was removed.
```

Notes:

- The newest snapshot is always kept.
- Folders without a valid manifest are shown as `skip` and never deleted.
- With both local and S3 storage, each snapshot is decided once and deleted
  from both. A snapshot that exists in only one place still counts.
- See [Retention](retention.md) for exactly how the policy decides.

---

## storage sync

Upload local snapshots that S3 does not have yet, for example after an upload
failed or after adding `storage.s3` to an existing setup.

```bash
# every configured server
bunker storage sync

# one server
bunker storage sync my-vps
```

No flags.

```
Syncing /home/you/backups -> s3://my-bunker/prod/

my-vps
  SNAPSHOT             SIZE    ACTION
  2026-10-17T09-12-03  42.0 MB  uploaded
  2026-10-16T09-10-00  42.0 MB  already in s3
  2026-10-15T09-00-11  -       skip (local no manifest)

Uploaded 1 snapshot(s), 42.0 MB.
```

Notes:

- Needs both `storage.dir` and `storage.s3`.
- Artifacts are uploaded first and `manifest.json` last, so a copy only counts
  once it is whole. A failed copy is removed from S3.
- Snapshots without a valid local manifest are skipped.
- Exit code `1` if any upload failed.
