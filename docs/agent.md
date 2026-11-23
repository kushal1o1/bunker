# Agent mode

Run bunker on the server itself. It backs the server up and pushes straight
to S3: no machine of yours has to be on, and nothing logs in to the server.

The agent holds only the age **public** key and an S3 key that can only
**upload**, so a hacked server can neither read nor delete old backups.
Verify, retention and restore stay on your laptop.

```
server (agent):  pg_dump / tar -> zstd -> age (public key) -> S3
laptop:          bunker status / list / verify / retention  <- S3
```

!!! note
    Agent mode is in development (`v0.5.0-dev`).

Two ways to run it, same binary:

- **Host agent**: a systemd timer on a plain server.
- **Container agent**: one container next to your Docker stacks. It covers
  databases in containers and named volumes, without the Docker socket or root.

## 1. On your laptop

Print the public key the agent will encrypt to:

```bash
bunker keygen --public
```

Add the server to `bunker.yaml` as an agent, with the same S3 bucket and
prefix the agent will use:

```yaml
servers:
  web-01:
    agent: true

storage:
  s3:
    endpoint: https://s3.eu-central-1.amazonaws.com
    bucket: my-bunker
    prefix: prod/
```

## 2. An S3 key that can only upload

Give the agent its own key with this policy and nothing else:

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": ["s3:PutObject", "s3:AbortMultipartUpload"],
    "Resource": "arn:aws:s3:::my-bunker/prod/*"
  }]
}
```

An upload key can still overwrite files. Turn on **bucket versioning** or
**Object Lock** so a hacked agent cannot destroy old snapshots by writing over
them.

## 3a. Host agent

The agent runs as a `bunker` user that can read only what you grant.
Ready-made files: [`examples/agent/host/`](https://github.com/kushal1o1/bunker/tree/main/examples/agent/host).

1. Create the user and grant read access to each path:

    ```bash
    sudo useradd --system --create-home bunker
    sudo setfacl -R -m u:bunker:rX /var/www/shop/media
    sudo setfacl -R -d -m u:bunker:rX /var/www/shop/media   # files created later
    ```

2. Give it a read-only database role. It logs in over the local socket as the
   OS user `bunker`, so no password is needed:

    ```sql
    CREATE ROLE bunker LOGIN;
    GRANT pg_read_all_data TO bunker;   -- Postgres 14+
    ```

3. Install the binary and write the config from the example files:

    ```
    /usr/local/bin/bunker
    /etc/bunker/agent.yaml      # edit: name, dbs, paths, s3, recipient
    /etc/bunker/agent.env       # the upload key; chmod 600
    ```

4. Check as the service will run (same user, same `agent.env`):

    ```bash
    sudo systemd-run --pipe --wait -p User=bunker \
      -p EnvironmentFile=/etc/bunker/agent.env /usr/local/bin/bunker agent check
    ```

5. Schedule it, every day at 03:00 (`--at` for another time):

    ```bash
    sudo bunker agent install
    ```

    This writes `bunker-agent.service` and `bunker-agent.timer` to
    `/etc/systemd/system` and enables the timer. Add `--dry-run` to see the
    files first. They are plain systemd files: edit them for any other
    schedule, or copy `bunker-agent.service` and `bunker-agent.timer` from the
    examples and enable the timer yourself.

## 3b. Container agent

One agent container per server covers every project on it. Ready-made files:
[`examples/agent/docker/`](https://github.com/kushal1o1/bunker/tree/main/examples/agent/docker).

1. Create a network for backups and attach **only the databases** to it, not
   the apps. Keep their ports unpublished.

    ```bash
    docker network create bunker-net
    ```

    ```yaml
    # in each project's docker-compose.yml
    services:
      postgres:
        networks: [default, bunker-net]
    networks:
      bunker-net: { external: true }
    ```

2. Give each database a read-only role with a password:

    ```bash
    docker exec -it shop-postgres psql -U postgres -c \
      "CREATE ROLE bunker LOGIN PASSWORD 'long-random'; GRANT pg_read_all_data TO bunker;"
    ```

3. Copy the example folder to `/opt/bunker-agent/` and edit `agent.yaml`, the
   volumes and `group_add` in `docker-compose.yml`. Put one line per database
   in `pgpass` (`host:port:database:user:password`).

4. Set the owners. `pgpass` is read inside the container (uid `10001`);
   `agent.env` is read by `docker compose` on the host, so it stays yours:

    ```bash
    sudo chown 10001 pgpass
    chmod 600 pgpass agent.env
    ```

5. Check:

    ```bash
    docker compose run --rm bunker-agent agent check
    ```

6. Schedule it, every day at 03:00 (`--at` for another time):

    ```bash
    sudo bunker agent install --docker /opt/bunker-agent
    ```

    This writes `/etc/cron.d/bunker-agent`, which runs
    `docker compose run --rm bunker-agent` in that folder. Without bunker on
    the host, add the line from `crontab.example` to the host's crontab
    instead.

### Reading files owned by other users

Volume files often belong to the app's user, for example `www-data` with mode
`640`. Add that group to the agent with `group_add` (`"33"` is `www-data` on
Debian images). The agent then reads them as a group member, with no extra
capabilities.

Files only their owner can read (mode `600`) cannot be backed up this way;
`agent check` names them.

## What the agent can and cannot do

| It has | It never has |
|---|---|
| the age public key | the private key |
| an upload-only S3 key | S3 read or delete |
| read-only database roles | database write |
| read access to what you grant or mount | root, the Docker socket, capabilities |
| outbound connections to S3 | a listening port |

## Removing it

```bash
sudo bunker agent uninstall
```

It disables the timer and removes the service, timer or cron file.
`agent.yaml`, `agent.env` and your snapshots in S3 stay.

## Checking on it

- `bunker status` on your laptop shows each agent's last run and newest
  snapshot, and exits `1` when one needs attention.
- On the server: `journalctl -u bunker-agent` (host) or
  `/var/log/bunker-agent.log` (container).
- A run that dies halfway leaves files without `manifest.json` (an upload key
  cannot delete them). They never count as a backup, and `retention apply
  --yes` on your laptop removes them after a day.

See [`agent run`](COMMANDS.md#agent-run), [`agent check`](COMMANDS.md#agent-check)
and [`status`](COMMANDS.md#status) for every option.
