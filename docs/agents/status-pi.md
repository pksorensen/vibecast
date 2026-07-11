# Conformance status: pi (pi-coding-agent)

**Result: 11/11 green** (C01–C11) on `feat/multi-agent-pi`, merged to base `feat/multi-agent`.
Real-mode model: **Azure Foundry gpt-5.5** via `pks foundry proxy` (Poul's choice), `api=openai-completions`.
Installed pi: `@mariozechner/pi-coding-agent` 0.73.1, Node 20. Full suite ~65s.

| Scenario | Status | Notes |
|---|---|---|
| C01 launch-registers | ✅ | agent-launch-independent; needs no model |
| C02 session-identity | ✅ | discover-identity: the extension's `session_start` reports pi's UUIDv7 at launch |
| C03 initial-prompt | ✅ | `before_agent_start` → `hook prompt` |
| C04 system-prompt-honored | ✅ | `--append-system-prompt`; reply via Stop payload fallback |
| C05 tool-events | ✅ | `tool_call`→`hook tool`, `tool_execution_end`→`hook post-tool`; pi write tool = lowercase `write` |
| C06 turn-complete | ✅ | `agent_end`→`hook stop`; extension supplies `transcript_lines` (pi has no claude transcript) |
| C07 completion-conclusion | ✅ | `stop_broadcast` is a **native pi tool** (no MCP) execing `vibecast stop-broadcast`; job-mode mandate |
| C08 guard-denies | ✅ | guard made case-insensitive for `bash`; needed `openai-completions` (see below) |
| C09 resume-relaunch | ✅ | `pi --session <uuid>` (0.73.1); `resumeCommandFragments(pi) = {"--session", id}` |
| C10 session-end-reported | ✅ | control-socket `/stop-broadcast`; agent-independent |
| C11 prompt-injection-tui | ✅ | `firesSessionStartAtLaunch(pi)=true` gates send-keys on `session_start` |

## Architecture (how pi differs from claude/codex)

pi ships **no shell-command hooks and no MCP** — its in-process TS extension system IS the hook
mechanism. vibecast ships a single `vibecast.ts` (`internal/agent/pi_extension.ts`, go:embed'd,
written by the config-seed into `$PI_CODING_AGENT_DIR/extensions/`, auto-discovered). It:

- translates pi lifecycle events → `vibecast hook <sub>` execs with Claude-shaped payloads (reusing
  every agent-agnostic hook handler);
- runs the guard synchronously and returns `{block:true, reason}` on the handler's exit-2 deny (C08);
- registers `stop_broadcast` as a **native pi tool** (`pi.registerTool`, typebox params, defensively
  loaded) that execs `vibecast stop-broadcast` — the codex-MCP analog (C07);
- since pi has no claude-format transcript, passes `last_assistant_message` + `transcript_lines`
  (built from `agent_end.messages`) in the Stop payload; `handleHookStop` falls back to them.

Adapter (`internal/agent/pi.go`): minimal command — no permission/hook-trust/MCP flags;
discover-identity; `pi --session <uuid>` resume (`--continue` fallback); Foundry rides as
`--model 'pks-foundry/gpt-5.5'` (or the config default). Job-mode `piJobModeInstructions` prepended
to `--append-system-prompt`.

## Load-bearing gotchas (read before onboarding another agent against Foundry)

1. **`api=openai-completions`, NOT `openai-responses`.** gpt-5.5 is a reasoning model; Foundry's
   Responses API 400s a follow-up turn that references a prior reasoning item unless `store=true`.
   pi's Responses client hardcodes `store=false` (its `supportsStore` compat flag, confusingly, only
   ever *sets* `store=false`). C08's blocked-tool second turn tripped it. Chat Completions is stateless
   (no server-side reasoning-item persistence) and handles the multi-turn path.
2. **Foundry prompt shield** refuses "reply with exactly this token, say nothing else" (0/4 — a
   canary/prompt-injection pattern). Conformance C06/C09 reworded to a benign integration check that
   still elicits the nonce (4/4). Applies to any content-filtered provider.
3. **Guard tool-name casing.** The process-kill guard matched only claude/codex's `Bash`; pi's tool is
   lowercase `bash`. Now case-insensitive (`strings.EqualFold`) — agent-agnostic.
4. **Headless stdin.** `pi --print` blocks on inherited stdin; needs `< /dev/null` for headless probes
   (non-issue under vibecast's tmux PTY).
5. **Config isolation** via `PI_CODING_AGENT_DIR` (relocates all of `~/.pi/agent`). No first-run trust
   dialog on 0.73.1. `PI_SKIP_VERSION_CHECK` + `PI_OFFLINE` for deterministic startup (offline does not
   block the model call).

## Foundry wiring (conformance harness)

`conformance/harness/foundry.go` starts one shared `pks foundry proxy` (free port + fixed token) when
pi is selected (TestMain); `preparePiConfig` writes pi's `models.json` (pks-foundry provider →
proxy, `openai-completions` + Bearer proxy-token) + `settings.json` (default provider/model) and
passes `FOUNDRY_PROXY_TOKEN`. No pks → keyless pi (C01/C02 only). Credential-less CI (mock provider)
is future work.
