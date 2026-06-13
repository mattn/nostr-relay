# nostr-relay

A [nostr](https://github.com/nostr-protocol/nostr) relay built on the
[relayer](https://github.com/fiatjaf/relayer) framework. It supports several
storage backends (SQLite, PostgreSQL, MySQL and OpenSearch) and can optionally
back up its SQLite database with [litestream](https://litestream.io/).

## Build

```
$ go install github.com/mattn/nostr-relay@latest
```

Or from a checkout of this repository:

```
$ make
```

A prebuilt container image is published to the GitHub Container Registry:

```
$ docker pull ghcr.io/mattn/nostr-relay:latest
```

## Usage

```
$ nostr-relay [options]
```

| Flag            | Default          | Description                                            |
|-----------------|------------------|--------------------------------------------------------|
| `-addr`         | `0.0.0.0:7447`   | Listen address                                         |
| `-driver`       | `sqlite3`        | Storage driver: `sqlite3` / `postgresql` / `mysql` / `opensearch` |
| `-database`     | `nostr-relay.sqlite` | Connection string (see below). Falls back to `$DATABASE_URL` |
| `-service-url`  | (empty)          | Public service URL. Falls back to `$SERVICE_URL`       |
| `-custom-search`| (empty)          | External search endpoint for NIP-50. Falls back to `$CUSTOM_SEARCH_URL` |
| `-version`      | `false`          | Print the version and exit                             |

## Configuration

In addition to the flags above, the following environment variables are
recognized:

| Variable             | Description                                                        |
|----------------------|--------------------------------------------------------------------|
| `DATABASE_URL`       | Connection string (same as `-database`)                            |
| `SERVICE_URL`        | Public service URL (same as `-service-url`)                        |
| `CUSTOM_SEARCH_URL`  | External search endpoint for NIP-50 (same as `-custom-search`)     |
| `LOG_LEVEL`          | `debug` / `info` / `warn` / `error` (default `info`)               |
| `PUSHOVER_TOKEN`     | Pushover application token; enables NIP-56 (kind 1984) report notifications |
| `PUSHOVER_USER`      | Pushover user key (required together with `PUSHOVER_TOKEN`)        |
| `NOSTR_RELAY_*`      | Override NIP-11 relay information, e.g. `NOSTR_RELAY_NAME`, `NOSTR_RELAY_DESCRIPTION`, `NOSTR_RELAY_CONTACT`, `NOSTR_RELAY_PUBKEY` |

For example, to override the NIP-11 information document:

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

## Running as a systemd service

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

## Docker

```
$ docker run -d --name nostr-relay \
    -p 7447:7447 \
    -v "$PWD/data:/data" \
    -e DATABASE_URL=/data/nostr-relay.sqlite \
    ghcr.io/mattn/nostr-relay:latest
```

A [compose.yaml](./compose.yaml) is also provided that runs the relay together
with litestream for continuous SQLite backup:

```
$ docker compose up -d
```

## Kubernetes (with litestream backup)

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
