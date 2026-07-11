# AgentAdapter specification

The Go interface that makes vibecast's coding agent pluggable. Lives in `internal/agent`.
Selected at startup by `VIBECAST_AGENT` (`claude` | `codex` | `pi`, default `claude`).
Unknown value → fail fast with a clear error before any tmux session is created.

Design constraints (from [research](research/), see [README](README.md) decision log):

- The **platform contract is frozen** in v1: adapters must produce today's metadata
  subtypes and session-event calls, with today's field names (`claudeSessionId` included —
  the `agentSessionId` dual-write happens in the later migration phase, §6).
- The Claude adapter must be a **pure extraction** — same launch command strings, same
  hook behavior and exit codes, so `VIBECAST_AGENT=claude` (and unset) is a no-op refactor.
  Golden tests pin the command strings and hook responses.
- Everything tmux/ttyd/relay/control-socket stays **outside** the interface.

> Reviewed 2026-07-10 against the real code by an adversarial implementability pass;
> the interface below incorporates its findings (multi-event ingestion, transcript seam,
> per-event hook responses, tool taxonomy members, wiring lifecycle).

## 1. The interface

```go
package agent

// Kind identifies an agent implementation.
type Kind string // "claude" | "codex" | "pi"

type Adapter interface {
    // --- identity ---
    Kind() Kind
    DisplayName() string          // "Claude Code", "Codex", "pi" — used in UI copy
    BinaryName() string           // resolved by the caller with exec.LookPath
    Version(ctx context.Context) (string, error) // cached once per process (gates use it)

    // --- capabilities (declared, never probed; see conformance-suite.md) ---
    Capabilities() Capabilities

    // --- launch (stream.go seam) ---
    // binPath is the exec.LookPath-resolved absolute path (today's commands embed it).
    // The caller prefixes `cd <workdir> && ` (both paths) and appends the
    // exit-code echo wrapper ONLY on the restart path — mirroring today's asymmetry
    // (SpawnPane runs the command bare; DoRestartClaude adds the rc-echo).
    BuildCommand(binPath string, spec LaunchSpec) (string, error)
    BuildResumeCommand(binPath string, spec LaunchSpec, agentSessionID string) (string, error)

    // --- event wiring ---
    // InstallEventWiring is called ONCE per stream (idempotent), inside
    // StartStream/ResumeStream after the tmux session exists and before the first
    // SpawnPane. Returned env is applied with `tmux set-environment` on the session
    // (panes inherit session env, not vibecast's process env). Restart/reattach paths
    // reuse the existing installation; cleanup is registered with process-exit cleanup
    // (root.go), NOT StopStream, so restarts never race it.
    // claude → no-op (plugin dir flag carries the wiring); codex → writes hooks.json
    // into the vibecast-managed agent home (see §3); pi → no-op (extension passed
    // via -e in BuildCommand).
    InstallEventWiring(spec LaunchSpec) (env map[string]string, cleanup func(), err error)

    // --- ingestion (hooks.go seam) ---
    // ParseHookEnvelope converts one `vibecast hook <event>` invocation into 1..N
    // normalized events plus transcript-derived attachments. Claude hook handlers
    // demonstrably emit multiple platform events per invocation (streamed assistant
    // text blocks from transcript increments, plan+tool_use for ExitPlanMode, final
    // Stop text/usage) — the transcript cursor state lives behind Transcripts().
    ParseHookEnvelope(event string, stdin []byte) (*EnvelopeResult, error)
    // SerializeHookResponse converts a normalized intent into the agent-native hook
    // stdout + exit code. The wire format differs per (event × origin) for the same
    // intent kind (see §4), hence both discriminators.
    SerializeHookResponse(event string, intent HookIntent) (stdout []byte, exitCode int)

    // --- transcripts (per-agent format + discovery; capability TranscriptUpload) ---
    Transcripts() TranscriptReader

    // --- tool taxonomy (lets shared guard/emission reach per-agent tables) ---
    // MapToolName maps a native tool name to the platform rendering vocabulary
    // (codex "apply_patch" → "Edit"-class; pi "bash" → "Bash"). Unknown → return input.
    MapToolName(native string) string
    // WritePaths returns filesystem paths a tool call would write (empty = not a
    // write tool). Feeds the shared job-mode path-containment guard.
    WritePaths(toolName string, input json.RawMessage) []string
    // SelfApprovingTools: tools whose invocation IS the approval (claude
    // AskUserQuestion/AskFollowupQuestion) — skipped by permission-request handling.
    SelfApprovingTools() []string
    // WorktreeExclusions: agent config dirs to ignore in dirty-worktree checks
    // (stop_broadcast refuse + AGENTICS_AUTO_GIT stop-block) — replaces the
    // hardcoded ".claude/" (claude: [".claude/"]; codex: [".codex/"]; pi: [".pi/"]).
    WorktreeExclusions() []string

    // --- session identity + resume ---
    SessionIdentity() SessionIdentity // Preassign | Discover
    ListSessions(workspace string) ([]AgentSessionInfo, error)
    // ResolveSessionPath maps a session id to its transcript file (used by `vibecast
    // sync` and the resume picker). ErrNotSupported when TranscriptUpload is false.
    ResolveSessionPath(workspace, sessionID string) (string, error)

    // --- version management (ensureClaudeUpToDate seam) ---
    // EnsureVersion honours pin ("" = update to latest unless auto-update disabled).
    // MUST stay fail-open with a timeout, as today.
    EnsureVersion(ctx context.Context, pin string) error

    // --- TUI automation (broadcast.go seam; see struct defs below) ---
    ScreenGates() []ScreenGate
    AnswerHandlers() []AnswerHandler
    ClassifyURL(url string) string // "" or provider context, e.g. "claude-login"

    // --- vibecast tools (stop_broadcast / chat_reply / share_*) ---
    // claude → NO-OP: the shipped claude-plugin/.mcp.json already registers the MCP
    // server via --plugin-dir (the runtime .mcp.json writer is dead code today; do
    // not resurrect it — writing .mcp.json into the CWD would double-register and
    // dirty the user's repo). codex → MCP entry in the vibecast-managed agent home
    // config.toml. pi → no-op (the vibecast extension registers native tools).
    // serverEnv is the session-event start response env (plugin-MCP servers need it —
    // this is the InjectPluginMCP seam, claude-only, kept inside this member).
    RegisterVibecastTools(spec LaunchSpec, serverEnv map[string]string) (cleanup func(), err error)

    // --- telemetry ---
    // Pure passthrough filter: returns the subset of ambient env vars this agent's
    // telemetry needs forwarded into the tmux session (claude: CLAUDE_CODE_ENABLE_
    // TELEMETRY etc. — ONLY when already set; they are Runner-controlled opt-ins).
    // Never fabricates values.
    TelemetryEnv(ambient map[string]string) map[string]string

    // --- liveness (Stop-hook "N local agents" sniff replacement) ---
    // BusySignal inspects a captured pane and reports whether the agent still has
    // background work (subagents) running. (false, ErrNotSupported) when the agent
    // has no such concept. Both call sites capture the pane themselves.
    BusySignal(paneText string) (busy bool, err error)
}

type EnvelopeResult struct {
    Events               []NormalizedAgentEvent // 1..N, in emission order
    TranscriptLines      []json.RawMessage      // raw agent-native lines (claude only in v1)
    AgentTranscriptLines []json.RawMessage      // subagent transcript increment (claude)
    Usage                *TokenUsage
    SessionSummary       string                 // first user prompt (session_start)
}

type TranscriptReader interface {
    // Increment reads new lines since the per-(stream,transcript) cursor and
    // advances it — cursor files under ~/.vibecast/transcripts/<streamId>/ as today.
    Increment(streamID, transcriptPath string) ([]json.RawMessage, error)
    FirstUserPrompt(transcriptPath string) (string, error)
    ExtractUsage(lines []json.RawMessage) *TokenUsage
    ExtractToolUseIDs(lines []json.RawMessage) []string
    AssistantTextBlocks(lines []json.RawMessage) []string // skips sidechains (claude)
}

type LaunchSpec struct {
    Workdir            string
    AgentSessionID     string // pre-assigned id when SessionIdentity == Preassign
    Model, ModelTier   string // adapter maps tier→model per its own table
    Effort             string // adapter maps to --effort / model_reasoning_effort / --thinking
    SystemPromptFile   string // station prompt file (VIBECAST_APPEND_SYSTEM_PROMPT_FILE)
    SystemPromptInline string // inline variant (VIBECAST_APPEND_SYSTEM_PROMPT) — both live today
    InitialPromptFile  string
    JobMode            bool   // AGENTICS_JOB_MODE=1 — unattended job. Distinct from PermissionMode
                              // (below): this gates the COMPLETION contract, not the permission
                              // bypass. codex injects its stop_broadcast developer_instructions
                              // mandate only when set (see research/codex.md §mcp); claude relies
                              // on the Stop-hook enforcement instead, so its adapter ignores it.
    PermissionMode     PermissionMode
    ExtraArgs          []string // opaque passthrough (VIBECAST_AGENT_EXTRA_ARGS; claude's
                                // --dangerously-load-development-channels rides here via
                                // the VIBECAST_CLAUDE_CHANNEL fallback)
    PluginDirs         []string // claude-only concept; others ignore
    StreamID           string   // vibecast session id, for event wiring env
}

// PermissionMode is forward-looking for codex/pi. NOTE: today's claude behavior is
// BypassAll UNCONDITIONALLY — `--dangerously-skip-permissions` is hardcoded into every
// launch and resume command, interactive AND job mode. The v1 claude adapter keeps
// that (byte-identical); do NOT derive it from AGENTICS_JOB_MODE.
// codex BypassAll = `approval_policy=never` + `sandbox_mode=workspace-write` (+ writable
// roots). `--dangerously-bypass-approvals-and-sandbox` is FORBIDDEN — §4's guard design
// relies on the sandbox as the apply_patch backstop.
// pi = no permission system; BypassAll is a no-op.
type PermissionMode int
const (
    BypassAll PermissionMode = iota
    Default
)

type SessionIdentity int
const (
    Preassign SessionIdentity = iota // vibecast mints the id and passes it on launch
    Discover                         // id arrives in the first session_start event
)

type AgentSessionInfo struct {
    ID           string
    Path         string // transcript/session file (feeds ResolveSessionPath/sync)
    FirstPrompt  string
    MessageCount int
    ModifiedAt   time.Time
}

// ScreenGate: first-run/auth dialog detection + reaction. Gates are code, not flat
// data — real gates need conditional re-capture flows (the tour gate sends '2',
// re-captures, and only conditionally sends Enter), metadata posting (theme/login/
// session-too-large gates post alp_pane votes instead of injecting keys), env
// conditions (trust gate checks VIBECAST_ALLOWED_DIRECTORIES), and Runner-visible
// stdout lines (OAuth gate). One-shot dedup is handled by SHARED machinery keyed on
// gate ID — today's trust dialog is answered from two racing goroutines sharing an
// atomic; that dedup moves into the gate runner, not each gate.
type ScreenGate struct {
    ID          string
    VersionGlob string                  // matched against the cached adapter Version()
    Match       func(plain string) bool // ANSI-stripped pane text
    Act         func(g GateServices)    // locked send-keys, re-capture, metadata post, env
}

// AnswerHandler keeps today's shape (broadcast.go answerHandler), gaining agent scope:
// selected by (questionType, versionGlob) within the active adapter.
type AnswerHandler struct {
    QuestionType string // permission | alp_pane | onboarding_external | tool
    VersionGlob  string
    Inject       func(h HandlerServices, q PendingQuestion) error
}
```

`Capabilities` is a struct of booleans matching the
[feature-matrix capability table](feature-matrix.md#capability-declaration-summary):
`Lifecycle`, `ToolCalls`, `PromptCapture`, `SessionEndEvent`, `SessionPreassign`,
`SessionResume`, `GuardDeny`, `GuardMutate`, `NativeApprovals`, `SystemPromptAppend`,
`PlanEvents`, `SubagentEvents`, `CompactionEvents`, `VibecastTools`, `TranscriptUpload`,
`TokenUsage`. Declare only what has been verified AND has committed fixtures
(see onboarding-playbook Phase I).

## 2. Normalized event model

Internal only — the platform keeps receiving today's metadata subtypes **with today's
field names** (`claudeSessionId` until the §6 migration). Vocabulary is ACP-aligned
(toolCallId, stopReason) so a future ACP backend is a field mapping.

```go
type NormalizedAgentEvent struct {
    Event          string // session_start|prompt|pre_tool|post_tool|assistant_text|
                          // turn_end|session_end|permission_request|plan|
                          // subagent_start|subagent_stop|pre_compact|post_compact|
                          // task_created|task_completed
    AgentKind      Kind
    AgentSessionID string // the agent's OWN session id (emitted as claudeSessionId in v1)
    Workspace      string // cwd — used for stream-session lookup (unchanged)
    TranscriptPath string // agent-native transcript/rollout/session file, if any

    Prompt      string            // prompt
    Source      string            // session_start: startup|resume|clear|compact
    ToolName    string            // pre_tool/post_tool/permission_request (native name)
    ToolInput   json.RawMessage
    ToolCallID  string
    ToolOutput  json.RawMessage   // post_tool
    IsError     bool              // post_tool
    Text        string            // assistant_text block, turn_end final text, plan
                                  // markdown, compact summary
    Usage       *TokenUsage       // normalized to Anthropic key names on emission
    Extra       map[string]any    // agent-specific leftovers (subagent fields, trigger, …)
}

type HookIntent struct {
    Kind   IntentKind   // Allow | Deny | InjectContext | BlockStop
    Origin IntentOrigin // Guard | WriteGuard | PermissionVote | AutoGit | StopEnforce
    Reason string
    Text   string       // InjectContext payload
}
```

**Emission** (normalized events → platform metadata subtypes) is shared code; the
transcript-derived enrichment (transcriptLines, usage, streamed assistant text,
session summaries, subagent toolUseIds) is **adapter-provided** via `EnvelopeResult` /
`TranscriptReader` — that is where the current Claude cursor logic moves. Mapping:
`pre_tool`→`tool_use`, `post_tool`→`tool_use_end`, `assistant_text`→streamed
`assistant_response`, `turn_end`→final `assistant_response`, `session_start`→
`session_start` (persists AgentSessionID), etc. Native tool names pass through
`MapToolName` for the platform's rendering expectations; unmapped names pass verbatim
(the viewer renders unknown tools generically).

**Timestamps**: today's units are kept exactly as-is in v1 — including `url_detected`'s
milliseconds inconsistency (it is an observable contract detail; the unit unification
happens in the §6 migration phase after verifying viewer handling).

## 3. Ingestion topologies per agent

All three funnel into the same `vibecast hook <event>` entrypoint (session lookup by
workspace, metadata POST transport, question-vote polling stay shared; sensitive-data
masking is server-side, not vibecast's job).

**Adapter selection inside hook subprocesses**: `vibecast hook` runs as a separate
process spawned from inside the tmux pane and inherits the tmux *session* env — so
(a) `VIBECAST_AGENT` is added to the session-env propagation list in
StartStream/ResumeStream, and (b) the agent kind is **persisted in the ~/.vibecast
SessionFile** (`agentKind` field) so `hookReadStdinAndFindSession`'s workspace lookup
yields the adapter authoritatively even for envelopes that look alike (codex's are
near-identical to claude's).

- **claude** — unchanged: `claude-plugin/hooks/hooks.json` via `--plugin-dir`.
  `ParseHookEnvelope`/`Transcripts()` are the existing hooks.go logic, extracted.
  `InstallEventWiring` and `RegisterVibecastTools` are no-ops (the plugin dir carries
  both wirings); plugin-MCP injection for extra `--plugin` servers stays inside
  `RegisterVibecastTools(spec, serverEnv)`.
- **codex** — `InstallEventWiring` writes `hooks.json` (same `vibecast hook <event>`
  commands) into a **vibecast-managed `$CODEX_HOME`** (e.g. `~/.vibecast/agent-homes/
  codex/<streamId>/`), NOT `<workdir>/.codex/`. Two hard reasons: the project layer
  only loads if the project is trusted (would need
  `-c 'projects."<dir>".trust_level="trusted"'`), and an untracked `<workdir>/.codex/`
  makes every job un-endable (stop_broadcast's dirty-worktree refuse + AUTO_GIT
  stop-block see it — that is also why `WorktreeExclusions()` exists). ⚠ A relocated
  `CODEX_HOME` relocates **auth.json too**: the adapter must copy/symlink the real
  `~/.codex/auth.json` (and config.toml model_providers) into the managed home.
  Launch includes `--dangerously-bypass-hook-trust` (hook-trust only; unrelated to
  project trust). Envelope fields are near-identical to Claude's; the parser mainly
  relabels (`turn_id`, `call_*` ids) and handles `apply_patch`. Session end is
  **synthesized** — no SessionEnd hook exists; pane-exit detection (already present
  via `remain-on-exit` + `pane_dead` probe) emits the normalized `session_end`.
- **pi** — vibecast ships `pi/vibecast.ts` (analogous to `claude-plugin/`). The
  extension subscribes to pi events and **execs `${VIBECAST_BIN} hook <event>`** with a
  synthesized envelope (JSON on stdin, `agent:"pi"` field). For `tool_call` it runs the
  guard synchronously and maps a Deny response to `{block: true, reason}`. It also
  registers `stop_broadcast` / `chat_reply` / `share_image` as native pi tools that call
  the existing control socket — replacing MCP, which pi does not support.

## 4. Guard

`internal/hooks` keeps ONE policy engine (`dangerousProcessKill`, job-mode path
containment via `WritePaths()`, future rules). Adapters only translate:

1. the native pre-tool payload → normalized (`ParseHookEnvelope`),
2. the policy verdict → native deny (`SerializeHookResponse(event, intent)`).

The wire format genuinely differs per (event × origin) — this is why `HookIntent`
carries `Origin` and the serializer gets the event name. Claude today (golden-tested
per pair): bash-guard deny = dual-schema `{decision:block}` **and**
`{hookSpecificOutput:{permissionDecision:deny}}` + exit 2 (cross-version compat);
job-mode write-guard deny = `{decision:block}` + exit 1; permission-vote deny =
`{decision:deny}` + exit 1; Stop blocks = `{decision:block}` with exit 2
(busy/stop-enforce) or exit 1 (auto-git); SessionStart/SubagentStart inject =
`{additionalContext}` + exit 0.

For observability, every deny verdict is also **recorded locally** as a JSON line under
`$VIBECAST_HOME/guard-denials/<streamId>.jsonl` (new in v1, additive, no platform
change) — the conformance suite's C08 asserts on this record since claude/codex denies
are otherwise invisible on the wire (stdout + exit code only, and a denied PreToolUse
produces no PostToolUse).

Rules stay **semantic**: the codex probe showed a denied `rm` being replayed as
`find -delete`. Deny reasons must state the *intent* blocked ("process-kill of the
operator", "write outside job worktree") so models don't treat it as a syntax puzzle.
Sandbox/approval configs remain enabled as defense in depth wherever the agent has them
(codex: `sandbox_mode=workspace-write` is mandatory, see PermissionMode note).

## 5. Env contract (Runner ⇄ vibecast)

New generic names; old `*CLAUDE*` names remain as claude-scoped fallbacks (read in this
order, first hit wins):

| New | Old (kept) | Meaning |
|---|---|---|
| `VIBECAST_AGENT` | — | `claude` (default) \| `codex` \| `pi`; also propagated into the tmux session env |
| `VIBECAST_MODEL` | `VIBECAST_CLAUDE_MODEL` | exact model id (adapter-validated) |
| `VIBECAST_MODEL_TIER` | `VIBECAST_CLAUDE_MODEL_TIER` | tier alias; per-adapter table |
| `VIBECAST_EFFORT` | `VIBECAST_CLAUDE_EFFORT` | reasoning effort; per-adapter mapping |
| `VIBECAST_AGENT_VERSION` | `CLAUDE_VERSION` | pin agent version for the whole line |
| `VIBECAST_AGENT_AUTO_UPDATE_DISABLED` | `CLAUDE_AUTO_UPDATE_DISABLED` | skip update gate |
| `VIBECAST_AGENT_EXTRA_ARGS` | `VIBECAST_CLAUDE_CHANNEL` (claude: maps to `--dangerously-load-development-channels`) | opaque extra CLI args |
| `VIBECAST_RESUME_SESSION_ID` | (same) | now documented as **opaque agent session id** |

Unchanged and already neutral: `SESSION_ID`, `BROADCAST_ID`, `VIBECAST_INITIAL_PROMPT_FILE`,
`VIBECAST_APPEND_SYSTEM_PROMPT` (inline) / `VIBECAST_APPEND_SYSTEM_PROMPT_FILE`,
`AGENTICS_*`, `STAGE_GIT_*`, OTEL endpoints.

## 6. Server-contract migration (later phase, coordinated with www-site)

1. `claudeSessionId` → `agentSessionId`: vibecast **dual-writes both fields** on
   `session_start` metadata; server accepts either, stores both, returns both on
   session-event start. Old stored JSON keeps reading via alias. Remove dual-write only
   after www-site + pks-cli releases. Until then, everything (including the conformance
   mockserver) speaks `claudeSessionId`.
2. `session_start` gains `agent: {kind, version}` (additive, ignored by old servers).
3. Timestamp unit unification (`url_detected` ms → s) after verifying viewer handling.
4. Platform plumbing for agent selection (www-site + pks-cli, mirrors modelTier):
   `operatorConfig.agent` (station) → line settings default → `AgentDefinition` →
   Runner env `VIBECAST_AGENT` → adapter selection. Optionally dispatch with
   `needs:['agent-runtime:<kind>']` so only Runners with that agent installed claim the job.
5. Runner script: gate `CLAUDE_CODE_*` env, `.claude/settings.local.json`, workspace
   `CLAUDE.md`, `.claude/agents` injection, and the claude credentials volume behind
   `agent==claude`; add per-agent credential volumes (`~/.codex`, `~/.pi/agent`) —
   generalizing ADR 0004's `claudeCredentialsScope` → `agentCredentialsScope`.
6. Cost pipeline: codex/pi emit no `claude_code.*` OTEL logs → `costSummary` degrades to
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
   tests on generated commands and on hook (event × intent) serializations). Persist
   `agentKind` in the SessionFile; add `VIBECAST_AGENT` to session-env propagation.
2. Extract ingestion: `hooks.go` handlers become `ParseHookEnvelope` + `Transcripts()`
   on the claude adapter (cursor logic moves as-is); the shared emitter consumes
   `EnvelopeResult`. Golden tests: recorded hook stdin fixtures → exact metadata POST
   sequences.
3. Conformance harness + Claude green (baseline).
4. Codex adapter on `feat/multi-agent-codex` (managed CODEX_HOME wiring + auth
   copy, discover-identity, resume, hooks-review gate, guard deny + sandbox backstop).
5. pi adapter on `feat/multi-agent-pi` (vibecast.ts extension, tools registration,
   preassign/discover by version; mock-provider conformance mode).
6. Renames + platform plumbing (www-site, pks-cli) as a coordinated change (§6).
