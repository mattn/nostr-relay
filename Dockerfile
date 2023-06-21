# syntax=docker/dockerfile:1.4

FROM golang:1.20 AS build-dev
RUN apt update
RUN apt install -y gcc
WORKDIR /go/src/app
COPY --link go.mod go.sum ./
COPY --link . .
RUN mkdir /data
RUN CGO_ENABLED=1 go build -buildvcs=false -trimpath -ldflags '-w -s' -o /go/bin/nostr-relay
FROM scratch AS stage
COPY --from=build-dev /data /data
ENV DATABASE_URL /data/nostr-relay.sqlite
COPY --link --from=build-dev /go/bin/nostr-relay /go/bin/nostr-relay
CMD ["/go/bin/nostr-relay"]
