# nostr-relay

A [nostr](https://github.com/nostr-protocol/nostr) relay built on the
[relayer](https://github.com/fiatjaf/relayer) framework. It supports several
storage backends (SQLite, PostgreSQL, MySQL and OpenSearch) and can optionally
back up its SQLite database with [litestream](https://litestream.io/).

## Contents

- [Install](#install)
- [Configuration](#configuration)
  - [Command-line flags](#command-line-flags)
  - [Environment variables](#environment-variables)
  - [NIP-11 information](#nip-11-information)
- [Storage backends](#storage-backends)
- [Running several instances](#running-several-instances)
- [Deployment](#deployment)
  - [systemd](#systemd)
  - [Docker](#docker)
  - [Docker Compose](#docker-compose)
  - [Kubernetes](#kubernetes)
- [License](#license)
- [Author](#author)

## Install

### From source

```
$ go install github.com/mattn/nostr-relay@latest
```

Or from a checkout of this repository:

```
$ make
```

### Container image

A prebuilt image is published to the GitHub Container Registry:

```
$ docker pull ghcr.io/mattn/nostr-relay:latest
```

## Configuration

```
$ nostr-relay [options]
```

### Command-line flags

| Flag            | Default          | Description                                            |
|-----------------|------------------|--------------------------------------------------------|
| `-addr`         | `0.0.0.0:7447`   | Listen address                                         |
| `-driver`       | `sqlite3`        | Storage driver: `sqlite3` / `postgresql` / `mysql` / `opensearch` |
| `-database`     | `nostr-relay.sqlite` | Connection string (see [Storage backends](#storage-backends)). Falls back to `$DATABASE_URL` |
| `-service-url`  | (empty)          | Public service URL. Falls back to `$SERVICE_URL`       |
| `-custom-search`| (empty)          | External search endpoint for NIP-50. Falls back to `$CUSTOM_SEARCH_URL` |
| `-redis`        | (empty)          | Redis URL to propagate events between instances (see [Running several instances](#running-several-instances)). Falls back to `$REDIS_URL` |
| `-version`      | `false`          | Print the version and exit                             |

### Environment variables

| Variable             | Description                                                        |
|----------------------|--------------------------------------------------------------------|
| `DATABASE_URL`       | Connection string (same as `-database`)                            |
| `SERVICE_URL`        | Public service URL (same as `-service-url`)                        |
| `CUSTOM_SEARCH_URL`  | External search endpoint for NIP-50 (same as `-custom-search`)     |
| `REDIS_URL`          | Redis URL to propagate events between instances (same as `-redis`) |
| `REDIS_CHANNEL`      | Redis pub/sub channel used with `REDIS_URL` (default `nostr-relay:events`) |
| `LOG_LEVEL`          | `debug` / `info` / `warn` / `error` (default `info`)               |
| `PUSHOVER_TOKEN`     | Pushover application token; enables NIP-56 (kind 1984) report notifications |
| `PUSHOVER_USER`      | Pushover user key (required together with `PUSHOVER_TOKEN`)        |
| `NOSTR_RELAY_*`      | Override NIP-11 relay information (see below)                       |

### NIP-11 information

Any field of the relay's NIP-11 information document can be overridden with a
`NOSTR_RELAY_`-prefixed environment variable:

```
NOSTR_RELAY_NAME="my relay"
NOSTR_RELAY_DESCRIPTION="a personal nostr relay"
NOSTR_RELAY_CONTACT="admin@example.com"
NOSTR_RELAY_PUBKEY="npub1xxxxx"
```

## Storage backends

### SQLite (default)

```
$ nostr-relay -database nostr-relay.sqlite
```

The connection string is a file path and may include
[go-sqlite3](https://github.com/mattn/go-sqlite3) options, for example
`nostr-relay.sqlite?_journal_mode=WAL`.

### PostgreSQL

Create the database first, then point the relay at it. The required tables are
created automatically on startup.

```
$ createdb nostr
$ nostr-relay -driver postgresql \
    -database "postgres://user:password@localhost:5432/nostr?sslmode=disable"
```

### MySQL

```
$ nostr-relay -driver mysql \
    -database "user:password@tcp(localhost:3306)/nostr"
```

### OpenSearch

```
$ nostr-relay -driver opensearch -database "https://localhost:9200"
```

## Running several instances

A subscription only receives events published to the same process, unless the
instances tell each other about the events they accept. Two ways are supported:

- **PostgreSQL**: instances sharing one database notify each other through
  `LISTEN`/`NOTIFY`. Nothing to configure.
- **Redis**: with `-redis` (or `REDIS_URL`), every accepted event is published on
  a Redis pub/sub channel and delivered by all instances subscribed to it. This
  works with any storage driver, and takes precedence over PostgreSQL's
  notifications when both are available.

```
$ nostr-relay -addr :7447 -database "nostr-relay.sqlite?_journal_mode=WAL&_busy_timeout=5000" -redis redis://localhost:6379 &
$ nostr-relay -addr :7448 -database "nostr-relay.sqlite?_journal_mode=WAL&_busy_timeout=5000" -redis redis://localhost:6379 &
```

The Redis URL follows [go-redis](https://github.com/redis/go-redis) conventions,
e.g. `rediss://user:password@host:6379` for TLS. Set `REDIS_CHANNEL` to keep
several relays on one Redis apart.

## Deployment

### systemd

Create a dedicated user and a working directory, then install a unit file at
`/etc/systemd/system/nostr-relay.service`:

```ini
[Unit]
Description=nostr-relay
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=nostr
Group=nostr
WorkingDirectory=/var/lib/nostr-relay
ExecStart=/usr/local/bin/nostr-relay -addr 0.0.0.0:7447 -database /var/lib/nostr-relay/nostr-relay.sqlite
Environment=LOG_LEVEL=info
Environment=NOSTR_RELAY_CONTACT=admin@example.com
Environment=NOSTR_RELAY_PUBKEY=npub1xxxxx
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

When using PostgreSQL or MySQL on the same host, add the database to the
ordering, e.g. `After=network-online.target postgresql.service`. Then enable and
start the service:

```
$ sudo systemctl daemon-reload
$ sudo systemctl enable --now nostr-relay
$ sudo journalctl -u nostr-relay -f
```

### Docker

```
$ docker run -d --name nostr-relay \
    -p 7447:7447 \
    -v "$PWD/data:/data" \
    -e DATABASE_URL=/data/nostr-relay.sqlite \
    ghcr.io/mattn/nostr-relay:latest
```

### Docker Compose

A [compose.yaml](./compose.yaml) is provided that runs the relay together with
litestream for continuous SQLite backup:

```
$ docker compose up -d
```

### Kubernetes

The [kustomize](./kustomize) manifests deploy the relay with litestream backup
to S3.

1. Edit litestream.yaml

    ```yaml
    dbs:
      - path: /data/nostr-relay.sqlite
        replicas:
          - type: s3
            endpoint: https://your-s3-endpoint
            name: nostr-relay.sqlite
            bucket: nostr-relay-backup
            path: nostr-relay.sqlite
            forcePathStyle: true
            sync-interval: 1s
            access-key-id: your-s3-access-key-id
            secret-access-key: your-secret-access-key
    ```

   * endpoint
   * access-key-id
   * secret-access-key

2. Create secret from litestream.yaml

    ```
    $ kubectl create secret generic litestream --from-file=litestream.yaml
    ```

3. Deploy with kustomize

    ```
    $ kubectl apply -k kustomize
    ```

4. Override NIP-11 information

    ```
    env:
    - name: DATABASE_URL
      value: /data/nostr-relay.sqlite
    - name: NOSTR_RELAY_CONTACT
      value: admin@example.com
    - name: NOSTR_RELAY_PUBKEY
      value: npub1xxxxx
    ```

## License

MIT

## Author

Yasuhiro Matsumoto (a.k.a. mattn)
