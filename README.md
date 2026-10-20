<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/images/bunker-white.png">
    <img src="docs/assets/images/bunker-black.png" alt="bunker logo" width="180">
  </picture>
</p>
<h1 align="center">bunker</h1>

<p align="center">
  <b>A backup orchestrator for your fleet of servers.</b><br>
  One binary on your machine pulls encrypted, verified backups from all of them over SSH.
</p>

<p align="center">
  <a href="https://github.com/kushal1o1/bunker/actions/workflows/ci.yml"><img src="https://github.com/kushal1o1/bunker/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/kushal1o1/bunker/releases/latest"><img src="https://img.shields.io/github/v/release/kushal1o1/bunker?include_prereleases&label=release" alt="release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/kushal1o1/bunker" alt="license"></a>
  <a href="https://kushal1o1.github.io/bunker/"><img src="https://img.shields.io/badge/docs-online-black" alt="docs"></a>
</p>

bunker backs up your databases and files from every server in one run. It
pulls them over SSH (read-only), then compresses, encrypts and checksums
everything on your machine. It does not reinvent backup tools. It orchestrates
them.

```mermaid
flowchart LR
    subgraph fleet["your servers (read-only)"]
        V1["vps-01<br/>databases · files"]
        V2["vps-02<br/>databases · files"]
        V3["vps-N<br/>databases · files"]
    end
    subgraph you["your machine: bunker"]
        O["orchestrator<br/>concurrency · timeouts · keepalive"]
        P["zstd → age → sha256"]
    end
    V1 -->|ssh| O
    V2 -->|ssh| O
    V3 -->|ssh| O
    O --> P --> S[("storage<br/>snapshots + manifest.json")]
```


## Install

```bash
curl -LO https://github.com/kushal1o1/bunker/releases/latest/download/bunker-linux-amd64
chmod +x bunker-linux-amd64 && sudo mv bunker-linux-amd64 /usr/local/bin/bunker
```

macOS, Windows, checksums and `go install`: see
[Getting Started](https://kushal1o1.github.io/bunker/getting-started/).

## Quickstart

```yaml
# bunker.yaml
servers:
  web-01:
    host: 203.0.113.10
    ssh: { user: bunker }
    backup:
      postgres: { dbs: [shop_db] }
      paths: [/var/www/shop/media]
```

```bash
bunker keygen              # create the encryption key once
bunker backup run --all    # back up every server
bunker backup list         # see your snapshots
```

IPs in these docs (`192.0.2.x`, `198.51.100.x`, `203.0.113.x`) are reserved
documentation addresses ([RFC 5737](https://datatracker.ietf.org/doc/html/rfc5737)),
not real servers.

## Docs

<https://kushal1o1.github.io/bunker/>

## Development

```bash
go build -o bunker ./cmd/bunker && go test ./...
```

Issues and pull requests are welcome: see [CONTRIBUTING.md](CONTRIBUTING.md).
Security issues: see [SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE)
