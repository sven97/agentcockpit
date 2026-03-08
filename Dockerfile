# ── Build stage ────────────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w -X main.version=${VERSION}" \
    -o /agentcockpit ./cmd/agentcockpit

# ── Litestream stage ───────────────────────────────────────────────────────────
FROM litestream/litestream:0.3.13 AS litestream

# ── Runtime stage ──────────────────────────────────────────────────────────────
# alpine gives us a shell for the entrypoint script.
FROM alpine:3.21

RUN apk add --no-cache ca-certificates

COPY --from=builder    /agentcockpit              /agentcockpit
COPY --from=litestream /usr/local/bin/litestream  /usr/local/bin/litestream
COPY deploy/litestream.yml                        /etc/litestream.yml
COPY deploy/entrypoint.sh                         /entrypoint.sh
RUN chmod +x /entrypoint.sh

VOLUME ["/data"]

ENV DATA_DIR=/data
# Cloud Run injects PORT=8080; local default keeps 7080.
ENV PORT=7080

EXPOSE 8080

ENTRYPOINT ["/entrypoint.sh"]
