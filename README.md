![vibecast](image.jpg)

# vibecast

Broadcast your agentic coding session live to the web — powered by Claude Code.

vibecast wraps your terminal in a tmux session, streams it over WebSocket, and lets anyone follow along in a browser in real-time. Claude Code hooks auto-publish tool calls, session metadata, and chat — so viewers always know what the agent is doing, not just what it's typing.

## Features

- **Live terminal streaming** — viewers watch your terminal in real-time via browser (no install required)
- **Claude Code integration** — hooks auto-report tool calls, approvals, session state, and metadata
- **Activity log** — viewers see a structured feed of prompts and tool calls alongside the terminal
- **Multi-pane** — spawn and switch between multiple Claude Code panes with an F-key bar
- **Chat** — viewers can send messages you see in the TUI
- **Session resume** — picks up where you left off after a crash (`--resume`)
- **Assembly Line support** — multiple pipeline stations stream to one viewer URL via a shared broadcast channel
- **OAuth login** — secure auth via `vibecast login` (PKCE flow)
- **Self-hostable** — point at your own server with `AGENTIC_SERVER`
- **OpenTelemetry** — optional trace export via `OTEL_EXPORTER_OTLP_ENDPOINT`

## Requirements

- **tmux** — session management (`brew install tmux` / `apt install tmux`)
- **ttyd** — terminal-to-web bridge (`brew install ttyd` / build from source)
- **Claude Code** — `npm install -g @anthropic-ai/claude-code`

## Install

```bash
npm install -g vibecast
```

Or build from source:

```bash
git clone https://github.com/pksorensen/vibecast
cd vibecast
go build -o vibecast ./main.go
```

## Quick Start

```bash
# Log in (first time)
vibecast login

# Start broadcasting
vibecast
```

A tmux lobby opens. Select a workspace and start Claude Code — your session goes live at `https://agentics.dk/live/<your-broadcast-id>`. Share the URL with anyone. No viewer account needed.

## Resume a session

```bash
vibecast --resume https://agentics.dk/live/abc123
vibecast --resume abc123
```

See [docs/session-resume.md](docs/session-resume.md) for how recovery works across crashes and restarts.

## Commands

```
vibecast                    Start broadcasting (opens tmux lobby)
vibecast --resume <id|url>  Resume an existing session
vibecast --broadcast-id <id> Use a specific broadcast channel ID
vibecast login              Authenticate with the server
vibecast logout             Remove saved credentials
vibecast hook               Claude Code hook handler (called by hooks.json)
vibecast mcp                Start MCP server mode
vibecast fkeybar            Internal: render F-key bar pane
vibecast sync               Sync session state
vibecast stop-broadcast     Stop an active broadcast
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `AGENTIC_SERVER` | `agentics.dk` | Server host to connect to |
| `BROADCAST_ID` | (sessionId) | Broadcast channel — set by Runner in Assembly Line mode |
| `SESSION_ID` | (generated) | Session identity for resume |
| `VIBECAST_DEBUG` | (off) | Enable debug logging to stderr |
| `VIBECAST_APPEND_SYSTEM_PROMPT` | (none) | Append text to Claude's system prompt |
| `VIBECAST_EXTRA_PLUGINS` | (none) | Colon-separated extra Claude plugin dirs |
| `VIBECAST_AUTO_APPROVE_IMAGES` | (off) | Skip image approval (headless/runner mode) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | (none) | OpenTelemetry trace endpoint |
| `OTEL_SERVICE_NAME` | `vibecast-cli` | Service name for traces |

## Documentation

| Doc | Contents |
|-----|----------|
| [docs/architecture.md](docs/architecture.md) | How the pieces fit — TUI, tmux, ttyd, ws-relay, session identity, broadcast aggregation |
| [docs/hooks.md](docs/hooks.md) | Claude Code hook integration — events, masking, activity log |
| [docs/alp-operator.md](docs/alp-operator.md) | vibecast as an ALP Operator — job flow, env vars, trust boundaries |
| [docs/session-resume.md](docs/session-resume.md) | Session recovery — three paths, what gets persisted, broadcast ID continuity |
| [docs/self-hosting.md](docs/self-hosting.md) | Running your own server — Docker, env vars, auth |

## Assembly Line Protocol (ALP)

vibecast is the reference **Operator** implementation of the [Assembly Line Protocol](https://agentics.dk/alp) — a four-layer architecture for running AI agents safely in production:

| Layer | Role | Implementation |
|-------|------|----------------|
| **Server** | Task queue — where work enters the system | [agentics.dk](https://agentics.dk) |
| **Runner** | Deterministic orchestrator — pulls tasks, controls execution | [pks-cli](https://github.com/pksorensen/pks-cli) |
| **Operator** | Drives the agent — last guardrail before non-deterministic execution | **vibecast** (this repo) |
| **Agent** | Executes the work | Claude Code |

The Operator role is deliberately pluggable — anyone can implement an Operator for a different agent by following the spec. vibecast currently targets Claude Code.

See [docs/alp-operator.md](docs/alp-operator.md) for the full protocol detail, and the [ALP security post](https://agentics.dk/blog/alp-security-by-design) for why this layering keeps agents safe.

## Self-Hosting

```bash
AGENTIC_SERVER=localhost:3000 vibecast
```

The server is the `ws-relay` + Next.js stack from [agentic-live-www](https://github.com/pksorensen/agentic-live-www). See [docs/self-hosting.md](docs/self-hosting.md) for full setup instructions.

## License

vibecast is licensed under the [Business Source License 1.1](LICENSE) (BUSL 1.1).

**Free for:**
- Broadcasting your own coding sessions (personal or professional)
- Self-hosting the server for your own team
- Contributing modifications back to this repo
- Any internal or non-competing use

**Requires a commercial license:**
- Building a hosted terminal broadcasting platform or live agentic coding service that competes with agentics.dk

After four years from each version's release, that version automatically converts to the Apache 2.0 license.

For commercial licensing enquiries: https://github.com/pksorensen/vibecast
