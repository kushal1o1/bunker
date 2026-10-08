## Global flags

| Flag | Default | Meaning |
|---|---|---|
| `-c, --config <path>` | `bunker.yaml` | config file to load |
| `-h, --help` | | help for any command |
| `-v, --version` | | print version |

Every command that reads config accepts `-c`. Paths inside the config support
`~` expansion.

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