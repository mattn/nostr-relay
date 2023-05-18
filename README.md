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
