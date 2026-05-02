# Development

Build from source, run from the repo, and the full command + environment reference.

## Build from source

```bash
git clone https://github.com/pksorensen/vibecast
cd vibecast
go build -o vibecast ./main.go
```

Requires Go 1.24+. The resulting binary is self-contained — drop it on `$PATH` and run.

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

## Environment variables

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
