# nostr-relay

nostr relay with backup method using litestream.

## Installation

1. Edit litestream.yaml


    ```yaml
    dbs:
      - path: /data/nostr-relay.sqlite
        replicas:
          - type: s3
            endpoint: https://your-s3-endoint
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

2. Deploy with kustomize

    ```
    $ kubectl apply -k kustomize
    ```

## License

MIT

## Author

Yasuhiro Matsumoto (a.k.a. mattn)
