<p align="center">
  <img src="assets/images/bunker-black.png#only-light" alt="bunker logo" width="160">
  <img src="assets/images/bunker-white.png#only-dark" alt="bunker logo" width="160">
</p>

# bunker

**A backup orchestrator for your fleet of servers.** One binary on your machine
pulls encrypted, verified backups from all of them over SSH. Nothing to install
on the servers.

bunker backs up your databases and files from every server in one run. It does
not reinvent backup tools. It orchestrates them and handles the rest: many
servers in parallel, timeouts, encryption, verification and retention.

By default bunker pulls from your servers over SSH. Optionally, a server can
run bunker as an [agent](agent.md) and push to S3 by itself (dotted line):
nothing logs in, and no machine of yours has to be on.

```mermaid
flowchart LR
    subgraph fleet["your servers"]
        V1["vps-01<br/>databases · files"]
        V2["vps-02<br/>databases · files"]
        V3["vps-03<br/>bunker agent (optional)<br/>systemd or Docker"]
    end
    subgraph you["your machine: bunker"]
        O["orchestrator<br/>concurrency · timeouts · keepalive"]
        P["zstd → age → sha256"]
    end
    V1 -->|ssh, read-only| O
    V2 -->|ssh, read-only| O
    O --> P --> S[("storage: disk or S3<br/>snapshots + manifest.json")]
    V3 -.->|upload-only key| S
```

## How it works

1. Run a read-only command on the server (`pg_dump -Fc` or `tar`).
2. Stream it through zstd and age into a file on your machine.
3. Record each file's SHA-256 in `manifest.json`. Without it, the snapshot is
   incomplete.

```
~/backups/
  my-vps/
    2026-10-17T09-12-03/
      manifest.json
      postgres/mydb.dump.zst.age
      files/var-www-uploads.tar.zst.age
```

## Quickstart

```bash
bunker keygen                       # create the encryption key once
# edit bunker.yaml: one server, a database or a path
bunker backup run my-vps            # take a snapshot
bunker backup verify my-vps 2026-10-17T09-12-03   # prove it is readable
```

Today: PostgreSQL and files, to local disk, S3 or both. Next: Docker, restore, more databases.

## Docs

- [Getting Started](getting-started.md) - install and your first backup
- [Configuration](configuration.md) - every `bunker.yaml` field
- [Retention](retention.md) - pruning old snapshots
- [Commands](COMMANDS.md) - full command reference
- [Roadmap](roadmap.md) - what is done and what is next
