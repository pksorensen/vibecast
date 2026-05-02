![vibecast](image.jpg)

# vibecast

Broadcast your agentic coding session live to the web — powered by Claude Code.

vibecast wraps your terminal in a tmux session, streams it over WebSocket, and lets anyone follow along in a browser in real-time. Claude Code hooks auto-publish tool calls, session metadata, and chat — so viewers always know what the agent is doing, not just what it's typing.

## Requirements

- **tmux** — vibecast runs inside a tmux session and will not start without it (`brew install tmux` / `apt install tmux`)
- **ttyd** — terminal-to-web bridge (`brew install ttyd` / build from source)
- **Claude Code** — `npm install -g @anthropic-ai/claude-code`
- **Node.js 18+** — to run `npx vibecast`

## Quick Start

```bash
npx vibecast
```

First run prompts you to log in. After that, vibecast opens a tmux lobby — pick a workspace, start Claude Code, and your session goes live at `https://agentics.dk/live/<your-broadcast-id>`. Share the URL with anyone; no viewer account needed.

## Run in a fresh VM (no local install)

Don't want tmux/ttyd/Claude on your machine? Spin up a sandbox VM with a devcontainer and run vibecast inside it:

```bash
npx @pks-cli/cli vibecast
```

This provisions an Azure VM, opens a devcontainer, and starts a vibecast session inside it. Useful for one-off demos or running untrusted code.

## Documentation

| Doc | Contents |
|-----|----------|
| [docs/architecture.md](docs/architecture.md) | How the pieces fit — TUI, tmux, ttyd, ws-relay, session identity, broadcast aggregation |
| [docs/hooks.md](docs/hooks.md) | Claude Code hook integration — events, masking, activity log |
| [docs/session-resume.md](docs/session-resume.md) | Session recovery — three paths, what gets persisted, broadcast ID continuity |
| [docs/alp-operator.md](docs/alp-operator.md) | vibecast as an ALP Operator — job flow, env vars, trust boundaries |
| [docs/self-hosting.md](docs/self-hosting.md) | Running your own server — Docker, env vars, auth |
| [docs/development.md](docs/development.md) | Build from source, full command list, environment variables |

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
