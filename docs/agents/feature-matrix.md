# Agent feature matrix

Concrete, empirically-verified mechanisms per integration concern. Versions:
**Claude Code 2.1.206**, **Codex CLI 0.142.5** (npm `@openai/codex`; latest 0.144.1),
**pi 0.73.1** (npm; deprecated scope) / **0.80.6** (`@earendil-works/pi-coding-agent`, needs Node ≥22.19; `legacy-node20` tag = 0.74.2).
Verified 2026-07-10; full evidence in [research/](research/).

Support legend: ✅ full · 🟡 partial/with-caveat · ❌ none · (cap) = capability-gated in the conformance suite.

## 1. Launch

| Concern | Claude Code | Codex | pi |
|---|---|---|---|
| Interactive launch w/ first prompt | positional arg → interactive first message ✅ | positional arg auto-submits in TUI ✅ | positional arg auto-submits ✅ |
| Append system prompt | `--append-system-prompt "$(cat f)"` ✅ | `-c developer_instructions="…"` (verified: appends as developer message, does **not** replace base) 🟡 + AGENTS.md | `--append-system-prompt <text\|file>` (repeatable) ✅ |
| Working dir | `cd <dir> &&` prefix (today) | `-C/--cd <dir>`, `--add-dir` | cwd of process (sessions keyed by cwd) |
| Event wiring install | `--plugin-dir <claude-plugin>` (hooks.json + .mcp.json) | write `hooks.json` ($CODEX_HOME or `<repo>/.codex/`), launch with `--dangerously-bypass-hook-trust` (or pre-answer trust dialog) | `-e /path/vibecast.ts` extension flag |
| Permission bypass | `--dangerously-skip-permissions` (+ auto-answer of its confirm dialog) | `--dangerously-bypass-approvals-and-sandbox`, or `approval_policy`/`sandbox_mode` configs (no `--full-auto` in 0.142.5) | none needed — pi has **no** permission system (YOLO by design) |
| Model / effort | `--model` (tier table haiku\|sonnet\|opus), `--effort low..max` | `-m <model>`, `-c model_reasoning_effort=…` | `--model <pattern>`, `--thinking off..xhigh` |
| Extra config | `--dangerously-load-development-channels` | `-c key=value` dotted TOML overrides; `-p` profiles; `CODEX_HOME` | `PI_CODING_AGENT_DIR`; `--session-dir`; `--no-*` discovery toggles |

## 2. TUI in tmux

| Concern | Claude Code | Codex | pi |
|---|---|---|---|
| Runs in tmux | ✅ | ✅ (ratatui; `--no-alt-screen` inline mode available) | ✅ (pi-tui differential rendering) |
| Prompt injection | `send-keys -l` + Enter ✅ | text, ~1s, Enter ✅ (verified) | text + Enter ✅ (Shift+Enter newline needs tmux extended-keys, cosmetic warning otherwise) |
| First-run dialogs | trust ("Quick safety check"), bypass-permissions confirm, theme picker, login method, tour ("Learn the moves"), session-too-large | hooks-review gate ("Hooks need review", answer "2"), command approval menus; **no folder-trust dialog observed** in 0.142.5 (pre-trust via `-c 'projects."<dir>".trust_level="trusted"'`) | none on 0.73.1; ≥0.79 project-trust prompt when `.pi/` resources exist — bypass `--approve` / `defaultProjectTrust:"always"` / pre-seed `~/.pi/agent/trust.json` |
| Startup determinism | `claude update` gate (existing) | bubblewrap banner in devcontainers (cosmetic) | set `PI_SKIP_VERSION_CHECK=1` (or `PI_OFFLINE=1`; note OFFLINE also skips fd/ripgrep helper downloads) |

## 3. Lifecycle events (session start/end, turn complete)

| Signal | Claude Code | Codex | pi |
|---|---|---|---|
| Session start | `SessionStart` hook (source: startup\|resume\|clear\|compact) ✅ | `SessionStart` hook (has `source`; fires on start AND resume) ✅ | `session_start` extension event (reason: startup\|resume\|fork\|…, carries sessionId + sessionFile) ✅ |
| Turn complete | `Stop` hook ✅ | `Stop` hook (last_assistant_message) ✅ + `notify` config (`agent-turn-complete`, argv JSON — machine config only) | `agent_end` event (per user prompt) ✅; `agent_settled` ≥0.80.4 |
| Session end | process/pane exit (today) | **no SessionEnd hook** — synthesize from pane exit 🟡 | `session_shutdown` event (fires on SIGTERM/SIGHUP/quit) ✅ + pane exit backstop |

## 4. Tool-call events

| Concern | Claude Code | Codex | pi |
|---|---|---|---|
| Pre-tool | `PreToolUse` hook ✅ | `PreToolUse` hook ✅ (verified TUI + exec) | `tool_execution_start` + `tool_call` events ✅ |
| Post-tool | `PostToolUse` hook (tool_response) ✅ | `PostToolUse` hook (tool_response string) ✅ | `tool_result` + `tool_execution_end` (isError) ✅ |
| Payload fields | session_id, tool_name, tool_input, tool_use_id, transcript_path | session_id, turn_id, transcript_path, model, permission_mode, tool_name ("Bash", "apply_patch", MCP names), tool_input, tool_use_id ("call_*") — near-identical to Claude | toolCallId, toolName, args / result content, isError |
| Full-fidelity stream (fallback) | transcript JSONL `~/.claude/projects/<enc-cwd>/<id>.jsonl` | rollout JSONL `~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl` (function_call/function_call_output, reasoning, token_count) | session JSONL `~/.pi/agent/sessions/--<enc-cwd>--/<ts>_<uuid>.jsonl` (post-hoc only) |
| ⚠ Do not rely on | — | `codex exec --json` item events: 0.142.5 emitted **no** command_execution items despite docs (turn boundaries only) | `--mode json` replaces the TUI (not usable while broadcasting); one observed unflushed `agent_end` with session persistence on |

## 5. Prompt capture

| | Claude Code | Codex | pi |
|---|---|---|---|
| Mechanism | `UserPromptSubmit` hook (`prompt`) ✅ | `UserPromptSubmit` hook (`prompt`) ✅ | `before_agent_start` event (`prompt`, fires for argv + typed + RPC prompts) ✅; `input` event for raw pre-expansion text |

## 6. Session identity + resume

| Concern | Claude Code | Codex | pi |
|---|---|---|---|
| ID strategy | **preassign**: `--session-id <uuid>` (UUIDv4 validated — invalid id kills claude) | **discover**: `session_id` (UUIDv7) from any hook payload / notify thread-id / rollout filename | ≥0.76 **preassign**: `--session-id <id>` ("create if missing"); ≤0.74 **discover** via `session_start` event |
| Resume | `--resume <uuid>` / `--continue` | `codex resume <uuid>` (TUI), `codex resume --last`, `codex exec resume <id>`; `codex fork` | `--session <path\|partial-uuid>` (verified), `-c` continue, `--fork`; ≥0.76 `--session-id` relaunch; **same cwd required** (sessions keyed by dir) |
| SessionStart on resume | fires (source=resume) ✅ | fires ✅ | fires (reason=resume) ✅ |

## 7. Permissions / guard (deny dangerous tool calls)

| Concern | Claude Code | Codex | pi |
|---|---|---|---|
| Deny mechanism | PreToolUse stdout `{hookSpecificOutput:{permissionDecision:"deny",…}}` / exit 2 ✅ | **same shape** — verified: TUI showed "PreToolUse hook (blocked)", command never ran ✅ | extension `tool_call` handler returns `{block:true, reason}` → tool never runs, reason lands as isError toolResult ✅ (input also **mutable**) |
| Native approval prompts | PermissionRequest hook + native dialog ('1' Allow / '3' Deny) | approval menus (y/p/esc) + `PermissionRequest` hook (can auto-answer per docs — untested) + `.rules` execpolicy files | none (no permission system); `ctx.ui.confirm` available for interactive approval |
| ⚠ Lesson | — | model routed around a naive `rm` substring deny using `find -delete` → guards must be **semantic**; also cover `apply_patch` and MCP tool names | same lesson applies |

## 8. Auth / onboarding

| Concern | Claude Code | Codex | pi |
|---|---|---|---|
| Login flows | claude.ai OAuth (device-code paste flow detected via pane text + `oauth/authorize` URL) | `codex login` (browser, port 1455), `--device-auth`, `--with-api-key` (stdin); status: `codex login status` exit code | `/login` TUI → subscription OAuth (Anthropic Claude Pro/Max, ChatGPT, Copilot; PKCE + paste-redirect-URL fallback, headless-workable) or API key; env keys (ANTHROPIC_API_KEY, OPENAI_API_KEY, GEMINI_API_KEY…); `~/.pi/agent/auth.json` (key can be literal \| ENV_NAME \| `!cmd`) |
| Logged-in probe | pane-scrape (today) | `codex login status` exit 0; `~/.codex/auth.json` presence + auth_mode ✅ | TUI banner "No models available. Use /login"; headless exit 1 "No API key found" ✅ |
| URL classifier | `claude.ai` / `auth.anthropic` → `claude-login` | `auth.openai.com` / `chatgpt.com` → `codex-login` | `claude.ai/oauth` (subscription path) → `pi-login` |
| ⚠ Billing note | — | — | subscription use through third-party harnesses bills as Anthropic **extra usage** (per-token), not plan quota |
| Proxy/gateway | ANTHROPIC_BASE_URL env | `model_providers` config (e.g. existing pks-foundry provider) | `pi.registerProvider('anthropic', {baseUrl})` from the vibecast extension, or models.json |

## 9. Version management

| | Claude Code | Codex | pi |
|---|---|---|---|
| Update | `claude update` | `codex update` (npm-aware) or `npm i -g @openai/codex@<v>` | `pi update` (pi-only default ≥0.79.7) or npm; **scope moved** to `@earendil-works/pi-coding-agent` (old scope frozen at 0.73.1) |
| Pin | `claude install <v>` via `CLAUDE_VERSION` | `npm i -g @openai/codex@<v>` | exact npm version; Node ≥22.19 for ≥0.75 (`legacy-node20` = 0.74.2) |

## 10. Headless mode (for future non-TUI stations; NOT the broadcast path)

| | Claude Code | Codex | pi |
|---|---|---|---|
| Mode | `-p` print / stream-json | `codex exec [--json]` (thread/turn events; exit 0 verified) + `codex app-server` (JSON-RPC, approval callbacks) | `-p` print; `--mode json` (JSONL events); `--mode rpc` (bidirectional: prompt/steer/abort/get_state/get_session_stats) |

## 11. MCP / vibecast tools (stop_broadcast, chat_reply, share_image…)

| | Claude Code | Codex | pi |
|---|---|---|---|
| MCP client | `.mcp.json` (project) ✅ | `[mcp_servers.*]` in config.toml / `codex mcp add` ✅ | ❌ **no MCP by design** → register vibecast tools natively via `pi.registerTool` in the vibecast extension (same control-socket calls) |

## 12. Context / compaction / token usage

| | Claude Code | Codex | pi |
|---|---|---|---|
| Compaction events | PreCompact/PostCompact hooks ✅ | PreCompact/PostCompact hooks (docs; unverified) 🟡 | `session_before_compact` (cancelable) + `session_compact` events; `compaction_start/end` in json/rpc ✅ |
| Token usage | transcript `message.usage` (Anthropic keys) | rollout `token_count` events (incl. model_context_window); exec `turn.completed.usage` | assistant message `usage {input, output, cacheRead, cacheWrite, cost}`; `ctx.getContextUsage()` |

## Capability declaration summary (what each adapter will declare)

| Capability | claude | codex | pi |
|---|---|---|---|
| `events.lifecycle` | ✅ | ✅ | ✅ |
| `events.tool_calls` | ✅ | ✅ | ✅ |
| `events.prompt` | ✅ | ✅ | ✅ |
| `events.session_end` | 🟡 (pane exit) | 🟡 (pane exit) | ✅ (session_shutdown) |
| `session.preassign` | ✅ | ❌ | ≥0.76 ✅ / ≤0.74 ❌ |
| `session.resume` | ✅ | ✅ | ✅ (same cwd) |
| `guard.deny` | ✅ | ✅ | ✅ |
| `guard.mutate_input` | ❌ | ❌ | ✅ |
| `approvals.native_prompt` | ✅ | ✅ | ❌ |
| `system_prompt.append` | ✅ | ✅ (developer_instructions) | ✅ |
| `plan_events` | ✅ (ExitPlanMode) | ❌ | ❌ |
| `subagent_events` | ✅ | ✅ (hooks exist; payload unverified) | ❌ (no subagents) |
| `compaction_events` | ✅ | 🟡 | ✅ |
| `vibecast_tools` (stop_broadcast et al.) | ✅ MCP | ✅ MCP | ✅ extension-registered |
| `transcript.upload` | ✅ (claude-jsonl) | ❌ (v1; format differs) | ❌ (v1) |
| `usage.tokens` | ✅ | ✅ | ✅ |
