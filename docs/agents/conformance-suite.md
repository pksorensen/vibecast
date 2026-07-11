# Agent conformance suite

The automated verification loop for agent integrations. An agent adapter is "onboarded"
when the suite is green for every capability it declares **that the catalog exercises**
(the report marks declared-but-unexercised capabilities explicitly — see Reporting).
This is also the start of vibecast's general verification loop: the same harness
exercises the full Operator pipeline (launch → tmux → events → server contract →
conclusion) end-to-end.

Prior art: agentapi's echo-agent scenario suite and vibe-kanban's recorded-fixture
replay + QaMock executor.

> Reviewed 2026-07-10 by an adversarial testability pass against the real vibecast
> source; this version incorporates its findings (launch ceremony, C09 split, deny
> observability, job-mode matrix, mock endpoint completeness).

## Architecture

```
go test -tags conformance ./conformance          (agents via VIBECAST_CONFORMANCE_AGENTS)
        │
        ├── mockserver (httptest, per scenario, STATEFUL) — every endpoint vibecast dials:
        │     GET  /api/lives/auth-config          → {authRequired:false}
        │     POST /api/lives/metadata             → records every event, in order
        │     POST /api/lives/session-event        → records; start returns {ok, pin, env:{},
        │                                             claudeSessionId: <the id captured from
        │                                             this stream's earlier session_start
        │                                             metadata, else null>}   ← resume path
        │     GET  /api/lives/question-vote        → scripted answers per scenario
        │     GET  /api/lives/sessions/{id}/pending-answer → scripted
        │     POST /_relay/snapshot                → 200, ignored
        │     PUT  /api/lives/sessions/{id}/workspace-archive → 200, recorded
        │     WS   /api/lives/broadcast/ws         → accepts; records text frames
        │                                             (stream_info, capabilities, dims)
        │                                             + 0x30 byte counts
        │     WS   /api/lives/chat/ws              → accepts, logs frames (vibecast dials
        │     WS   /api/lives/chat/channel/ws        these repeatedly; refusing = noise)
        │
        ├── vibecast under test — launched THE WAY THE RUNNER LAUNCHES IT (vibecast does
        │   not stream as a bare subprocess; it opens a lobby and waits):
        │     1. private tmux server per scenario:  TMUX_TMPDIR=<scenario tmpdir>
        │        (isolates the hardcoded 'vibecast-lobby' session name + server env;
        │        enables parallel scenarios; keeps dev machines' real lobby safe)
        │     2. harness runs vibecast inside a detached tmux session with `unset TMUX`,
        │        env: AGENTIC_SERVER=127.0.0.1:<mock>, VIBECAST_AGENT=<agent>,
        │        VIBECAST_HOME=<tempdir>, SESSION_ID=<test id>, prompt files,
        │        per-scenario job/trust env (see Scenario env matrix)
        │     3. wait for $VIBECAST_HOME/control.sock
        │     4. POST /start-stream on the control socket (as pks-cli does)
        │     5. poll GET /status until phase ∈ {starting, live}
        │
        └── assertions: ordered event-stream matchers over mockserver's log (subtype,
              required fields, deadlines) + filesystem probes (workspace files,
              $VIBECAST_HOME session file, guard-denials log) + control-socket /status
              + tmux capture-pane dumps on failure.
```

Harness env, always: `CLAUDE_AUTO_UPDATE_DISABLED=1` (or a pinned `CLAUDE_VERSION`),
`PI_SKIP_VERSION_CHECK=1`, codex equivalent — the update gate can otherwise burn 120s
and silently change the agent version mid-suite. The harness records each agent's
resolved version once per run, before C01.

Host requirements: `tmux`, `ttyd`, the agent binary, and (for real-model runs) that
agent's credentials. Throwaway workspace + `VIBECAST_HOME` + `TMUX_TMPDIR` per
scenario; teardown kills the private tmux server.

**Field names**: the mockserver and all matchers speak **today's contract** —
`claudeSessionId` on session_start/tool_use/session-event-start-response (that is what
the byte-identical Claude adapter emits and what `ResumeStream` decodes). Matchers
additionally accept `agentSessionId` so they keep passing once dual-write lands
(adapter-spec §6).

## Determinism strategy: three run modes

| Mode | Agent behind the TUI | Needs credentials | Where it runs |
|---|---|---|---|
| `real` | the actual model | yes (claude: subscription; codex: ChatGPT login; pi: key/OAuth/gateway) | dev machines, nightly |
| `mockmodel` | real agent binary, fake model backend — **pi only**: a conformance-owned, scenario-aware mock-provider extension that vibecast ships (built on `pi.registerProvider`; the pattern was verified 2026-07-10, the extension itself is suite work: it must parse nonces out of prompts and emit matching tool calls per scenario). Injected via `VIBECAST_AGENT_EXTRA_ARGS='-e <path>/mock-provider.ts --provider mock --model mock-model'`. claude/codex have no equivalent | no | CI, every push |
| `echo` | scripted fake agent binary implementing the adapter's *wiring style* | no | CI (later phase; see honesty note) |

**Honest coverage statement (v1):** per-push CI covers the **unit tier + pi mockmodel
scenarios only**. Claude and codex scenario runs require real credentials and happen on
dev machines and nightly — the merge gate for those two agents is those runs, not CI.

**Echo-mode honesty:** a fake *claude* is a mini-emulator (assistant_response/usage are
derived from transcript-increment reads, so it must write valid Claude-format JSONL
*and* implement 12 hook events with matcher/exit-code semantics). Echo mode is therefore
scoped to the codex/pi wiring styles first (hook envelopes / extension events suffice
there); a claude echo agent is roadmap, budgeted as its own work item.

Real-model scenarios keep prompts cheap and deterministic-ish with **nonce protocols**:
"Reply with exactly the text VC-NONCE-<8hex> and nothing else", "Create a file named
<nonce>.txt containing <nonce> using your file-write tool". Assertions match events and
filesystem effects, never exact model prose.

## Scenario env matrix

Job mode is not a free knob — it changes the machine under test in two directions:
**with** `AGENTICS_JOB_MODE=1` the Stop hook blocks the agent's stop twice demanding
`stop_broadcast` then auto-ends with conclusion `incomplete` (a spontaneous session end
mid-scenario); **without** it, none of the onboarding auto-answers run, so a fresh
workspace can block forever at a first-run dialog. Therefore:

| Scenarios | Mode | First-run dialogs handled by |
|---|---|---|
| C01–C06, C08, C09, C11 | non-job | **pre-trusted workspace**: claude → isolated `CLAUDE_CONFIG_DIR` with pre-seeded trust (`hasTrustDialogAccepted`) ; codex → `-c 'projects."<dir>".trust_level="trusted"'`; pi → `--approve` / pre-seeded `~/.pi/agent/trust.json` |
| C07, C10 | job mode (`AGENTICS_JOB_MODE=1`, `AGENTICS_JOB_ID=test-…`) — scenario ends via the stop path before the auto-`incomplete` enforcement can fire, and assertions tolerate stop-block turns | job-mode auto-answers active |
| C12 | job mode (the onboarding_external detector is job-mode-only) | n/a (logged-out is the point) |

## Scenario catalog v1

Each scenario declares `requires:` capabilities (from
[adapter-spec §Capabilities](adapter-spec.md)). Undeclared capability → scenario reports
SKIP (never silent); a capability declared but failing → FAIL. Every wait is bounded.
Mode validity: R = real, M = pi mockmodel.

| ID | Name | Requires | Modes | Given / When / Then |
|---|---|---|---|---|
| C01 | launch-registers | — | R M | launch ceremony completes → mock receives session-event `start`, broadcaster WS connects, `stream_info` + `capabilities` frames arrive ≤ 60s after /start-stream |
| C02 | session-identity | lifecycle | R M | agent starts → metadata `session_start` with non-empty `claudeSessionId` ≤ 90s. Preassign agents: value equals the minted id — probe: compare against `<VIBECAST_HOME>/sessions/<streamId>.json` `Panes[].ClaudeSessionID`. Discover agents: any stable non-empty id |
| C03 | initial-prompt-published | prompt | R M | `VIBECAST_INITIAL_PROMPT_FILE` = nonce prompt → a `prompt` event whose text contains the nonce arrives ≤ deadline (ordering vs tool_use is advisory only — hooks POST async and can race) |
| C04 | system-prompt-honored | system_prompt.append | **R only** (a canned provider cannot "honor" anything) | station prompt = "when asked for the station code, answer <nonce>"; initial prompt asks for it → some `assistant_response` text contains the nonce |
| C05 | tool-events | tool_calls | R M (M = scripted write tool call) | prompt = "create file <nonce>.txt with content <nonce>" → `tool_use` and `tool_use_end` with a write-class toolName and matching `toolUseId` pair; file exists with content. **codex** requires `-s danger-full-access` (`codexSandboxFlag`): its default workspace-write OS sandbox can't initialize in the runner container (userns/uid_map denied), so apply_patch fails and the pair never completes; the run stalls on an unanswerable "retry without sandbox?" prompt. danger-full-access disables only the OS sandbox — the PreToolUse guard hook still fires (C08 stays green), so the guard is the floor. See [research/codex.md](research/codex.md) §Recommended integration strategy (e) |
| C06 | turn-complete | lifecycle | R M | after C03's reply → a turn-end signal is observed (Stop-derived final `assistant_response` event; its `text` field may legitimately be empty if a streamed `assistant_response` already carried the reply — assert nonce ∈ any assistant_response text AND the final event's arrival) |
| C07 | completion-conclusion | vibecast_tools | R | job-mode; prompt instructs calling the stop tool with conclusion=success → session-event `end` with `conclusion:"success"` + message; identical shape across agents (the ALP Runner contract). **codex** also needs the job-mode `developer_instructions` mandate (`codexJobModeInstructions`) — without it gpt-5.5 finishes without calling the stop tool (measured 0/5 without, 8/8 with; see [research/codex.md](research/codex.md) §mcp) |
| C08 | guard-denies | guard.deny | R M | a sentinel process (`conformance-sentinel` in its cmdline) runs; prompt = "run `pkill -f conformance-sentinel`; if it fails, do NOT try any other way to stop it — report the error instead" → deny record exists in `$VIBECAST_HOME/guard-denials/<streamId>.jsonl` (adapter-spec §4) AND the turn completes. Sentinel death with a recorded deny = prompt-compliance flake → retry once (the guard demonstrably worked; models can legitimately kill via allowed `pgrep`+`kill <pid>`) |
| **C09** | **resume-relaunch** (Runner contract) | session.resume | R | **IMPLEMENTED** (`C09_resume_relaunch`, green 2026-07-10). Phase 1 brings the agent live with one real turn (so a resumable transcript exists) and the harness harvests the reported+recorded agent session id; the whole run is torn down (agent + tmux + vibecast) keeping VIBECAST_HOME/workspace/config-seed; phase 2 relaunches vibecast fresh with `VIBECAST_RESUME_SESSION_ID=<harvested id>` + `SESSION_ID=<streamId>` and POST `/start-stream` → StartStream → SpawnPane relaunches the agent with `claude --resume <id>` (probe: agent pane `#{pane_start_command}`), and a **fresh `session_start` beyond phase 1's** proves the agent re-registered. This is the Runner's real recovery path (`VIBECAST_RESUME_SESSION_ID` env — see agentics-store.ts), not the interactive `vibecast --resume <streamId>` flag. Identity assertion is **per-adapter** (see below) |
| ~~C09a~~ | resume: tmux-alive reattach | session.resume | — | **DEFERRED (not in CI)**: `stream.ResumeStream` (per-pane ttyd reattach, no agent relaunch) is reachable only via the splash-screen Enter keypress in the TUI, and the headless program is built `tea.WithInput(nil)` — there is **no control-socket trigger**. Driving it would need a new `/resume-stream` control route (a product addition). The Runner also never uses this path (it always relaunches the agent via `VIBECAST_RESUME_SESSION_ID`), so it is out of v1 scope. Tracked as a product gap below |
| ~~C09c~~ | resume: server-fetch | session.resume | — | **DEFERRED (not in CI)**: recovering the agent id from the session-event start response (`claudeSessionId`) is a `ResumeStream`-only feature — `StartStream` (the headless path) does not parse it. Same unreachability as C09a, and the Runner never wipes the id it passes, so this variant is not part of the production contract |
| C10 | session-end-reported | — | R M | job-mode session; POST `/stop-broadcast` on the control socket (message + conclusion) → session-event `end` arrives with those fields, no earlier than the ~10s flush grace, after trailing metadata. (Pane-kill does NOT produce `end` — that path is ws-relay's job in production and is out of mockserver scope) |
| C11 | prompt-injection-tui | — | R M | harness-driven `tmux send-keys` of a nonce message (v1 validates the agent's REPL submit semantics; vibecast's chat-channel delivery machinery gets C11b later via a mock chat-channel WS) → `prompt` event with the nonce ≤ deadline |
| C12 | auth-gate-detected | — | R, manual runbook (not CI) | launch with credentials absent/relocated, job mode → `onboarding_external` or `url_detected` with this agent's login classifier ≤ 120s. Also pins each adapter's ClassifyURL patterns empirically (codex's are currently assumed, never captured) |

**C09 identity assertion is per-adapter** — resume semantics differ and "same id" is
wrong for claude: `claude --resume <uuid>` mints a NEW session id (the platform is
built around harvesting the new id after every job). Assertions: **claude** = the
relaunch command contained `--resume <old id>` (probe: pane launch cmdline /
VIBECAST_HOME session file) and a fresh `session_start` arrived; **codex** = expected
same thread id (verify empirically before pinning); **pi** = same id (verified
2026-07-10). Context recall ("what nonce…") is the only true resume proof and runs in
real mode only; mock-mode C09 verifies the mechanical relaunch flags.

Capability coverage note: `plan_events`, `subagent_events`, `compaction_events`,
`usage.tokens`, `transcript.upload` have **no scenario yet** — the report lists them as
`declared, unverified (no scenario)` rather than green. Roadmap adds: C13 plan event
(real-mode plan prompt → `plan` with planMarkdown), C14 usage-present (usage object on
assistant_response/tool_use_end), C15 question round-trip (AskUserQuestion → scripted
pending-answer → injected answer unblocks the turn — this finally covers the
answer-injection tables, the most version-brittle code in vibecast).

## Suite layout in the repo

```
conformance/
  harness/          # mockserver, launch ceremony, tmux helpers, event matchers
  scenarios/        # one file per scenario, table-driven; scenario = Go code, not YAML
  agents_test.go    # TestConformance/<agent>/<scenario>; agents from VIBECAST_CONFORMANCE_AGENTS
  fixtures/         # recorded native envelopes per agent (unit-replay through ParseHookEnvelope)
  mockprovider/     # the pi scenario-aware mock provider extension (mockmodel mode)
```

Two test tiers:

1. **Unit tier** (no tmux, no agent, always in CI): recorded-fixture replay — captured
   native hook/extension envelopes from real runs, committed under `fixtures/<agent>/`,
   replayed through `ParseHookEnvelope` → normalized-event golden tests, plus
   `SerializeHookResponse` golden tests per (event × intent-origin) pair and
   command-construction golden tests. Catches agent version churn cheaply.
2. **Scenario tier** (tmux + agent binary): the C-scenarios. Build tag
   `//go:build conformance` so `go test ./...` stays fast.

Run examples:

```bash
# unit tier (always)
go test ./internal/agent/... ./conformance/harness/...

# scenario tier, one agent
VIBECAST_CONFORMANCE_AGENTS=claude go test -tags conformance ./conformance -v

# scenario tier, everything available on this machine
VIBECAST_CONFORMANCE_AGENTS=claude,codex,pi go test -tags conformance ./conformance -v
```

Agent selection is `VIBECAST_CONFORMANCE_AGENTS` (+ standard `-run` filtering); the
runner auto-skips agents whose binary or credentials are missing, with a loud SKIP
reason (never a silent pass).

## Reporting

`go test -json` output is the machine surface. A tiny `conformance/report` helper renders
the capability × scenario matrix per agent:

```
agent=codex   version=0.142.5   mode=real
  C01 launch-registers        PASS   4.1s
  C02 session-identity        PASS   8.9s   (discover)
  C05 tool-events             PASS  41.2s
  C08 guard-denies            PASS  33.0s
  C09 resume-relaunch         FAIL   —      relaunch cmd missing resume flag
  subagent_events             DECLARED, UNVERIFIED (no scenario)
  plan_events                 SKIP  (capability not declared)
```

A FAIL on a declared+exercised capability blocks merging that agent's branch into
`feat/multi-agent`. The matrix output is committed to `docs/agents/status-<agent>.md`
on each agent branch as the "findings report" that merges back to base.

## Roadmap after v1

- C13/C14/C15 (plan, usage, question round-trip) to close the declared-capability gaps.
- echo-agent mode for the codex/pi wiring styles; claude echo emulator as its own item.
- C11b: chat-channel WS delivery path through a mock chat endpoint.
- **C09-reattach + C09-server-fetch** (product gap): a headless `/resume-stream` control
  route is needed before either can be driven in CI. Today `stream.ResumeStream` (per-pane
  ttyd reattach without relaunching the agent, and its server-side `claudeSessionId`
  recovery) is reachable only via the interactive splash Enter keypress; the headless
  program has `tea.WithInput(nil)`, and `StartStream` neither triggers reattach nor parses
  `claudeSessionId`. Add the control route → then C09a/C09c become testable. Neither is on
  the Runner's production path (it always relaunches via `VIBECAST_RESUME_SESSION_ID`).
- Wire the scenario tier into the release flow (label-gated, like the existing e2e suite).
- Extend the harness toward the general vibecast e2e loop (viewer WS assertions, ws-relay
  end-on-disconnect, resize storms per docs/terminal-sizing.md).
