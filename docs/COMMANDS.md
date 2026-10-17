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

After `backup run --dry-run` and after a single-server `backup run`, bunker
prints one suggested next command, ready to paste:

```
Next: bunker backup verify my-vps 2026-10-08T13-32-59
```

Hints go to stderr and only appear in an interactive terminal, so cron jobs
and piped output never see them. If you passed `-c`, the hint keeps it. Turn
hints off with `--no-hints` or by setting `BUNKER_NO_HINTS=1`.

---

## Commands

| Command | Purpose |
|---|---|
| `bunker version` | print version |
| `bunker keygen [--force]` | generate the age encryption identity |
| `bunker server list` | show configured servers |
| `bunker backup run [server...]` | run a backup |
| `bunker backup verify <server> <snapshot>` | verify a snapshot's checksums, archive integrity and decryption |
| `bunker completion <shell>` | generate a shell completion script |
| `bunker help [command]` | cobra help |

---