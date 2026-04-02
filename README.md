# vibecast

Broadcast your agentic coding session live to the web — powered by Claude Code.

vibecast wraps your terminal in a tmux session, streams it over WebSocket, and lets anyone follow along in a browser in real-time. Designed for Claude Code workflows: hooks auto-publish tool calls, session metadata, and chat — so viewers always know what the agent is doing.

## Features

- **Live terminal streaming** — viewers watch your terminal in real-time via browser (no install required)
- **Claude Code integration** — hooks auto-report tool calls, approvals, session state, and metadata
- **Multi-pane support** — spawn and switch between multiple Claude Code panes with F-key bar (htop-style)
- **Chat** — viewers can send messages you see in the TUI
- **Session resume** — picks up where you left off after a crash (`--resume`)
- **OAuth login** — secure auth via `vibecast login` (PKCE flow)
- **Self-hostable** — point at your own server with `AGENTIC_SERVER`
- **OpenTelemetry** — optional trace export via `OTEL_EXPORTER_OTLP_ENDPOINT`

## Requirements

- **tmux** — session management (`brew install tmux` / `apt install tmux`)
- **ttyd** — terminal-to-web bridge (`brew install ttyd` / build from source)
- **Claude Code** — `npm install -g @anthropic-ai/claude-code`

## Install

### npm (recommended)

```bash
npm install -g vibecast
```

### Build from source

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

This opens a tmux lobby. Select a workspace and start Claude Code — your session is live at `https://agentics.dk/lives/<your-stream-id>`.

Share the URL with anyone. No viewer account needed.

## Resume a Session

```bash
# Resume by URL or stream ID
vibecast --resume https://agentics.dk/lives/abc123
vibecast --resume abc123
```

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `AGENTIC_SERVER` | `agentics.dk` | Server host to connect to |
| `VIBECAST_DEBUG` | (off) | Enable debug logging to stderr |
| `VIBECAST_APPEND_SYSTEM_PROMPT` | (none) | Append text to Claude's system prompt |
| `VIBECAST_EXTRA_PLUGINS` | (none) | Colon-separated extra Claude plugin dirs |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | (none) | OpenTelemetry trace endpoint |
| `OTEL_EXPORTER_OTLP_INSECURE` | `false` | Allow insecure OTLP endpoint |
| `OTEL_SERVICE_NAME` | `vibecast-cli` | Service name for traces |

## Self-Hosting

Point vibecast at your own server:

```bash
AGENTIC_SERVER=localhost:3000 vibecast
```

The server is the `ws-relay` + Next.js stack from [agentic-live-www](https://github.com/pksorensen/agentic-live-www). See that repo for self-hosting instructions.

## Commands

```
vibecast                    Start broadcasting (opens tmux lobby)
vibecast --resume <id|url>  Resume an existing session
vibecast login              Authenticate with the server
vibecast logout             Remove saved credentials
vibecast hook               Claude Code hook handler (called by hooks.json)
vibecast mcp                Start MCP server mode
vibecast fkeybar            Internal: render F-key bar pane
vibecast sync               Sync session state
```

## Claude Code Plugin

When you start a broadcast, vibecast automatically installs a Claude Code plugin (`claude-plugin/`) that wires up hooks for:

- Tool call events (PreToolUse / PostToolUse)
- Session start/stop
- Prompt metadata

This is what lets viewers see live status updates — not just the raw terminal output.

## License

MIT
