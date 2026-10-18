# Build:  docker build -f deploy/docker/Dockerfile -t redxd:local .     (from the repository root)
# Uses only the local source tree + the Go module proxy for dependencies. Not published anywhere.
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TAGS=""
ARG VERSION=0.1.0-dev
RUN CGO_ENABLED=0 go build -trimpath -tags "$TAGS" -ldflags "-s -w -X redx/host.Version=$VERSION" -o /out/redxd ./cmd/redxd \
 && CGO_ENABLED=0 go build -trimpath -tags "$TAGS" -ldflags "-s -w -X redx/host.Version=$VERSION" -o /out/redx ./cmd/redx

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 redx && mkdir /data && chown redx /data
COPY --from=build /out/redxd /out/redx /usr/local/bin/
USER redx
ENV REDX_DATA_DIR=/data REDX_LISTEN=0.0.0.0:7777
VOLUME /data
EXPOSE 7777
HEALTHCHECK --interval=10s --timeout=3s --retries=3 CMD wget -qO- http://127.0.0.1:7777/healthz >/dev/null || exit 1
ENTRYPOINT ["redxd", "run"]
