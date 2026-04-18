# Architecture

vibecast is built from three cooperating processes on your machine: the **TUI/orchestrator**, a **tmux session**, and a **ttyd web terminal bridge**. They connect to a remote server that fans your stream out to any number of browser viewers.

```
┌─────────────────────────── your machine ───────────────────────────┐
│                                                                     │
│  vibecast (TUI)                                                     │
│    ├── spawns tmux session                                          │
│    ├── spawns ttyd (websocket → tmux)                               │
│    ├── connects broadcaster WebSocket → ws-relay                    │
│    └── runs Claude Code hooks via claude-plugin/                    │
│                                                                     │
│  tmux                                                               │
│    └── pane 0: Claude Code (+ optional extra panes)                 │
│                                                                     │
│  ttyd                                                               │
│    └── bridges tmux pty → WebSocket (local port)                    │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
                          │ broadcaster WS
                          ▼
┌──────────────── agentics.dk (or self-hosted) ───────────────────────┐
│                                                                     │
│  ws-relay                                                           │
│    ├── /api/lives/broadcast/ws  ← vibecast connects here            │
│    ├── /api/lives/ws            ← browsers connect here             │
│    └── /_relay/*                ← internal Next.js ↔ relay IPC      │
│                                                                     │
│  Next.js                                                            │
│    ├── /live/[broadcastId]      viewer page                         │
│    └── /api/lives/*             metadata, auth, session restore     │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

## Session identity

Every vibecast session has two IDs:

| ID | Purpose |
|----|---------|
| `sessionId` | Internal transport key — identifies this vibecast process on the relay, used for file storage, tmux pane routing, session restore |
| `broadcastId` | The channel viewers watch — `https://agentics.dk/live/<broadcastId>` |

When running standalone, `broadcastId` defaults to `sessionId`. When launched by a Runner as part of an Assembly Line, the Runner passes `BROADCAST_ID` so all stations of a pipeline appear under a single viewer URL.

## Data flows

### Terminal data
```
tmux pty → ttyd (local WS) → vibecast broadcaster WS → ws-relay → viewer browser WS → xterm.js
```

### Hook metadata (tool calls, prompts, session events)
```
Claude Code hook → vibecast hook subcommand → POST /api/lives/metadata (Next.js)
                                                      │
                                              POST /_relay/fanout (relay)
                                                      │
                                              viewer WS → LiveViewer component
```

### Chat
```
viewer types message → chat WS → ws-relay → TUI chat panel
TUI sends reply       → chat WS → all viewers
```

## Multi-pane

vibecast supports multiple simultaneous Claude Code panes inside the same tmux session. Each pane:

- has a unique `paneId`
- streams independently over the broadcaster WebSocket (multiplexed as `sessionId:paneId`)
- appears as a separate window in the viewer's window manager UI

The F-key bar (rendered by `vibecast fkeybar` in its own tmux pane) shows which pane is active and provides keyboard shortcuts to create, switch, and stop panes.

## Broadcast aggregation (Assembly Line mode)

When multiple sessions share a `broadcastId`, the relay:

1. Maintains `broadcastSessions: Map<broadcastId, Set<sessionId>>`
2. Fans out pane data from any session to all viewers watching that broadcastId
3. Builds a unified `pane_list` so the viewer's window manager shows panes from all sessions

This is how an Assembly Line with three stations can be watched from a single `/live/<broadcastId>` URL — each station runs its own vibecast with the same `BROADCAST_ID`.
