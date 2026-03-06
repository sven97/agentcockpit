# AgentCockpit

Centralized AI coding session manager. Run Claude Code, Codex CLI, and other AI agents across multiple machines — approve actions, view history, and manage sessions from a single web dashboard.

## Quick start

```bash
# Install
curl -fsSL agentcockpit.sh | sh

# Connect this machine
agentcockpit connect

# Set up Claude Code hooks
agentcockpit hooks setup
```

## Deployment modes

| Mode | Description |
|---|---|
| Local | `agentcockpit serve --local` — single machine, no auth |
| Self-hosted | Docker Compose on any Linux server |
| Cloud | [agentcockpit.app](https://agentcockpit.app) |

## Self-hosting

```bash
curl -fsSL https://raw.githubusercontent.com/sven97/agentcockpit/main/deploy/docker-compose.yml -o docker-compose.yml
AGENTCOCKPIT_SECRET=<random> docker compose up -d
```

## Development

```bash
git clone https://github.com/sven97/agentcockpit
cd agentcockpit
go run ./cmd/agentcockpit serve --local
```

## Docs

[agentcockpit.sh](https://agentcockpit.sh)
