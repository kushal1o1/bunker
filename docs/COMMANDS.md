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

After `backup run --dry-run`, a single-server `backup run`, and a
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
| [`bunker keygen [--force]`](#keygen) | generate the age encryption identity |
| [`bunker server list`](#server-list) | show configured servers |
| [`bunker backup run [server...]`](#backup-run) | run a backup |
| [`bunker backup list [server...]`](#backup-list) | list stored snapshots |
| [`bunker backup verify <server> <snapshot>`](#backup-verify) | verify a snapshot's checksums, archive integrity and decryption |
| [`bunker retention apply [server...]`](#retention-apply) | preview, or with `--yes` delete, snapshots the retention policy drops |
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
bunker 0.1.0-dev
```

---

## keygen

Create the age keypair that encrypts every backup.

```bash
# first-time setup
bunker keygen

# rotate the key (old snapshots can no longer be decrypted)
bunker keygen --force
```

| Flag | Default | Meaning |
|---|---|---|
| `--force` | `false` | replace an existing identity |

```
generated age identity at /home/you/.config/bunker/key.txt
public key: age152wesvp3vuxl3vef6hvedu897zx8pqua4ztm8mn373r75lpzdutq90h5v9
```

With `--force` the old and new public keys are both printed, so you can see
the key changed:

```
replaced age identity at /home/you/.config/bunker/key.txt (old age152wes... -> age13t6lq...)
```

Notes:

- The identity is written to `encryption.identity` from `bunker.yaml` with
  mode `0600`, inside a `0700` directory.
- Without `--force`, an existing identity is never overwritten.
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
Encryption identity: /home/you/.config/bunker/key.txt
```

Real run:

```
Backing up my-vps (203.0.113.10)
  ok postgres/mydb                                     41 MiB  6.2s
  ok files/var/www/my-project/uploads                  12 MiB  1.8s
  snapshot 2026-10-17T09-12-03 (53 MiB)
  done: /home/you/backups/my-vps/2026-10-17T09-12-03
```

Notes:

- On the server bunker only runs `pg_dump -Fc` and `tar -cf -` and reads their
  output. Compression, encryption and the SHA-256 checksums happen on your
  machine.
- Databases are backed up first, then paths, in config order.
- `Ctrl-C` (or SIGTERM) closes the SSH session and deletes the partial
  snapshot, so a half-written snapshot never looks complete.
- `--all` cannot be combined with server names, and running with neither is an
  error.
- Servers are backed up one at a time; `concurrency` is not used yet.

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
my-vps (3 snapshots, 84 MiB)
  SNAPSHOT             CREATED (UTC)        ARTIFACTS  SIZE    STATUS
  2026-10-17T09-12-03  2026-10-17 09:12:03  2          42 MiB  complete
  2026-10-16T09-10-00  2026-10-16 09:10:00  2          42 MiB  complete
  2026-10-15T09-00-11  -                    -          -       no manifest

web-01: no snapshots
```

| Status | Meaning |
|---|---|
| `complete` | `manifest.json` is present and valid. This does not mean verified; run `bunker backup verify` for that. |
| `no manifest` | the folder has no manifest, usually left by a run that was killed before it finished |
| `bad manifest` | a manifest exists but cannot be read or is missing fields |

Notes:

- Reads local storage only: no network and no key needed.
- The size in the header adds up complete snapshots only.

---

## backup verify

Prove a stored snapshot is intact and readable.

```bash
# find a snapshot, then verify it
bunker backup list my-vps
bunker backup verify my-vps 2026-10-17T09-12-03
```

No flags.

```
Verifying my-vps/2026-10-17T09-12-03
  manifest recipient  age1r7jyfzpdx5x2e70xy8uawy5yx0fk64dfdgxnafkjfsc6eufk64mquw26d2
  local identity      age1r7jyfzpdx5x2e70xy8uawy5yx0fk64dfdgxnafkjfsc6eufk64mquw26d2
  ARTIFACT                                      SIZE    SHA256  DECRYPT
  postgres/mydb.dump.zst.age                    41 MiB  ok      ok
  files/var-www-my-project-uploads.tar.zst.age  12 MiB  ok      ok

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
  2026-10-17T18-00-00  42 MiB  keep    daily 1, weekly 1, monthly 1
  2026-10-17T06-00-00  42 MiB  delete
  2026-10-16T09-00-00  42 MiB  keep    daily 2

Preview only: 1 snapshot(s), 42 MiB would be deleted. Nothing was removed.
```

Notes:

- The newest snapshot is always kept.
- Folders without a valid manifest are shown as `skip` and never deleted.
- See [Retention](retention.md) for exactly how the policy decides.
