# Onboarding playbook: adding a new coding agent to vibecast

A step-by-step, mostly-mechanical procedure. It is written so that a competent engineer —
or an inexpensive model — can onboard agent N+1 without re-deriving the architecture.
Read [adapter-spec.md](adapter-spec.md) once before starting; then follow this checklist.

**Definition of done:** the conformance suite is green for every capability the new
adapter declares ([conformance-suite.md](conformance-suite.md)), the feature matrix has a
new column, and a findings report (`status-<agent>.md`) is merged to the base branch.

---

## Phase R — Research worksheet (no code)

Create `docs/agents/research/<agent>.md`. Answer every row **empirically** (run the
binary; docs lie, versions drift). The 12 categories mirror the
[feature matrix](feature-matrix.md) — fill in the new column as you go.

| # | Question to answer | You are done when… |
|---|---|---|
| R1 | How is it installed/pinned/updated? (npm/brew/binary; dist-tags; self-update cmd) | you can pin an exact version in one command |
| R2 | Exact launch flags: initial prompt (positional? does it auto-submit in the TUI?), append-vs-replace system prompt, working dir, config dir override | you launched it in tmux with a station prompt + initial prompt and both demonstrably took effect |
| R3 | Does the TUI run in tmux? Does `tmux send-keys 'text' Enter` submit? Any first-run dialogs (trust/theme/tour) — capture their EXACT screen text | you scripted a full first-run start-to-prompt without touching the keyboard |
| R4 | Event mechanism: hooks? extension API? notify? JSONL stream that works WITH the TUI (not instead of it)? For each lifecycle/tool/prompt event: exact payload shape | you captured real payloads for session-start, prompt, pre-tool, post-tool, turn-complete into `conformance/fixtures/<agent>/` |
| R5 | Session identity: where does the agent's session id live? Can you preassign it at launch, or only discover it? Session file location + format | you resumed a killed session by id and the agent remembered earlier context |
| R6 | Resume flags (`--resume`/`--session`/`resume <id>`), continue-latest, fork; constraints (same cwd? id format validation?) | same as R5 |
| R7 | Guard: can an external integration deny a tool call before execution? Exact deny protocol (stdout schema / exit code / return value). **Test the bypass**: deny `rm` and watch whether the model routes around it | you blocked a command, saw the agent continue gracefully, and wrote down the deny protocol |
| R8 | Auth: login flows (OAuth/device/API key), headless options, auth file location, how "logged out" presents in the TUI and in headless stderr, login URL patterns for `ClassifyURL` | you can detect logged-out state from a pane capture and preflight it before launch |
| R9 | Permission/approval prompts: does the agent have native approval dialogs? Key sequences to answer them? A hook to auto-answer? | you enumerated every dialog job-mode must auto-answer, with exact match strings |
| R10 | Token usage + compaction: where do usage counters appear? compaction events? | you can map its usage fields → the Anthropic-shaped `usage` object |
| R11 | MCP: client support + config format (for `RegisterVibecastTools`); if none, what replaces it (native tool registration)? | you know how `stop_broadcast` will reach the agent |
| R12 | Version quirks: minimum version for each needed feature, runtime floors (Node/OS), scope/package moves, known bugs | the feature-matrix column has version annotations |

⚠ Practices that saved us real pain during codex/pi research — do all of them:

- Probe in a **throwaway dir + relocated config home** (`CODEX_HOME`-style env) so you
  see true first-run behavior without disturbing real credentials.
- Capture **raw payloads to files** while probing; they become the committed fixtures.
- Drive the TUI **through tmux from the start** (send-keys, capture-pane) — that is the
  production environment, and dialog behavior differs from a bare terminal.
- Test the guard **adversarially** (deny one command, see what the model does next).
- Check whether an advertised JSON stream actually emits what the docs promise
  (codex 0.142.5's `exec --json` silently omitted all command items).

## Phase I — Implement the adapter

On a branch `feat/multi-agent-<agent>` off `feat/multi-agent`:

1. `internal/agent/<agent>/adapter.go` — implement the interface members in this order
   (each is independently testable):
   1. `Kind/DisplayName/BinaryName/Version` + registry entry.
   2. `Capabilities()` — declare ONLY what Phase R verified AND has committed fixtures.
      Undeclared = skipped, not shameful.
   3. `BuildCommand`/`BuildResumeCommand` + golden tests (exact expected strings).
   4. `InstallEventWiring` + `ParseHookEnvelope` (returns 1..N events + attachments;
      fixtures replay tests) + `Transcripts()` if the agent feeds content from its own
      transcript files + `SerializeHookResponse` per (event × intent-origin).
   5. Tool taxonomy: `MapToolName`, `WritePaths`, `SelfApprovingTools`,
      `WorktreeExclusions`.
   6. `SessionIdentity` + `ListSessions` + `ResolveSessionPath`.
   7. `EnsureVersion`.
   8. `ScreenGates()`/`AnswerHandlers()`/`ClassifyURL` from the R3/R8/R9 exact strings —
      include a `versionGlob` on every entry so UI churn in future agent versions is an
      additive table row, not a rewrite.
   9. `RegisterVibecastTools`, `TelemetryEnv`, `BusySignal` (or ErrNotSupported).
2. If the agent needs a shipped artifact (like `claude-plugin/` or `pi/vibecast.ts`),
   put it next to the binary and mirror it into `npm/*/bin/` like the claude plugin.
3. Keep EVERYTHING else untouched: no changes to tmux/relay/control-socket/emission code.
   If you feel the need, the abstraction has a gap — raise it instead of patching around.

## Phase V — Verify with the conformance suite

1. Unit tier green: fixtures replay + command golden tests.
2. Scenario tier locally: `go test -tags conformance ./conformance -run 'TestConformance/<agent>' -v`
   — work through C01→C11 in order; each scenario failure tells you which adapter member
   is wrong. Do not reorder or weaken assertions to pass; fix the adapter, or undeclare
   the capability (with a findings note).
3. Generate `docs/agents/status-<agent>.md` (the matrix report) and update the
   [feature matrix](feature-matrix.md) column with final, verified values.

## Phase P — Platform plumbing (only after V is green)

1. pks-cli Runner: per-agent launch-script section (credentials volume, agent install/
   version pin, gate claude-only scaffolding behind `agent==claude`).
2. www-site: add the agent to the station `operatorConfig.agent` union + station-inspector
   dropdown; optionally dispatch with `needs:['agent-runtime:<agent>']`.
3. Docs: `docs/alp-operator.md` env table; CHANGELOG entry.

## Phase M — Merge back

Merge the agent branch into `feat/multi-agent` with: the adapter, fixtures,
`status-<agent>.md`, the updated feature-matrix column, and research doc. The base branch
must stay green for all previously onboarded agents (run their suites too — shared-code
regressions are the main merge risk).

---

## Worked examples

[research/codex.md](research/codex.md) and [research/pi.md](research/pi.md) (2026-07)
show what a completed Phase R worksheet looks like. Phase I/V for codex and pi are the
first executions of this playbook, on `feat/multi-agent-codex` / `feat/multi-agent-pi`
per the README rollout plan — once merged, those branches become the reference
implementations.
