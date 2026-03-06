# ── Build stage ────────────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w -X main.version=${VERSION:-dev}" \
    -o /agentcockpit ./cmd/agentcockpit

# ── Runtime stage ──────────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12

COPY --from=builder /agentcockpit /agentcockpit

# Data directory (mount a volume here)
VOLUME ["/data"]

ENV DATA_DIR=/data
ENV PORT=7080

EXPOSE 7080

ENTRYPOINT ["/agentcockpit", "serve"]
