# syntax=docker/dockerfile:1.4

FROM golang:1.20-alpine AS build-dev
WORKDIR /go/src/app
COPY --link go.mod go.sum ./
RUN apk --update add --no-cache upx gcc musl-dev || \
    go version && \
    go mod download
COPY --link . .
RUN mkdir /data
RUN CGO_ENABLED=1 go install -buildvcs=false -trimpath -ldflags '-w -s -extldflags "-static"'
RUN [ -e /usr/bin/upx ] && upx /go/bin/bsky-haikubot || echo
FROM scratch
COPY --from=build-dev /data /data
COPY --link --from=build-dev /go/bin/nostr-relay /go/bin/nostr-relay
COPY --from=build-dev /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
CMD ["/go/bin/nostr-relay"]
