# Multi-Agent Operator: running Claude Code, Codex, and pi under vibecast

vibecast is the ALP **Operator**: it owns a tmux session, launches a coding agent inside it,
streams the terminal to viewers, publishes normalized metadata events to the server, and
reports the job conclusion to the Runner. Today all of that is wired to **Claude Code**.

This folder is the design for making the agent pluggable — first targets **OpenAI Codex CLI**
and **pi-coding-agent**, with a written playbook + automated conformance suite so onboarding
agent N+1 is a mostly-mechanical process that does not require re-deriving the design.

## Documents

| Doc | What it is |
|---|---|
| [feature-matrix.md](feature-matrix.md) | Capability matrix: Claude Code vs Codex vs pi, per integration concern, with concrete flags/events/paths |
| [adapter-spec.md](adapter-spec.md) | The `AgentAdapter` Go interface, normalized event model, env contract, migration plan |
| [onboarding-playbook.md](onboarding-playbook.md) | Step-by-step checklist for onboarding a new agent (research worksheet → adapter → conformance green) |
| [conformance-suite.md](conformance-suite.md) | The scenario suite that verifies an agent integration works — the start of vibecast's verification loop |
| [research/](research/) | Raw findings from the 2026-07-10 research pass (vibecast coupling inventory, platform contract, codex, pi, prior art) |

## Architecture: two planes

```
┌─ presentation plane (UNCHANGED, agent-agnostic) ─────────────────┐
│ tmux session ── ttyd ── broadcaster WS ── ws-relay ── viewers    │
│ (bytes; masking; panes; resize; keyboard relay; PIN)             │
└──────────────────────────────────────────────────────────────────┘
┌─ control/metadata plane (THE ABSTRACTION) ───────────────────────┐
│ AgentAdapter                                                      │
│  ├─ launch/resume command construction                            │
│  ├─ event ingestion (hooks / extension) → NormalizedAgentEvent    │
│  ├─ session identity (preassign or discover) + resume             │
│  ├─ guard delivery (deny dangerous tool calls)                    │
│  ├─ screen gates + answer injection (onboarding/auth dialogs)     │
│  ├─ version management, telemetry env, MCP/tool registration      │
│  └─ Capabilities() declaration (drives conformance gating)        │
└──────────────────────────────────────────────────────────────────┘
```

The presentation plane already works for any process in a pty. Everything agent-specific is
concentrated in the control plane, behind one interface, selected by `VIBECAST_AGENT`
(`claude` | `codex` | `pi`, default `claude`).

## Key findings that shaped the design (2026-07-10)

1. **Codex now has a Claude-style hooks system** (feature `hooks` = stable + enabled in
   0.142.5): SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, PermissionRequest,
   Stop, SubagentStart/Stop, PreCompact/PostCompact — with stdin JSON payloads almost
   field-for-field identical to Claude Code's, firing in **both** exec and the interactive
   TUI, and a deny-capable PreToolUse (verified live). vibecast's existing `vibecast hook`
   ingestion model ports nearly 1:1.
2. **pi's extension system is a strict superset of hooks**: in-process TypeScript modules
   receive the full lifecycle (session_start with sessionId, before_agent_start with the
   prompt, tool_execution_*/tool_call with **blocking + input mutation**, agent_end,
   session_shutdown) and can exec/POST freely. A single `vibecast.ts` extension replaces the
   whole Claude plugin. pi also has a **mock provider** path that runs the real agent loop
   with zero API keys — used by the conformance suite for credential-less CI.
3. **The platform contract is already agent-neutral in shape.** The metadata subtypes
   (`prompt`, `tool_use`, `tool_use_end`, `session_start`, `assistant_response`, `plan`,
   `permission_request`, …) carry generic names; Claude leaks only in field naming
   (`claudeSessionId`), raw `transcriptLines` (Claude JSONL), Anthropic-shaped `usage`
   keys, and a few tool-name switches. The viewer has a verified metadata-only conversation
   fallback, so non-Claude agents render correctly without uploading transcripts.
4. **Prior art converged on the same shape** (vibe-kanban executor trait, agentapi's echo-
   agent scenario suite, Zed's ACP): small per-agent adapter, capability declaration (never
   probing), events normalized with stable toolCallIds, approval timeout = denial, and a
   scripted fake agent + recorded fixtures for conformance. ACP is deliberately **not**
   adopted as the interface now (ACP agents run headless — nothing to broadcast) but the
   internal event vocabulary is ACP-aligned so an ACP backend stays a field-mapping away.

## Decision log

| # | Decision | Why |
|---|---|---|
| D1 | Two planes; only the control plane is abstracted | Terminal streaming is bytes; it already works for any agent |
| D2 | One Go interface `AgentAdapter` in `internal/agent`, selected by `VIBECAST_AGENT` | Mirrors the existing "Runner passes values, vibecast owns the CLI mapping" principle |
| D3 | Keep `vibecast hook <event>` as the single ingestion entrypoint; per-agent envelope parsers → internal `NormalizedAgentEvent`; emission keeps today's metadata subtypes | Codex hooks are envelope-compatible; pi's extension synthesizes the envelope; platform contract unchanged |
| D4 | Capabilities are **declared** by the adapter, never probed; conformance gates on them | ACP + vibe-kanban pattern; makes the conformance doc the onboarding checklist |
| D5 | Session identity strategy per adapter: preassign (claude `--session-id`, pi ≥0.76 `--session-id`) or discover (codex SessionStart hook) | Codex cannot preassign; pi <0.76 cannot either |
| D6 | Conclusion reporting stays vibecast-owned: `stop_broadcast` MCP tool (claude, codex) / extension-registered tool (pi has no MCP) | ALP's Runner contract must be identical across agents |
| D7 | Guard is a shared Go policy invoked from each agent's deny mechanism; **semantic, not substring** | Codex model demonstrably bypassed an `rm` substring block with `find -delete` |
| D8 | Screen-gate detection + answer injection promoted to (agentKind × versionGlob) tables | broadcast.go's answerHandler already sketches this; codex/pi have different dialogs |
| D9 | Platform/runner changes are minimal and deferred: `operatorConfig.agent` → `AgentDefinition` → `VIBECAST_AGENT`; `needs:['agent-runtime:<kind>']`; renames with dual-write aliases | Everything event-shaped already generalizes; vibecast-internal work lands first |
| D10 | Non-Claude agents do not upload `transcriptLines` initially; the viewer's metadata fallback renders the conversation | Zero platform change; full-fidelity replay later via a `transcriptFormat` discriminator |
| D11 | Adapters normalize token usage into the Anthropic-shaped `usage` object | One adapter-side mapping beats touching every consumer |
| D12 | TUI-scrape is fallback, never foundation; structured events (hooks/extension) are the metadata source | agentapi/omnara both hit the scraping→structured pivot; we start structured |

## Rollout plan (branches)

- `feat/multi-agent` (this branch): design docs, normalized event layer, adapter interface,
  Claude adapter extraction (byte-identical behavior), conformance harness + Claude green.
- `feat/multi-agent-codex`: codex adapter, driven by the conformance suite. Findings merge back here.
- `feat/multi-agent-pi`: pi adapter (vibecast.ts extension), driven by the conformance suite. Findings merge back here.

## Open items needing Poul's input

1. **pi auth in runners**: Claude Pro/Max OAuth through pi bills as Anthropic *extra usage*
   (per-token, not plan quota) per pi's docs. Alternatives: provider API key, or routing pi
   through pks-agent-gateway via `pi.registerProvider('anthropic', {baseUrl})`. Which?
2. **Node 22 in runner images**: pi ≥0.75 (needed for `--session-id` preassign) requires
   Node ≥22.19; devcontainer currently has Node 20 (caps pi at legacy 0.74.2, discovery-mode
   resume only). Bump acceptable?
3. **Codex hook trust**: launch with `--dangerously-bypass-hook-trust` (two cosmetic warning
   items in the transcript) vs auto-answering the one-time "Hooks need review" dialog via
   send-keys. Spec assumes the flag; flip if the warning items bother viewers.
