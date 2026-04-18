# Session Resume

vibecast can recover a session after a crash or disconnect without losing context.

## Starting a resume

```bash
# By stream URL
vibecast --resume https://agentics.dk/live/abc123

# By session ID
vibecast --resume abc123
```

## Recovery paths

vibecast tries three recovery sources in order:

1. **tmux still alive** — the original tmux session is still running. vibecast reattaches to it, reconnects the broadcaster WebSocket, and continues streaming without disturbing Claude.

2. **Local session file** — `~/.vibecast/sessions/<sessionId>.json` exists with the last known state (Claude session ID, workspace path, broadcast ID). vibecast starts a new tmux session and relaunches Claude with `--resume <claudeSessionId>` so Claude picks up its conversation.

3. **Server fetch** — no local file. vibecast fetches the session metadata from `GET /api/lives/restore?streamId=<sessionId>`, which returns the persisted Claude session ID and project path. Same relaunch as path 2.

## What gets persisted

On `SessionStart`, vibecast writes:

- `sessionId` — the relay transport key
- `broadcastId` — the channel viewers watch
- `claudeSessionId` — Claude's internal session UUID (needed for `--resume`)
- `workspacePath` — the directory Claude was working in
- `projectName` — display name

This is stored both locally (`~/.vibecast/sessions/`) and on the server via `POST /api/lives/session-event`.

## Broadcast ID on resume

When resuming, vibecast reuses the original `broadcastId` — so viewers watching the stream URL see the session come back online without needing a new URL.

In Assembly Line mode, the Runner supplies the same `BROADCAST_ID` for retry jobs, ensuring the broadcast channel is stable across retries and handoffs between stations.
