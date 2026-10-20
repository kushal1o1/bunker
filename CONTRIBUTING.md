# Contributing to bunker

Thanks for helping. Bug reports, ideas, docs fixes and code are all welcome.

## Before you start

- **Small fix** (typo, clear bug): open a pull request directly.
- **New feature or behaviour change**: open an issue first so we can agree on
  the approach. Check the [roadmap](https://kushal1o1.github.io/bunker/roadmap/)
  and its "Not planned" list.

## Setup

You need Go 1.27 or newer. Python is only needed for the docs.

```bash
git clone https://github.com/kushal1o1/bunker.git
cd bunker
go build -o bunker ./cmd/bunker
go test ./...
```

Docs:

```bash
python3 -m venv venv && venv/bin/pip install -r requirements.txt
venv/bin/mkdocs serve    # http://127.0.0.1:8000/bunker/
```

## Project layout

| Path | What lives there |
|---|---|
| `cmd/bunker` | `main`, nothing else |
| `internal/cli` | cobra commands, output, progress table, hints |
| `internal/config` | loading, defaults and validation of `bunker.yaml` |
| `internal/model` | config structs |
| `internal/backup` | artifacts, pipeline, orchestrator, manifest, verify, catalog |
| `internal/retention` | the retention policy (pure, no I/O) |
| `internal/sshx` | SSH connect, exec, timeouts, keepalive |
| `docs/` | the documentation site (MkDocs Material) |

## Rules that do not change

These are the project's promises. A pull request that breaks one will not be
merged.

- **The server only runs read-only commands.** All remote execution goes through
  `Artifact.Command()` and `sshx.Client.Exec`. Arguments are always quoted.
- **Encryption happens on the control machine.** Nothing is stored unencrypted,
  and the age private key never leaves it.
- **Destructive actions preview first** and need an explicit flag (like
  `retention apply --yes`).
- **No secrets in `bunker.yaml`.** Reference keys by path, and read credentials
  from the environment.
- **Stdout is for results, stderr for progress and hints**, so output can be
  piped.

## Code style

- Run `gofmt` and `go vet ./...` before you push.
- Keep it simple and standard library first. A new dependency needs a reason
  in the pull request.
- Wrap errors with context, `fmt.Errorf("read manifest: %w", err)`, and make
  error messages say what to do when possible:
  `unknown server "x" (try: bunker server list)`.
- Every new config field gets a default in `config.setDefaults` and a check in
  `Validate`, so users never have to write it.
- Comments explain *why*, not what the next line does.
- Anything that runs in goroutines must pass `go test -race`.

### CLI commands

Enforced by tests in `internal/cli/help_test.go`:

- `Short` is lowercase with no trailing period.
- `Long` and `Example` are set.
- The command has a `## <command>` section in `docs/COMMANDS.md`.

## Tests

```bash
go test ./...
go test -race ./internal/backup/ ./internal/sshx/ ./internal/cli/
```

- Every bug fix comes with a test that fails without the fix.
- SSH code is tested against an in-process fake SSH server (see
  `internal/backup/e2e_test.go` and `internal/sshx/sshx_test.go`). Never test
  against a real server.

## Docs

A change users can see ships with its docs in the same pull request:

- `docs/configuration.md` for new config fields
- `docs/COMMANDS.md` for commands and flags

Use documentation IPs only (`192.0.2.x`, `198.51.100.x`, `203.0.113.x`, see
[RFC 5737](https://datatracker.ietf.org/doc/html/rfc5737)), and check that the
site builds:

```bash
venv/bin/mkdocs build --strict
```

## Adding a new database

Each source is an `Artifact` (`internal/backup/artifact.go`):

```go
type Artifact interface {
    ID() string       // "mysql/shop_db"
    FilePath() string // "mysql/shop_db.sql.zst.age"
    Command() string  // read-only command whose stdout is the backup
}
```

1. Add the config struct in `internal/model`, plus defaults and validation in
   `internal/config`.
2. Implement the `Artifact` and add it in `buildArtifacts`.
3. Test `Command()` quoting, and add an end-to-end test with the fake server.
4. Document it in `docs/configuration.md`.

The pipeline, encryption, manifest and verify need no changes.

## Commits and branches

- Branch from `main`: `feat/<Name>`, `fix/<Name>`, `docs/<Name>`.
- Use [conventional commits](https://www.conventionalcommits.org/):
  `feat: ...`, `fix: ...`, `docs: ...`, `refactor: ...`, `test: ...`,
  `chore: ...`. Reference the issue: `feat: add mysql artifact iref(#21)`.
- Keep one logical change per pull request.

## Pull request checklist

- [ ] `gofmt`, `go vet ./...` and `go test ./...` pass
- [ ] `go test -race` passes for concurrent code
- [ ] Tests added or updated
- [ ] Docs updated, and `mkdocs build --strict` passes
- [ ] None of the rules above are broken
