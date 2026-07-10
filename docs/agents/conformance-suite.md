# Agent conformance suite

The automated verification loop for agent integrations. An agent adapter is "onboarded"
when the suite is green for every capability it declares. This is also the start of
vibecast's general verification loop: the same harness exercises the full Operator
pipeline (launch → tmux → events → server contract → conclusion) end-to-end.

Prior art: agentapi's echo-agent scenario suite (same scenarios validated two adapter
backends) and vibe-kanban's recorded-fixture replay + QaMock executor. We assemble all
three layers.

## Architecture

```
go test ./conformance -args -agent=claude
        │
        ├── mockserver  (httptest) implements the platform contract vibecast talks to:
        │     POST /api/lives/metadata          → records every event, in order
        │     POST /api/lives/session-event     → records; start returns {ok, pin, env:{},
        │                                          agentSessionId? (resume scenarios)}
        │     GET  /api/lives/question-vote     → scripted answers per scenario
        │     GET  /api/lives/sessions/{id}/pending-answer → scripted
        │     POST /_relay/snapshot             → ignored
        │     WS   /api/lives/broadcast/ws      → accepts; records text frames (stream_info,
        │                                          capabilities, dims) + 0x30 byte counts
        │
        ├── vibecast under test: the real binary, launched as a subprocess with
        │     AGENTIC_SERVER=127.0.0.1:<mock port>, VIBECAST_AGENT=<agent>,
        │     VIBECAST_HOME=<tempdir>, SESSION_ID=<test id>, job-mode env as needed.
        │     It creates the real tmux session and launches the real agent.
        │
        └── assertions: ordered event-stream matchers over mockserver's log
              (subtype, required fields, deadlines), plus filesystem probes in the
              scenario workspace and tmux pane captures on failure.
```

Requirements on the host: `tmux`, `ttyd`, the agent binary, and (for real-model runs)
that agent's credentials. Every scenario runs in a throwaway workspace + `VIBECAST_HOME`;
tmux sessions are namespaced `vibecast-conformance-*` and killed in teardown.

## Determinism strategy: three run modes

| Mode | Agent behind the TUI | Needs credentials | Where it runs |
|---|---|---|---|
| `real` | the actual model | yes (claude: subscription; codex: ChatGPT login; pi: key/OAuth/gateway) | dev machines, nightly |
| `mockmodel` | real agent binary, fake model backend — **pi**: `--provider mock` extension (verified); **claude/codex**: not available in v1 | no | CI, every push |
| `echo` | scripted fake agent binary implementing the adapter's wiring (agentapi's echo.go pattern) — exercises vibecast's plumbing, not the agent | no | CI, every push (later phase) |

Real-model scenarios keep prompts cheap and deterministic-ish with **nonce protocols**:
"Reply with exactly the text VC-NONCE-<8hex> and nothing else", "Create a file named
<nonce>.txt containing <nonce> using your file-write tool". Assertions match events and
filesystem effects, never exact model prose.

## Scenario catalog v1

Each scenario declares `requires:` capabilities (from
[adapter-spec §Capabilities](adapter-spec.md)). Undeclared capability → scenario reports
SKIP (never silent), and a capability declared but failing → FAIL. Deadlines are generous
(agent startup can be slow) but every wait is bounded.

| ID | Name | Requires | Given / When / Then |
|---|---|---|---|
| C01 | launch-registers | — | vibecast launched with agent X → mockserver receives session-event `start` and broadcaster WS connects with `stream_info` + `capabilities` frames ≤ 60s |
| C02 | session-identity | lifecycle | agent starts → metadata `session_start` with non-empty `agentSessionId` (preassign: equals the minted id; discover: any stable id) ≤ 90s |
| C03 | initial-prompt-published | prompt | `VIBECAST_INITIAL_PROMPT_FILE` = nonce prompt → metadata `prompt` event whose text contains the nonce, before first `tool_use` |
| C04 | system-prompt-honored | system_prompt.append | station prompt = "when asked for the station code, answer <nonce>"; initial prompt asks for the station code → some `assistant_response`/final text contains the nonce |
| C05 | tool-events | tool_calls | initial prompt = "create file <nonce>.txt with content <nonce>" → `tool_use` (pre) and `tool_use_end` (post) with a file-write-class toolName + matching `toolCallId` pair; file exists on disk with content |
| C06 | turn-complete | lifecycle | after C03's reply → normalized turn-end observed (final `assistant_response` with text) ≤ deadline |
| C07 | completion-conclusion | vibecast_tools | job-mode session; prompt instructs calling the stop tool with conclusion=success → session-event `end` with `conclusion:"success"` + message; job's stop path identical across agents |
| C08 | guard-denies | guard.deny | prompt = "run `pkill -f conformance-sentinel` then report what happened" → NO such process killed (sentinel survives), a deny is observable (pre_tool followed by error/deny evidence), agent continues (turn completes) |
| C09 | crash-resume | session.resume | run C05, then SIGKILL the agent pane process; relaunch vibecast with `--resume <streamId>` → `session_start` with `source`∈{resume,startup} and SAME `agentSessionId` (or continued session), and the agent can answer "what nonce did you write earlier?" with the nonce (real mode only) |
| C10 | session-end-reported | — | quit the agent (or kill pane) → session-event `end` arrives with the 10s flush grace respected |
| C11 | prompt-injection-chat | — | send a chat.message-style injection via tmux path → `prompt` event for the injected text (validates send-keys submission for this agent's TUI) |
| C12 | auth-gate-detected | — (real, logged-out only; manual) | launch with credentials absent/relocated → `onboarding_external` or `url_detected` with this agent's login classifier ≤ 120s. Not in CI; runbook scenario |

Notes:

- C08's sentinel: the harness starts a `sleep`-loop process whose cmdline contains
  `conformance-sentinel` before the scenario and asserts it is still alive after. This
  tests the *policy effect*, not the deny message format. The prompt must instruct the
  model to report the failure rather than retry alternatives (the codex probe showed
  models circumventing naive denies — the assertion is only "sentinel alive + turn
  completed", never "model gave up").
- C09 is the highest-value scenario (crash-resume is vibecast's flagship behavior) and
  the most agent-coupled: preassign agents resume by the minted id; discover agents by
  the captured one; pi additionally requires same-cwd relaunch.
- C07 pins the ALP Runner contract: whatever the agent, the Runner sees the same
  session-event `end` shape. This is the invariant that makes agents swappable in
  assembly lines.

## Suite layout in the repo

```
conformance/
  harness/          # mockserver, vibecast process runner, tmux helpers, event matchers
  scenarios/        # one file per scenario, table-driven; scenario = Go code, not YAML
  agents_test.go    # TestConformance/<agent>/<scenario> — -agent flag or VIBECAST_CONFORMANCE_AGENTS
  fixtures/         # recorded native envelopes per agent (unit-replay through ParseHookEnvelope)
```

Two test tiers:

1. **Unit tier** (no tmux, no agent, always in CI): recorded-fixture replay — captured
   native hook/extension envelopes from real runs are committed under `fixtures/<agent>/`
   and replayed through `ParseHookEnvelope` → normalized-event golden tests. Catches
   agent version churn cheaply. Command-construction golden tests live here too.
2. **Scenario tier** (tmux + agent binary): the C-scenarios above. Gated by build tag
   `//go:build conformance` so `go test ./...` stays fast.

Run examples:

```bash
# unit tier (always)
go test ./internal/agent/... ./conformance/harness/...

# scenario tier, one agent
go test -tags conformance ./conformance -run 'TestConformance/claude' -v

# scenario tier, everything available on this machine
VIBECAST_CONFORMANCE_AGENTS=claude,codex,pi go test -tags conformance ./conformance -v
```

The runner auto-skips agents whose binary or credentials are missing, with a loud SKIP
reason (never a silent pass).

## Reporting

`go test -json` output is the machine surface. A tiny `conformance/report` helper renders
the capability × scenario matrix per agent:

```
agent=codex   version=0.142.5
  C01 launch-registers        PASS   4.1s
  C02 session-identity        PASS   8.9s   (discover)
  C05 tool-events             PASS  41.2s
  C08 guard-denies            PASS  33.0s
  C09 crash-resume            FAIL   —      resume produced new session id (expected 019f…)
  plan_events                 SKIP  (capability not declared)
```

A FAIL on a declared capability blocks merging that agent's branch into
`feat/multi-agent`. The matrix output is committed to `docs/agents/status-<agent>.md`
on each agent branch as the "findings report" that merges back to base.

## Roadmap after v1

- echo-agent mode (scripted fake agent per wiring style) to move C01–C08 into every-push CI.
- Scenario for permission voting (C13: `permission_request` → scripted vote → allow/deny
  injection) once codex `PermissionRequest` payloads are captured.
- Wire the scenario tier into the release flow (label-gated, like the existing e2e suite).
- Extend the harness to double as the general vibecast e2e loop (viewer WS assertions,
  resize storms per docs/terminal-sizing.md).
