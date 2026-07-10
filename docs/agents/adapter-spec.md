# AgentAdapter specification

The Go interface that makes vibecast's coding agent pluggable. Lives in `internal/agent`.
Selected at startup by `VIBECAST_AGENT` (`claude` | `codex` | `pi`, default `claude`).
Unknown value → fail fast with a clear error before any tmux session is created.

Design constraints (from [research](research/), see [README](README.md) decision log):

- The **platform contract is frozen**: adapters must produce today's metadata subtypes and
  session-event calls. Nothing upstream of vibecast changes in v1 except opt-in renames.
- The Claude adapter must be a **pure extraction** — byte-identical launch commands, same
  hook behavior, so `VIBECAST_AGENT=claude` (and unset) is a no-op refactor.
- Everything tmux/ttyd/relay/control-socket stays **outside** the interface.

## 1. The interface

```go
package agent

// Kind identifies an agent implementation.
type Kind string // "claude" | "codex" | "pi"

type Adapter interface {
    // --- identity ---
    Kind() Kind
    DisplayName() string          // "Claude Code", "Codex", "pi" — used in UI copy
    BinaryName() string           // looked up with exec.LookPath
    Version(ctx context.Context) (string, error)

    // --- capabilities (declared, never probed; see conformance-suite.md) ---
    Capabilities() Capabilities

    // --- launch (stream.go seam) ---
    // BuildCommand returns the shell command for tmux new-window / respawn-pane.
    // The caller wraps it with `cd <workdir> &&` and the exit-code echo, as today.
    BuildCommand(spec LaunchSpec) (string, error)
    BuildResumeCommand(spec LaunchSpec, agentSessionID string) (string, error)

    // --- event wiring (installed before launch) ---
    // Install writes whatever the agent needs so that its events reach
    // `vibecast hook <event>`: claude → --plugin-dir flag (no-op here),
    // codex → hooks.json, pi → nothing (extension is passed via -e in BuildCommand).
    // Returns env vars to inject into the tmux session (e.g. VIBECAST_BIN).
    InstallEventWiring(spec LaunchSpec) (env map[string]string, cleanup func(), err error)

    // --- ingestion (hooks.go seam) ---
    // ParseHookEnvelope converts the agent-native stdin payload of
    // `vibecast hook <event>` into a NormalizedAgentEvent.
    ParseHookEnvelope(event string, stdin []byte) (*NormalizedAgentEvent, error)
    // SerializeHookResponse converts a normalized intent (deny / inject-context /
    // block-stop) into the agent-native hook stdout + exit code.
    SerializeHookResponse(intent HookIntent) (stdout []byte, exitCode int)

    // --- session identity + resume ---
    SessionIdentity() SessionIdentity // Preassign | Discover
    // SessionStore lists this agent's own sessions for the resume picker / sync.
    ListSessions(workspace string) ([]AgentSessionInfo, error)

    // --- version management (ensureClaudeUpToDate seam) ---
    // EnsureVersion honours pin ("" = update to latest unless auto-update disabled).
    // MUST stay fail-open with a timeout, as today.
    EnsureVersion(ctx context.Context, pin string) error

    // --- TUI automation (broadcast.go seam) ---
    ScreenGates() []ScreenGate       // detection: first-run/auth dialogs
    AnswerHandlers() []AnswerHandler // injection: (questionType × versionGlob) → keys
    ClassifyURL(url string) string   // "" or provider context, e.g. "claude-login"

    // --- vibecast tools (stop_broadcast / chat_reply / share_*) ---
    // RegisterVibecastTools makes the conclusion contract available to the agent:
    // claude/codex → MCP registration (.mcp.json / config.toml [mcp_servers]);
    // pi → tools are registered by the vibecast extension itself (no-op here).
    RegisterVibecastTools(spec LaunchSpec) (cleanup func(), err error)

    // --- telemetry ---
    TelemetryEnv(otelEndpoint string) map[string]string // CLAUDE_CODE_ENABLE_TELEMETRY etc.

    // --- liveness (Stop-hook "N local agents" sniff replacement) ---
    // BusySignal inspects a captured pane and reports whether the agent still has
    // background work (subagents) running. Return false, ErrNotSupported when the
    // agent has no such concept.
    BusySignal(paneText string) (busy bool, err error)
}

type LaunchSpec struct {
    Workdir           string
    AgentSessionID    string // pre-assigned id when SessionIdentity == Preassign
    Model, ModelTier  string // adapter maps tier→model per its own table
    Effort            string // adapter maps to --effort / model_reasoning_effort / --thinking
    SystemPromptFile  string // station prompt (file indirection avoids quoting bugs)
    InitialPromptFile string
    PermissionMode    PermissionMode // BypassAll (job mode today) | Default
    ExtraArgs         []string       // opaque passthrough (VIBECAST_AGENT_EXTRA_ARGS)
    PluginDirs        []string       // claude-only concept; others ignore
    StreamID          string         // vibecast session id, for event wiring env
}

type SessionIdentity int
const (
    Preassign SessionIdentity = iota // vibecast mints the id and passes it on launch
    Discover                         // id arrives in the first session_start event
)
```

`Capabilities` is a struct of booleans matching the
[feature-matrix capability table](feature-matrix.md#capability-declaration-summary):
`Lifecycle`, `ToolCalls`, `PromptCapture`, `SessionEndEvent`, `SessionPreassign`,
`SessionResume`, `GuardDeny`, `GuardMutate`, `NativeApprovals`, `SystemPromptAppend`,
`PlanEvents`, `SubagentEvents`, `CompactionEvents`, `VibecastTools`, `TranscriptUpload`,
`TokenUsage`.

## 2. Normalized event model

Internal only — the platform keeps receiving today's metadata subtypes. Vocabulary is
ACP-aligned (toolCallId, stopReason) so a future ACP backend is a field mapping.

```go
type NormalizedAgentEvent struct {
    Event          string // session_start|prompt|pre_tool|post_tool|turn_end|session_end|
                          // permission_request|plan|subagent_start|subagent_stop|
                          // pre_compact|post_compact|task_created|task_completed
    AgentKind      Kind
    AgentSessionID string // the agent's OWN session id (was: claudeSessionId)
    Workspace      string // cwd — used for stream-session lookup (unchanged)
    TranscriptPath string // agent-native transcript/rollout/session file, if any

    Prompt      string            // prompt
    Source      string            // session_start: startup|resume|clear|compact
    ToolName    string            // pre_tool/post_tool/permission_request (native name)
    ToolInput   json.RawMessage
    ToolCallID  string
    ToolOutput  json.RawMessage   // post_tool
    IsError     bool              // post_tool
    Text        string            // turn_end final assistant text, plan markdown, compact summary
    Usage       *TokenUsage       // normalized to Anthropic key names on emission
    Extra       map[string]any    // agent-specific leftovers (subagent fields, trigger, …)
}

type HookIntent struct {
    Kind   IntentKind // Allow | Deny | InjectContext | BlockStop
    Reason string
    Text   string     // InjectContext payload
}
```

**Emission** (normalized event → platform metadata subtype) is shared code, not per-adapter:
`pre_tool`→`tool_use`, `post_tool`→`tool_use_end`, `turn_end`→final `assistant_response`,
`session_start`→`session_start` (+ persists AgentSessionID), etc. Tool-name mapping to the
platform's rendering expectations (`Bash`/`Write`/`Edit`) is a small per-adapter table used
at emission time (codex `apply_patch` → `Edit`-class rendering; pi's `bash`→`Bash`,
`edit`/`write`→`Edit`/`Write`). Unmapped names pass through verbatim — the viewer renders
unknown tools generically.

**Timestamps**: all emissions use Unix seconds. This fixes the existing `url_detected`
milliseconds inconsistency (viewer already tolerates both; new code emits seconds only).

## 3. Ingestion topologies per agent

All three funnel into the same `vibecast hook <event>` entrypoint (session lookup by
workspace, masking, metadata POST, question-vote polling stay shared):

- **claude** — unchanged: `claude-plugin/hooks/hooks.json` via `--plugin-dir`.
  `ParseHookEnvelope` is the existing parsing, extracted.
- **codex** — `InstallEventWiring` writes `hooks.json` (same `vibecast hook <event>`
  commands) into a vibecast-managed `$CODEX_HOME` or `<workdir>/.codex/`; launch includes
  `--dangerously-bypass-hook-trust`. Envelope fields are near-identical to Claude's;
  the parser mainly relabels (`turn_id`, `call_*` ids) and handles `apply_patch`.
  Turn-completion redundancy: `notify` config may be added later; Stop hook suffices.
  Session end: **synthesized** — no SessionEnd hook exists; pane-exit detection (already
  present via `remain-on-exit` + `pane_dead` probe) emits the normalized `session_end`.
- **pi** — vibecast ships `pi/vibecast.ts` (analogous to `claude-plugin/`). The extension
  subscribes to pi events and **execs `${VIBECAST_BIN} hook <event>`** with a synthesized
  envelope (JSON on stdin, `agent:"pi"` field). For `tool_call` it runs the guard
  synchronously and maps a Deny response to `{block: true, reason}`. It also registers
  `stop_broadcast` / `chat_reply` / `share_image` as native pi tools that call the existing
  control socket — replacing MCP, which pi does not support.

## 4. Guard

`internal/hooks` keeps ONE policy engine (`dangerousProcessKill`, job-mode path
containment, future rules). Adapters only translate:

1. the native pre-tool payload → normalized (`ParseHookEnvelope`),
2. the policy verdict → native deny (`SerializeHookResponse` for claude/codex JSON+exit
   codes; the pi extension maps verdict → `{block:true}`).

Path-containment needs the per-agent tool taxonomy: claude `Write/Edit/MultiEdit/
NotebookEdit` field `file_path`/`notebook_path`; codex `apply_patch` (paths inside the
patch body — v1 may guard `Bash` only and rely on codex `sandbox_mode workspace-write`
as the backstop); pi `write`/`edit` args.

Rules stay **semantic**: the codex probe showed a denied `rm` being replayed as
`find -delete`. Deny reasons must state the *intent* blocked ("process-kill of the
operator", "write outside job worktree") so models don't treat it as a syntax puzzle.
Sandbox/approval configs remain enabled as defense in depth wherever the agent has them.

## 5. Env contract (Runner ⇄ vibecast)

New generic names; old `*CLAUDE*` names remain as claude-scoped fallbacks (read in this
order, first hit wins):

| New | Old (kept) | Meaning |
|---|---|---|
| `VIBECAST_AGENT` | — | `claude` (default) \| `codex` \| `pi` |
| `VIBECAST_MODEL` | `VIBECAST_CLAUDE_MODEL` | exact model id (adapter-validated) |
| `VIBECAST_MODEL_TIER` | `VIBECAST_CLAUDE_MODEL_TIER` | tier alias; per-adapter table |
| `VIBECAST_EFFORT` | `VIBECAST_CLAUDE_EFFORT` | reasoning effort; per-adapter mapping |
| `VIBECAST_AGENT_VERSION` | `CLAUDE_VERSION` | pin agent version for the whole line |
| `VIBECAST_AGENT_AUTO_UPDATE_DISABLED` | `CLAUDE_AUTO_UPDATE_DISABLED` | skip update gate |
| `VIBECAST_AGENT_EXTRA_ARGS` | — | opaque extra CLI args |
| `VIBECAST_RESUME_SESSION_ID` | (same) | now documented as **opaque agent session id** |

Unchanged and already neutral: `SESSION_ID`, `BROADCAST_ID`, `VIBECAST_INITIAL_PROMPT_FILE`,
`VIBECAST_APPEND_SYSTEM_PROMPT(_FILE)`, `AGENTICS_*`, `STAGE_GIT_*`, OTEL endpoints.

## 6. Server-contract migration (later phase, coordinated with www-site)

1. `claudeSessionId` → `agentSessionId`: vibecast **dual-writes both fields** on
   `session_start` metadata; server accepts either, stores both, returns both on
   session-event start. Old stored JSON keeps reading via alias. Remove dual-write only
   after www-site + pks-cli releases.
2. `session_start` gains `agent: {kind, version}` (additive, ignored by old servers).
3. Platform plumbing for agent selection (www-site + pks-cli, mirrors modelTier):
   `operatorConfig.agent` (station) → line settings default → `AgentDefinition` →
   Runner env `VIBECAST_AGENT` → adapter selection. Optionally dispatch with
   `needs:['agent-runtime:<kind>']` so only Runners with that agent installed claim the job.
4. Runner script: gate `CLAUDE_CODE_*` env, `.claude/settings.local.json`, workspace
   `CLAUDE.md`, `.claude/agents` injection, and the claude credentials volume behind
   `agent==claude`; add per-agent credential volumes (`~/.codex`, `~/.pi/agent`) —
   generalizing ADR 0004's `claudeCredentialsScope` → `agentCredentialsScope`.
5. Cost pipeline: codex/pi emit no `claude_code.*` OTEL logs → `costSummary` degrades to
   `source:'none'` (settle unaffected). Parity later = derive cost from metadata `usage`.

## 7. What is explicitly out of scope (v1)

- ACP as the adapter interface (headless agents have no TUI to broadcast; revisit when the
  remote-transport RFD lands — the internal vocabulary is already aligned).
- Transcript upload (`transcriptLines`) for codex/pi — the viewer's metadata-derived
  conversation is the render path; a `transcriptFormat` discriminator is the future door.
- codex `app-server`, pi `--mode rpc` — better programmatic surfaces, but they replace the
  TUI; the broadcast product requires the real TUI on stream.
- pi subagents / codex multi-agent normalization beyond pass-through `Extra` fields.

## 8. Refactor order (small, verifiable steps — see conformance-suite.md for the loop)

1. Introduce `internal/agent` types + registry; **mechanical extraction** of the Claude
   adapter (launch/version/resume/gates tables move; behavior byte-identical — golden
   tests on generated commands).
2. Extract ingestion: `hooks.go` parses via `adapter.ParseHookEnvelope` (claude parser =
   current code); the emission layer stays unchanged.
3. Conformance harness + Claude green (baseline).
4. Codex adapter on `feat/multi-agent-codex` (hooks.json wiring, discover-identity,
   resume, gates: hooks-review dialog; guard deny).
5. pi adapter on `feat/multi-agent-pi` (vibecast.ts extension, tools registration,
   preassign/discover by version; mock-provider conformance mode).
6. Renames + platform plumbing (www-site, pks-cli) as a coordinated change.
