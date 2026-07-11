# Conformance status: codex (OpenAI Codex CLI)

**Result: 11/11 green** (C01–C11) on `feat/multi-agent-codex`, merged to base `feat/multi-agent`.
Installed codex: `@openai/codex` 0.142.5. Real-mode via the user's ChatGPT login (auth.json copied
into an isolated CODEX_HOME).

| Scenario | Status | Notes |
|---|---|---|
| C01 launch-registers | ✅ | |
| C02 session-identity | ✅ | discover-identity: codex mints its own UUIDv7, surfaced via SessionStart |
| C03 initial-prompt | ✅ | positional prompt auto-submits |
| C04 system-prompt-honored | ✅ | `-c developer_instructions`; rollout→claude transcript normalization |
| C05 tool-events | ✅ | **needs `-s danger-full-access`** (see below) |
| C06 turn-complete | ✅ | |
| C07 completion-conclusion | ✅ | vibecast MCP `stop_broadcast`; needs the job-mode `developer_instructions` mandate (0/5 without, 8/8 with) |
| C08 guard-denies | ✅ | PreToolUse guard hook still fires under danger-full-access |
| C09 resume-relaunch | ✅ | `codex resume <uuid>`; order-independent matcher |
| C10 session-end-reported | ✅ | |
| C11 prompt-injection-tui | ✅ | REPL-ready marker "OpenAI Codex" (codex fires no SessionStart at idle) |

## Load-bearing decisions

1. **`-s danger-full-access` (operator-signed-off).** codex's default `workspace-write` OS sandbox
   wraps writes in a bundled `codex-linux-sandbox` that needs an unprivileged userns + uid_map write,
   which the ALP Runner container denies (`unshare -Ur true` → `Operation not permitted`). Under
   workspace-write every apply_patch fails → C05 timed out. `-s danger-full-access` (sandbox_mode
   ONLY — NOT `--dangerously-bypass-approvals-and-sandbox`, which also kills approvals and is forbidden
   by a golden-test invariant) removes only the OS backstop; approval stays on-request and the
   PreToolUse guard hook still fires (C08). The container is the isolation boundary. See
   `internal/agent/codex.go` `codexSandboxFlag`.
2. **Plugins off** (`-c features.plugins=false`). A fresh CODEX_HOME stages the plugin marketplace via
   a detached git clone — a per-launch network/latency/failure surface for an unattended Operator, and
   the source of a temp-dir cleanup race in conformance. See `codexPluginDisableFlag`.
3. **MCP tool exposure.** codex 0.142.x defers MCP-server tools behind a `tool_search` meta-tool;
   disabling `features.tool_suggest` + `features.tool_search_always_defer_mcp_tools` puts vibecast's
   tools back in the direct toolset (load-bearing for C07).
4. **Hook-trust bypass** (`--dangerously-bypass-hook-trust`) so vibecast's generated hooks.json loads
   without the interactive review gate — hook-trust only, a separate axis from sandbox/approvals.

## Known open item (non-blocking)

codex rejects claude's `{"additionalContext":…}` SessionStart hook stdout with a non-fatal
"hook returned invalid session start JSON output" warning; the metadata-POST side effect still fires,
so all scenarios pass. Emitting codex's expected SessionStart schema is a cosmetic follow-up.
