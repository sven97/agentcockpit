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

## Git Conventions

All agents (Claude, Codex, humans) must follow these rules consistently.

### Branch names

Format: `<type>/<short-description>` — lowercase, hyphens, no agent prefix.

| Type | When to use |
|---|---|
| `feat/` | New feature or capability |
| `fix/` | Bug fix |
| `chore/` | CI, config, deps, tooling |
| `refactor/` | Code restructure, no behaviour change |
| `docs/` | Documentation only |

Examples: `feat/e2e-encryption`, `fix/terminal-full-height`, `chore/path-based-release`

### Commit messages

Format: **Conventional Commits** — `<type>(<scope>): <description>`

- Type: `feat`, `fix`, `chore`, `refactor`, `docs`, `test`, `perf`
- Scope: package or area affected, e.g. `agent`, `relay`, `store`, `ui`, `ci`
- Description: imperative, lowercase, no trailing period, ≤72 chars
- Body (optional): explain *why*, not *what*; wrap at 72 chars

```
feat(agent): derive per-session key from user's ECDH public key

Generates an ephemeral P-256 keypair on session start and sends the
public key to the relay so the browser can derive the same AES-256-GCM
session key independently.
```

Common types at a glance:
```
feat(ui): add approval badge count to nav
fix(store): swallow duplicate-column error on re-migration
chore(ci): trigger release only on CLI path changes
refactor(relay): extract session sequence tracking to own struct
docs(readme): update install instructions
```

### Pull requests

- **Title**: identical to the commit message first line (same `type(scope): desc` format)
- **Body**: always include these two sections:

```markdown
## Summary
- bullet describing what changed and why (1–4 bullets)

## Test plan
- [ ] how to verify it works
```

- Keep PRs small and focused — one logical change per PR
- Always run `go build ./...` and `go test ./...` before pushing

### Branch protection — never push directly to main

`main` is **branch-protected**. Direct pushes are rejected. Always:

1. Create a branch: `git checkout -b fix/my-fix`
2. Push the branch: `git push -u origin fix/my-fix`
3. Open a PR: `gh pr create ...`
4. Wait for the `test` CI check to pass
5. Merge via PR: `gh pr merge <number> --squash --delete-branch`

### Post-merge monitoring

After merging any PR, **always monitor the Deploy workflow** to confirm the Cloud Run deployment succeeded:

```bash
gh run list --repo sven97/agentcockpit --limit 3
gh run watch <run-id> --repo sven97/agentcockpit
```

**CI/CD pipeline on push to main:**

| Workflow | Trigger | What it does |
|---|---|---|
| `CI` | PRs to main | `go test ./...` + `go build` (required status check) |
| `Deploy` | Every push to main | Builds Docker image → pushes to Artifact Registry → deploys to Cloud Run |
| `Release CLI` | Push to main touching `cmd/`, `internal/agent/`, `internal/protocol/`, `go.mod` | Auto-bumps patch version, creates git tag, runs goreleaser |

**Deploy checks:** Cloud Run health probe hits `GET /health` on port 8080. If the probe fails, the old revision stays live and the workflow exits with error. Check Cloud Run logs:

```bash
gcloud logging read 'resource.type="cloud_run_revision" AND resource.labels.service_name="agentcockpit"' --limit=30 --format="value(textPayload)"
```
