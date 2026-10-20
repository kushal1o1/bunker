# Security policy

bunker handles database dumps and encryption keys, so security reports are
taken seriously.

## Reporting a vulnerability

**Do not open a public issue.** Report it privately through GitHub:
**Security** tab, then **Report a vulnerability**.

Please include:

- the affected version (`bunker version`)
- what an attacker can do, and under what conditions
- steps to reproduce

You will get a reply within 7 days. Once a fix is released, the report is
credited unless you prefer otherwise.

## Supported versions

bunker is pre-1.0. Only the latest release gets security fixes.

## In scope

- Running anything on a server other than the allowlisted read-only commands
- Data written unencrypted, or the age private key leaving the control machine
- Bypassing SSH host key verification
- A tampered or truncated snapshot that passes `bunker backup verify`
- Path traversal in snapshot, server or artifact names

## Out of scope

- Compromise of the control machine itself (it holds your keys by design)
- Vulnerabilities in OpenSSH, `pg_dump`, `tar`, age or zstd themselves (report
  those upstream)
