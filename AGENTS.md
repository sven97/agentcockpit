# AGENTS.md

This file provides guidance to Codex (Codex.ai/code) when working with code in this repository.

## Commands

```bash
# Run locally (no auth, single user)
go run ./cmd/agentcockpit serve --local

# Run tests
go test ./...

# Build (CGO disabled — pure Go SQLite)
CGO_ENABLED=0 go build -ldflags="-s -w" -o agentcockpit ./cmd/agentcockpit

# Build with version
CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=1.2.3" -o agentcockpit ./cmd/agentcockpit
```

Always build with `CGO_ENABLED=0` — the SQLite driver (`modernc.org/sqlite`) is pure Go.

## Architecture

AgentCockpit is a **relay server** that sits between host agents (running on developer machines) and a browser dashboard. Three deployment modes share identical code, differing only in config: local (`--local` flag), self-hosted, and cloud.

### Three-process flow

```
Codex → [hook shim] → [agent daemon] → (WebSocket) → [relay server] → [browser]
```

1. **`agentcockpit serve`** — the relay server. Hosts connect via WebSocket (`/ws/host`), browsers connect via WebSocket (`/ws/browser`). REST API handles sessions, approvals, and auth.
2. **`agentcockpit install`** — authorizes this machine and installs a background daemon service (launchd/systemd) that connects outbound to the relay. For debugging, `agentcockpit agent` runs the daemon in the foreground.
3. **`agentcockpit hook`** — the hook shim, registered as Codex's `PreToolUse` hook. Called per tool use, connects to the daemon's Unix socket, and blocks until the user approves or rejects from the browser. Fails open (allows) if the daemon isn't running.

### Package map

| Package | Purpose |
|---|---|
| `cmd/agentcockpit/` | Cobra CLI entry point. Subcommands include `install`, `connect` (legacy alias), `agent`, `hook`, `hooks`, `serve`, `user` |
| `internal/protocol/` | Shared WebSocket message types and constants between relay and agent |
| `internal/store/` | SQLite persistence — `Store` interface, `models.go` (structs), `sqlite.go` (implementation) |
| `internal/relay/` | WebSocket hub — routes messages between `HostConn` and `BrowserConn` pools, manages approval request/response pairing |
| `internal/server/` | HTTP server — REST API handlers, WebSocket upgrade handlers, embedded web UI |
| `internal/agent/` | Host daemon — outbound WebSocket to relay, Unix socket listener for hook shim, PTY session pool |

### Web UI

The UI is a **single vanilla HTML/CSS/JS file** at `internal/server/webdist/index.html`, embedded into the Go binary at build time via `//go:embed webdist`. No build step, no framework, no bundler. Edit this file directly.

### Approval flow (critical path)

1. Codex calls `agentcockpit hook` (stdin: tool name + input JSON)
2. Hook shim connects to daemon Unix socket, sends `HookRequest`, **blocks**
3. Daemon forwards as `approval_request` WebSocket message to relay
4. Relay persists to DB, fans out to all browser connections for that user
5. User approves/rejects in browser → browser sends `approval_response` over WebSocket
6. Relay routes response to daemon → daemon unblocks hook shim via channel
7. Hook shim writes decision JSON to stdout; exits with code 2 on deny

### Binary PTY frames

PTY output is sent as binary WebSocket frames with a 33-byte header:
`[0x01 byte][32-byte ASCII hex sessionId][raw PTY data]`

### Auth

- Tokens are stored as SHA-256 hashes in SQLite; raw tokens are never persisted
- WebSocket clients pass token via `?token=` query param (can't set headers)
- Local mode bypasses all auth — implicit `local@localhost` admin user

### Host registration

Hosts self-register via an invite flow: browser generates a short-lived invite token (`POST /api/hosts/invite`), CLI claims it (`POST /api/hosts/claim`). `agentcockpit install --invite <token>` performs the claim and daemon setup in one step.
