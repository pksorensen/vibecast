# vibecast as an ALP Operator

The [Assembly Line Protocol (ALP)](https://agentics.dk/alp) defines a four-layer architecture for running AI agents safely in production:

| Layer | Role |
|-------|------|
| **Server** | Task queue — where work enters the system |
| **Runner** | Deterministic orchestrator — pulls tasks, controls execution environment |
| **Operator** | Drives the agent — the last guardrail before non-deterministic execution |
| **Agent** | Executes the work |

vibecast is the reference **Operator** implementation. It sits between the Runner (pks-cli) and the Agent (Claude Code), and is responsible for:

- Launching and managing the agent process inside a controlled tmux session
- Publishing the session stream to the relay so humans can observe
- Reporting lifecycle events (session start, tool calls, completion) back to the Server
- Enforcing operator-level guardrails (image approval, keyboard pin, etc.)

## How a job flows through vibecast

```
Runner (pks-cli)
  │
  ├── pulls job from Server
  ├── builds launch script with env vars:
  │     SESSION_ID=<id>
  │     BROADCAST_ID=<assemblyLine.broadcastId>
  │     VIBECAST_APPEND_SYSTEM_PROMPT=<station prompt>
  │     AGENTICS_JOB_ID=<id>
  │     ... (repo, token, task prompt, etc.)
  │
  └── exec vibecast
        │
        ├── reads env vars, starts tmux + ttyd
        ├── connects broadcaster WS to ws-relay with sessionId + broadcastId
        ├── registers session with Server via POST /api/lives/session-event
        ├── launches Claude Code with:
        │     --plugin-dir claude-plugin/
        │     --append-system-prompt <station prompt>
        │     INITIAL_PROMPT=<task description>
        │
        ├── Claude Code runs — hooks fire back into vibecast hook
        │     → tool calls published to relay → viewers see live activity
        │
        └── Claude exits → vibecast reports conclusion → Runner marks job done
```

## Environment variables set by the Runner

| Variable | Description |
|----------|-------------|
| `SESSION_ID` | Unique ID for this job's vibecast session |
| `BROADCAST_ID` | Assembly line broadcast channel (optional — falls back to SESSION_ID) |
| `AGENTICS_JOB_ID` | Job ID in the Server's task queue |
| `AGENTICS_TOKEN` | Auth token for Server API calls |
| `AGENTICS_BASE_URL` | Server base URL |
| `AGENTICS_PROJECT` | `owner/project` slug |
| `VIBECAST_APPEND_SYSTEM_PROMPT` | Station-level system prompt appended to Claude's context |
| `VIBECAST_EXTRA_PLUGINS` | Additional Claude plugin directories |
| `VIBECAST_AUTO_APPROVE_IMAGES` | Skip image approval dialog (headless stations) |
| `CLAUDE_AUTO_UPDATE_DISABLED` | Opt out of the default update-to-latest run before the first spawn |
| `CLAUDE_VERSION` | Pin every station in the line to an exact Claude version (overrides auto-update) |
| `INITIAL_PROMPT` | The task description injected as the first user message |
| `AGENTICS_BACKGROUND_WAIT_HOLD_MINUTES` | How long the Server keeps a *parked* session marked active (default 30) |

> **Claude version is an operator concern, not line config.** Before the first
> spawn of a session, vibecast runs `claude update` once (frozen for the whole
> line so stations don't drift if a release lands mid-run). It is fail-open — a
> failed update never blocks the broadcast. Set `CLAUDE_VERSION` to pin a known
> build, or `CLAUDE_AUTO_UPDATE_DISABLED=1` to use whatever the image ships.

## Parking: waiting is cheaper than being forced to continue

The Stop hook was originally written to catch the end of a turn and push the agent
onward, because an agent that stopped was an agent that had given up. That premise
has expired. Claude Code now ends a turn on purpose while background shells,
subagents, monitors, workflows or a scheduled wake-up are still in flight, and
re-invokes itself the moment that work completes. Forcing a continuation there does
not make the job finish sooner — it spends a full model round-trip per poll on
something the agent was already going to be woken for.

So the Stop hook reads `background_tasks` and `session_crons` out of its own payload:

- **Work still in flight** → the stop is allowed. vibecast posts a `background_wait`
  event naming what is being waited on, and the Server holds the session `isActive`
  for a bounded window so the Runner does not read the deliberate quiet as
  "agent done". The first real activity after the wake clears the hold; a station
  that parks again simply takes a fresh one. The Runner's own max timeout is still
  the backstop.
- **`background_tasks` present and empty** → this is the true final stop, and every
  gate runs as before: auto-git, and the `stop_broadcast` requirement.
- **Key absent entirely** (codex, pi, older Claude Code) → unchanged legacy path.
  Absent is not the same as empty, and treating it as "done" would end those jobs
  early.

A park never spends the `stop_broadcast` block budget. That budget is there to stop
an endless "call stop_broadcast" loop, so it counts *consecutive* blocks and resets
on a park and on each new prompt — as a lifetime budget it would auto-conclude a
long station as `incomplete` on its third honest stop.

## Broadcast aggregation across stations

An Assembly Line is a pipeline of stations. Each station dispatches its own vibecast job, but all jobs share the same `BROADCAST_ID`. The relay aggregates all sessions under that broadcastId, so a viewer watching `/live/<broadcastId>` sees every active station simultaneously — each as its own window in the viewer UI.

```
Station 1: SESSION_ID=abc  BROADCAST_ID=xyz  →  /live/xyz (window 1)
Station 2: SESSION_ID=def  BROADCAST_ID=xyz  →  /live/xyz (window 2)
Station 3: SESSION_ID=ghi  BROADCAST_ID=xyz  →  /live/xyz (window 3)
```

## Operator capabilities

The Operator role in ALP is intentionally pluggable. vibecast declares its capabilities to the Runner via the control socket (`/status` endpoint):

- `sessionId` — the active session identifier
- `broadcastId` — the channel being streamed to
- Pane count and state

This lets the Runner report accurate status to the Server and link the job to the correct session for the viewer UI.

## Trust boundaries

```
Runner  (deterministic, auditable C# code)
  │  controls: job selection, env vars, working directory, resource limits
  │
  ▼
Operator  (vibecast — this repo)
  │  controls: tmux session lifecycle, Claude Code flags, plugin wiring
  │  guardrails: image approval, keyboard pin, system prompt injection
  │
  ▼
Agent  (Claude Code — non-deterministic)
     controls: file edits, tool calls, git operations
```

The Runner never directly touches tmux or the agent process — all agent interaction flows through the Operator. This separation ensures that even if the agent misbehaves, the Runner can terminate the job cleanly by killing the Operator process.
