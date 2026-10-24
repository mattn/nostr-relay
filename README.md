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
  - [Paid writes](#paid-writes)
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
| `-driver`       | `sqlite3`        | Storage driver: `sqlite3` / `turso` / `postgresql` / `mysql` / `opensearch` / `firestore` |
| `-database`     | `nostr-relay.sqlite` | Connection string (see [Storage backends](#storage-backends)); the GCP project ID for `firestore`. Falls back to `$DATABASE_URL` |
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
| `FIRESTORE_DATABASE` | Firestore database ID for `-driver firestore` (default: the project's default database) |
| `FIRESTORE_COLLECTION` | Firestore collection for `-driver firestore` (default `nostr-events`) |
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

The variables are read once at startup. Limitations use the
`NOSTR_RELAY_LIMITATION_` prefix and are the values the relay enforces, not just
the ones it advertises:

```
NOSTR_RELAY_LIMITATION_MAXCONTENTLENGTH=8192
NOSTR_RELAY_LIMITATION_CREATEDATLOWERLIMIT=94608000   # seconds into the past (0: no limit)
NOSTR_RELAY_LIMITATION_CREATEDATUPPERLIMIT=900        # seconds into the future (default 1800)
```

`default_limit` follows `max_limit`, and `restricted_writes` is set while the
allowlist is not empty. A relay that charges can advertise its fees and where to
pay; `NOSTR_RELAY_FEES` takes the NIP-11 `fees` object as JSON:

```
NOSTR_RELAY_PAYMENTSURL="https://relay.example.com/pay"
NOSTR_RELAY_LIMITATION_PAYMENTREQUIRED=true
NOSTR_RELAY_FEES='{"admission":[{"amount":1000000,"unit":"msats"}]}'
```

### Paid writes

Optionally, the relay can charge a fee for each event, taken from a prepaid
balance per pubkey. Balances are topped up through a
[Bark](https://gitlab.com/ark-bitcoin/bark) wallet daemon (`barkd`), in either
or both of two ways:

- `ark`: each pubkey gets its own Ark address, and an Ark payment to it is
  credited as soon as it shows up in the wallet history;
- `lightning`: a Lightning invoice, received through the Ark server's Lightning
  gateway.

It is off unless `BARKD_URL` is set, and needs a SQL driver (`sqlite3`, `turso`,
`postgresql` or `mysql`) for the balances.

| Variable                   | Description                                                  |
|----------------------------|--------------------------------------------------------------|
| `BARKD_URL`                | barkd REST URL, e.g. `http://127.0.0.1:3000`; enables paid writes |
| `BARKD_TOKEN`              | barkd auth token, if it has one                              |
| `PAID_WRITES_FEE_MSATS`    | fee per event in msats (default `1000`)                      |
| `PAID_WRITES_KINDS`        | comma-separated kinds to charge (default: all)               |
| `PAID_WRITES_RAILS`        | `ark`, `lightning` or `ark,lightning` (default)              |
| `PAID_WRITES_POLL_SECONDS` | how often to look for payments (default `5`)                 |

The NIP-11 document then advertises `payment_required`, `restricted_writes`,
`fees.publication` and `payments_url`, which is `/pay` on the `-service-url`
unless `NOSTR_RELAY_PAYMENTSURL` says otherwise. An event without enough credit
is rejected with `restricted:` and that URL.

```
$ curl 'https://relay.example.com/pay?pubkey=<hex>&amount=1000'
{"ark_address":"ark1...","balance_msats":0,"bip321":"bitcoin:?ark=ark1...&lightning=lnbc...&amount=0.00001","fee_msats":1000,"invoice":"lnbc...","pubkey":"<hex>"}
```

`amount` (sats) asks for a Lightning invoice; without it only the balance and
the Ark address are returned. `bip321` is the payment request as one
[BIP-321](https://github.com/bitcoin/bips/blob/master/bip-0321.mediawiki) URI,
with the rails that are on. A client can pass it unchanged to its wallet with
[NWC-321](https://github.com/nostr-wallet-connect/nwc/blob/main/321.md) `pay`: an
Ark wallet on the relay's Ark server pays the Ark address, any other wallet pays
the invoice.

## Storage backends

### SQLite (default)

```
$ nostr-relay -database nostr-relay.sqlite
```

The connection string is a file path and may include
[go-sqlite3](https://github.com/mattn/go-sqlite3) options, for example
`nostr-relay.sqlite?_journal_mode=WAL`.

### Turso

```
$ nostr-relay -driver turso \
    -database "libsql://your-db.turso.io?authToken=..."
```

The same SQLite schema, but served by [Turso](https://turso.tech) over the
network, so several instances can share it (see
[Running several instances](#running-several-instances)).

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

### Firestore

```
$ nostr-relay -driver firestore -database my-gcp-project
```

Credentials come from the usual Google application default credentials.
`FIRESTORE_DATABASE` and `FIRESTORE_COLLECTION` select the database and the
collection.

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

What each driver needs to scale out:

| Driver       | Sharing the storage                                   | Notifying the other instances |
|--------------|-------------------------------------------------------|-------------------------------|
| `sqlite3`    | Same host only: one file in WAL mode with `_busy_timeout` | Redis                     |
| `turso`      | Anywhere: the database is served over the network     | Redis                         |
| `postgresql` | Anywhere                                              | Built in (`LISTEN`/`NOTIFY`), or Redis |
| `mysql`      | Anywhere                                              | Redis                         |
| `opensearch` | Anywhere                                              | Redis                         |
| `firestore`  | Anywhere                                              | Redis                         |

So with `turso` and Redis, instances can run on different machines just like
with PostgreSQL, without sharing a file system. Whatever the driver, start the
first instance on its own against an empty database: the schema is created on
startup, and several instances doing so at once can collide.

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
