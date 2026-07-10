# Research findings: vibecast Claude Code coupling inventory (2026-07-10)

> Generated from the multi-agent research workflow (session 9d7d329a, 2026-07-10). Source of truth for the design docs in this folder.

## Summary

Complete inventory of Claude-Code-specific integration points in /workspaces/agentic-live-www/external/vibecast (~11k lines read in full: hooks.go, broadcast.go, stream.go, session.go, control.go, mcp/serve.go+mcp.go, types.go, cmd/root|hook|sync|stopbroadcast|cycleagent|mcp.go, claude-plugin/hooks.json+.mcp.json+plugin.json, telemetry.go, util.go, all docs/*.md, plus grep sweeps of fkeybar/tui).

The Claude coupling clusters into 8 areas: (1) LAUNCH — stream.go builds the `claude` command line (`--dangerously-skip-permissions` always, `--plugin-dir`, `--append-system-prompt "$(cat file)"`, positional initial prompt, `--model`/`--effort` with validated tier/effort tables, `--dangerously-load-development-channels`, `--session-id <pre-generated UUIDv4>` fresh / `--resume <uuid>`|`--continue` on resume), with `exec.LookPath("claude")` and a bash fallback. (2) VERSION MGMT — `claude update` / `claude install <CLAUDE_VERSION>` once per process, fail-open, gated by CLAUDE_AUTO_UPDATE_DISABLED. (3) EVENT INGESTION — the claude-plugin registers 12 Claude hook events that shell into `vibecast hook <x>`; hooks.go parses Claude's stdin envelope (session_id, transcript_path, cwd, tool_name, tool_input, tool_use_id, tool_response, agent_id/agent_type, permission_suggestions, trigger, summary…) and writes Claude's hook-output JSON (additionalContext, decision:block/deny, hookSpecificOutput.permissionDecision) with meaningful exit codes. (4) TRANSCRIPT — incremental JSONL reader for Claude's transcript format (entry types, message.content blocks, Anthropic usage token names, isSidechain), plus ~/.claude/projects/<encoded-cwd>/*.jsonl discovery for resume-picker and `vibecast sync`. (5) SESSION RESUME — claudeSessionId pre-assignment, persistence in ~/.vibecast session files and on the server, three-path recovery ending in `--resume`. (6) TUI AUTOMATION — job-mode capture-pane text matching of Claude's exact screen copy (trust 'Quick safety check', 'Login successful', 'Security notes', 'Bypass Permissions mode' option 2, 'Learn the moves' tour skip, theme picker's 7 options, login-method picker, OAuth 'Paste code here'+oauth/authorize, session-too-large menu) with tmux send-keys answers, an answer-handler table already keyed by (questionType, claude-version-glob), and Claude-dialog-specific key sequences ('1'/'3' permission keys, AskUserQuestion wizard Down/Enter/Tab navigation). (7) GUARDS + COMPLETION — sync PreToolUse Bash kill-guard, job-mode Write/Edit/NotebookEdit path guard, ExitPlanMode plan extraction, and the Stop-hook conclusion enforcement that sniffs Claude's 'N local agents' status-bar text and forces the stop_broadcast MCP call. (8) CONFIG SURFACES — .mcp.json injection (Claude's MCP config format), CLAUDE_CODE_ENABLE_TELEMETRY/_ENHANCED_TELEMETRY_BETA env propagation, VIBECAST_CLAUDE_* env names, and UI copy.

Already agent-agnostic and reusable unchanged: all tmux/ttyd orchestration (session/window/group-session management, fixed sizing, remain-on-exit, respawn+pane_dead probes, F-keys, fkeybar), the broadcaster/chat/chat-channel WebSocket relays, snapshot posting, viewer keyboard relay with PIN, the control socket, the vibecast MCP server protocol plumbing and conclusion pipeline (stop_broadcast → control → session-event end), the ~/.vibecast session store, Keycloak auth, OTEL, and the metadata POST transport. The event names/payloads themselves (documented exhaustively in event_schema_notes) are the server contract and are Claude-shaped in field names (claudeSessionId, Anthropic usage keys, raw Claude transcriptLines, Claude tool names).

## Integration points

### launch-command-build (launch, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/stream/stream.go:320-346
- buildClaudeCommand/buildClaudeResumeCommand assemble the full agent launch shell string: `claude --dangerously-skip-permissions` + plugin flags + model/effort/channel flags + --append-system-prompt + positional initial prompt + --session-id (fresh) or --resume <uuid>/--continue (resume). Executed via `tmux new-window sh -c` (SpawnPane, 633-639) or `tmux respawn-pane -k` (DoRestartClaude, 401-402), prefixed with `cd <workdir> &&` and wrapped to print `[claude] exited rc=` on exit (395).
- **Portability:** Core adapter seam. Needs an AgentAdapter.BuildCommand(opts{workdir, agentSessionID, resume, model, effort, systemPromptFile, initialPromptFile, permissionMode, pluginDirs}) → argv. Codex: `codex --dangerously-bypass-approvals-and-sandbox` / `--sandbox`, `-m <model>`, positional prompt, `codex resume <id>`; no --append-system-prompt (use AGENTS.md or -c experimental instructions). pi: its own flags. The rc-wrapper, cd-prefix and tmux spawn mechanics are already agent-agnostic.

### launch-skip-permissions (launch, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/stream/stream.go:321,340,345
- `--dangerously-skip-permissions` is hardcoded into EVERY launch/resume command (interactive and job mode). This is why the bypass-permissions confirmation dialog auto-answer exists in broadcast.go.
- **Portability:** Make permission mode an adapter option. Codex equivalent: --dangerously-bypass-approvals-and-sandbox or --full-auto; pi has its own approval model. The paired TUI confirmation dialog differs per agent.

### launch-plugin-dir (launch, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/stream/stream.go:67-81
- buildPluginFlags emits `--plugin-dir <dir>` for the claude-plugin dir shipped next to the binary (telemetry.PluginDir, internal/telemetry/telemetry.go:151-164) plus colon-separated VIBECAST_EXTRA_PLUGINS. This is how the hooks + vibecast MCP server get wired into Claude.
- **Portability:** Codex/pi have no --plugin-dir. The adapter must own 'event wiring installation': for codex, write notify/hook config or run `codex exec --json` and parse the event stream; for pi, install its hook config. The generic need is EventSource.Install(sessionEnv).

### append-system-prompt (launch, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/stream/stream.go:83-108
- buildAppendSystemPromptFlag maps VIBECAST_APPEND_SYSTEM_PROMPT / VIBECAST_APPEND_SYSTEM_PROMPT_FILE (the ALP station prompt set by the Runner) to `--append-system-prompt "$(cat file)"`, shell-escaped. readAppendSystemPrompt re-reads it for the stream_info metadata. Note: the prompt lands in Claude's argv — the reason pkill -f self-matches (see bash-guard).
- **Portability:** Env var names are already agent-neutral; only the flag emission is Claude's. Codex has no such flag — adapter could prepend to the initial prompt, write AGENTS.md, or use codex -c. Keep the file-based indirection; it avoids all quoting issues.

### initial-prompt-positional (launch, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/stream/stream.go:110-155
- buildInitialPromptArg passes the job's task prompt (VIBECAST_INITIAL_PROMPT_FILE) as a POSITIONAL argument: `"$(cat 'file')"`. Relies on Claude semantics that a positional arg starts INTERACTIVE mode with that text as the first user message (explicitly not -p print mode). Defensively drops the arg + emits vibecast.initial_prompt.missing span if the file is missing/empty.
- **Portability:** Codex positional prompt behaves similarly in interactive TUI; pi may not. Alternative injection path already exists in the codebase (literal tmux send-keys + Enter, as used for chat.message) — adapter chooses per agent. Keep the missing-file guard; it is agent-neutral.

### session-id-flag (session-identity, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/stream/stream.go:175-187,607-612
- vibecast PRE-GENERATES a UUIDv4 (SpawnPane:608) and passes it as `claude --session-id <uuid>` so it knows the agent session id before launch. claudeSessionIDFlag drops non-UUIDv4 values because Claude exits immediately on invalid ids (documented incident). util.IsUUIDv4 (internal/util/util.go:58-65) exists solely for this.
- **Portability:** Adapter needs SessionIdentity strategy: pre-assign (Claude --session-id), discover-after-launch (codex prints/stores its rollout id; would need parsing ~/.codex/sessions or the JSON event stream), or none. The SessionFile field claudeSessionId should become agentSessionId + agentKind.

### model-effort-flags (launch, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/stream/stream.go:189-226
- buildModelFlag: VIBECAST_CLAUDE_MODEL (verbatim) or VIBECAST_CLAUDE_MODEL_TIER (validated against {haiku,sonnet,opus}) → `--model`. buildEffortFlag: VIBECAST_CLAUDE_EFFORT validated against {low,medium,high,xhigh,max} → `--effort`. Comment states vibecast (Operator) owns the tier→alias mapping.
- **Portability:** Env vars are Claude-named (VIBECAST_CLAUDE_*). Abstraction: VIBECAST_AGENT_MODEL/_TIER/_EFFORT with per-adapter tier tables (codex: gpt-5.x + `model_reasoning_effort`; pi: its models). Keep the drop-unknown-with-log behavior.

### dev-channel-flag (launch, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/stream/stream.go:228-239
- buildDevAgentChannelFlag: VIBECAST_CLAUDE_CHANNEL → `--dangerously-load-development-channels '<value>'` for devcontainer devagent sessions (agent-share A2A plan).
- **Portability:** Pure Claude flag; adapter should treat as an opaque per-agent extra-args mechanism (e.g. VIBECAST_AGENT_EXTRA_ARGS).

### version-management (version-management, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/stream/stream.go:241-318
- ensureClaudeUpToDate (sync.Once per process = frozen for the whole assembly line): default runs `claude update`; CLAUDE_VERSION=x.y.z runs `claude install x.y.z` (pin wins over disable); CLAUDE_AUTO_UPDATE_DISABLED=1/true/yes/on skips. Fail-open with 120s timeout, output captured into vibecast.claude.autoupdate span. Called from both SpawnPane (620) and DoRestartClaude (379).
- **Portability:** Adapter needs VersionManager{Update(), Pin(version)}: codex = `npm i -g @openai/codex@<ver>` or brew (no self-update subcommand); pi = its installer. Env names CLAUDE_VERSION/CLAUDE_AUTO_UPDATE_DISABLED are Runner-facing contract (docs/alp-operator.md) — need AGENT_VERSION/AGENT_AUTO_UPDATE_DISABLED aliases.

### binary-lookup-fallback (launch, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/stream/stream.go:374-378,616-618
- exec.LookPath("claude") hardcoded in DoRestartClaude and SpawnPane; SpawnPane falls back to `echo 'Claude Code not found - using bash.' && exec bash` when absent.
- **Portability:** Adapter provides BinaryName()/ResolvePath(). Fallback message needs agent-neutral copy.

### restart-flow (lifecycle, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/stream/stream.go:348-453
- DoRestartClaude: respawn-pane -k with fresh or resume command, then #{pane_dead} probe after 1s + capture-pane of last 2KB into the vibecast.claude.restart span. Reachable from control socket /restart-claude?clearContext (control.go:54-73), F6 fkey (control.go:298-306), MCP restart_claude tool (serve.go:412-429), TUI session picker, and the RestartClaude callback on SharedStatus (types.go:125) used by auth-recovery.
- **Portability:** Mechanics (respawn-pane, pane_dead probe) are agent-agnostic; only the command built and the names (restart_claude, /restart-claude, ClaudeRestartedMsg) are Claude-flavored. Rename to restart_agent; resume semantics delegate to the adapter.

### hook-subcommand-envelope (hook-envelope, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/hooks/hooks.go:31-109
- `vibecast hook <event>` reads Claude Code's hook stdin envelope. Common fields parsed everywhere: cwd, transcript_path, session_id (hookReadStdinAndFindSession, 79-109; session located by workspace path match against ~/.vibecast/sessions). Per-event fields: prompt (prompt); session_id+source (session); tool_name+tool_input+tool_use_id (tool/guard); +tool_response (post-tool); agent_id+agent_type+tool_input.{prompt,description,subagent_type} (subagent-start); agent_transcript_path (subagent-stop); task_id/task_subject/task_description/teammate_name/team_name (task-created/completed); permission_suggestions (permission-request); trigger+custom_instructions (pre-compact); summary (post-compact).
- **Portability:** This is the biggest surface. Define an internal NormalizedHookEvent and make each agent adapter translate its native event feed into it. Codex has no in-process hook system like this — a codex adapter would tail `codex exec --json` events or the rollout file; pi has hooks with different field names. The workspace→session lookup and metadata POST are already agent-agnostic.

### hook-registration-plugin (hook-registration, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/claude-plugin/hooks/hooks.json:1-158
- Claude plugin registering 12 hook events → `${VIBECAST_BIN:-${CLAUDE_PLUGIN_ROOT}/../vibecast} hook <x>`: UserPromptSubmit(async), SessionStart(sync), PreToolUse matcher "Bash"→guard(sync) + matcher ""→tool(async), PostToolUse(async), SubagentStart(sync), SubagentStop(async), TaskCreated/TaskCompleted(async), Stop(sync), PermissionRequest(async), PreCompact/PostCompact(async). Sibling files: claude-plugin/.claude-plugin/plugin.json (plugin manifest), claude-plugin/.mcp.json (vibecast MCP server entry). Mirrored 4x under npm/*/bin/claude-plugin/.
- **Portability:** Entirely Claude Code plugin format (hook names, matcher semantics, async flag, ${CLAUDE_PLUGIN_ROOT} expansion, VIBECAST_BIN escape hatch). Per-agent adapters ship their own wiring artifact; the `vibecast hook` CLI entrypoint can stay as the ingestion point if adapters can shell out (pi hooks can; codex mostly can't — needs a stream-tailing sidecar instead).

### hook-output-protocol (hook-envelope, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/hooks/hooks.go:463-467,546-559,607-614,779-784,934-996,1178-1186
- Hooks WRITE Claude's hook-output JSON to stdout: SessionStart/SubagentStart emit {additionalContext} (broadcast notice / SUBAGENT_PROMPT_SUFFIX injection); guard emits legacy {decision:"block",reason} PLUS current {hookSpecificOutput:{hookEventName:"PreToolUse",permissionDecision:"deny",permissionDecisionReason}} + exit 2 for cross-version compat; job-mode write guard and Stop-hook blocks emit {decision:"block",reason} with exit 1/2; permission-request emits {decision:"deny",reason} exit 1.
- **Portability:** Output schema + exit-code semantics are pure Claude hook API. Normalized layer should express intents (InjectContext, DenyTool(reason), BlockStop(reason)) that each adapter serializes natively — codex has no synchronous-deny hook at all, so the guard would need to move into a sandbox policy or command-approval callback for codex.

### bash-guard (guard, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/hooks/hooks.go:470-560
- handleHookGuard: fast synchronous PreToolUse deny for tool_name=="Bash" commands containing broad process kills (killall, pkill by name/-f, kill -1 / kill -- -pgid; pgrep and pkill -F pidfile allowed; regexes 485-492, segment split on &&/||/;/&/|/newline). Rationale + reason text reference Claude's argv embedding the station system prompt. Tested in guard_test.go.
- **Portability:** Policy (dangerousProcessKill) is 100% agent-neutral Go; only the trigger (Claude PreToolUse/Bash) and reply schema are Claude. Codex: map to shell-command approval or sandbox config; pi: its PreToolUse equivalent. Also the reason copy says 'this Claude process'.

### jobmode-write-guard (guard, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/hooks/hooks.go:577-617
- In AGENTICS_JOB_MODE=1 with VIBECAST_ALLOWED_DIRECTORIES set, handleHookTool blocks Write/Edit/MultiEdit (tool_input.file_path) and NotebookEdit (tool_input.notebook_path) targets outside the job work tree, via {decision:block} exit 1.
- **Portability:** Hardcodes Claude tool names + input field names. Needs a per-agent tool taxonomy (codex: apply_patch paths; pi: its edit tools) feeding a shared path-containment policy.

### exitplanmode-plan (hook-envelope, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/hooks/hooks.go:619-636
- PreToolUse of tool_name=="ExitPlanMode" extracts tool_input.plan and posts a dedicated `plan` metadata event (planMarkdown) for the viewer plan card.
- **Portability:** ExitPlanMode is Claude-only. Normalized event: PlanProposed{markdown}. Codex/pi adapters may never emit it.

### askuserquestion-handling (hook-envelope, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/hooks/hooks.go:1004-1052
- handleHookPermissionRequest builds the vote question label from tool_input file_path/path/command; explicitly SKIPS tool_name AskUserQuestion/AskFollowupQuestion (answering is the approval); synthesizes toolUseId 'perm-<streamId>-<ms>' when Claude omits tool_use_id so the poll key matches the server's fallback format.
- **Portability:** Tool names and the missing-tool_use_id quirk are Claude-version-specific. The vote/poll protocol itself (server pendingQuestion + question-vote) is agent-neutral.

### stop-hook-completion (completion, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/hooks/hooks.go:894-1002
- Stop hook: (1) posts final assistant_response (+usage) from the transcript increment; (2) AGENTICS_AUTO_GIT=1 → blocks stop while `git status --porcelain` is dirty (AGENTICS_COMMIT_MESSAGE_HINT in reason); (3) job mode: sniffs the agent pane via `tmux capture-pane -t $TMUX_PANE` for regex `\d+ local agents` (Claude Code's status-bar subagent counter) and sleeps 60s then blocks; (4) job mode: queries control socket /status; if phase=="live", blocks up to 2 times demanding the agent call the stop_broadcast MCP tool (message + conclusion success|failure|cancelled), then auto-POSTs /stop-broadcast with conclusion="incomplete" (stop-block counter persisted in transcripts/<streamId>/stop_blocks, 1279-1295).
- **Portability:** Three couplings: Stop-hook existence (codex: detect turn-completion from the JSON event stream or notify hook), the 'N local agents' status-bar text sniff (pure Claude TUI copy — needs per-agent liveness signal or removal), and blocking-stop semantics (Claude honors {decision:block} by continuing the turn; codex/pi have no such re-prompt loop, so conclusion enforcement may need to move to the Operator side entirely).

### transcript-parsing (transcript, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/hooks/hooks.go:111-345,1297-1357
- Incremental transcript reader keyed by byte-offset cursors in ~/.vibecast/transcripts/<streamId>/<sha256(transcriptPath)[:8]>.offset. Parses Claude JSONL: filters entry type ∈ {user,assistant,tool_use,tool_result,thinking,result}; navigates message.content[] blocks (type text / tool_use with id); message.usage {input_tokens,output_tokens,cache_read_input_tokens,cache_creation_input_tokens}; skips isSidechain lines for streaming assistant text; readFirstUserPrompt for session summaries. Raw lines are forwarded verbatim as transcriptLines in metadata.
- **Portability:** Wholesale Claude transcript format. Adapter seam: TranscriptReader{Increment() []NormalizedLine, FirstUserPrompt(), Usage()}. Codex rollout files (~/.codex/sessions/*.jsonl) have a different line schema; pi differs again. Because raw lines flow to the server/viewer, the normalized schema must be defined server-side too (or lines tagged with a format field). The cursor mechanism itself is agnostic.

### transcript-discovery (transcript, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/session/session.go:209-298
- ScanClaudeSessions: hardcodes ~/.claude/projects/<cwd with '/'→'-'>/<sessionId>.jsonl, skips agent-*.jsonl, extracts first prompt + message count for the resume picker (fkeybar/TUI 'Sessions are stored in ~/.claude/projects/'). cmd/sync.go:105-127 resolveSessionPath duplicates the same path scheme for `vibecast sync`, which uploads raw jsonl to /api/lives/sync.
- **Portability:** Adapter: SessionStore.List(workspace) → []AgentSessionInfo and ResolvePath(id). Codex: ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl; pi: its own dir. UI copy references ~/.claude/projects too (fkeybar/bar.go:700, tui/views.go:166).

### claudesessionid-resume (session-resume, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/stream/stream.go:332-346,607-624,1117-1207,1326-1335
- Resume wiring: SessionFile.ClaudeSessionID + Panes[].ClaudeSessionID (types.go:163-180) persisted locally and on the server (via hook session metadata claudeSessionId); ResumeStream recovers it from the session-event response (claudeSessionId field) when no local file; buildClaudeResumeCommand emits `--resume <uuid>` (UUIDv4-validated) else `--continue`; Runner passes prior id via VIBECAST_RESUME_SESSION_ID (cmd/root.go:102) → ClaudeResumeID → SpawnPane claudeResumeID. Also docs/session-resume.md documents the 3 recovery paths.
- **Portability:** Rename fields to agentSessionId; adapter provides ResumeCommand(id) (codex: `codex resume <rollout-id>`; --continue equivalent: `codex resume --last`). Server API field name claudeSessionId is part of the www-site contract and needs a coordinated rename/alias.

### onboarding-auto-answer (tui-injection, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/broadcast/broadcast.go:289-682,1187-1227
- Job-mode snapshot loop (2s fast / 15s slow) captures the pane, ANSI-strips, and matches Claude Code first-run screen COPY TEXT: 'Quick safety check' trust dialog → Enter (dup detection in ttyd-relay loop, shared trustDialogAnswered atomic; VIBECAST_ALLOWED_DIRECTORIES path match gates auto-trust); 'Login successful'+'Press Enter to continue' → Enter; 'Security notes'+'Press Enter to continue' → Enter; 'Bypass Permissions mode'+'Yes, I accept' → '2'+Enter (default is 'No, exit' — blind Enter kills Claude); 'Learn the moves'+'Skip for now' tour (v2.1.205+) → '2', re-capture, conditional Enter; 'Choose the text style' theme picker → alp_pane vote with Claude's exact 7-option menu order; 'Select login method:'+'subscription' → alp_pane vote (3 options); 'Resume from summary'/'Resume full session' session-too-large menu → alp_pane vote. Rolling last-10 plain captures saved to ~/.vibecast/debug/captures. Comment at 506-510 documents deliberately NOT pre-baking ~/.claude.json (env providers ANTHROPIC_API_KEY/ANTHROPIC_BASE_URL/CLAUDE_CODE_USE_BEDROCK suppress gates upstream).
- **Portability:** The most brittle, most version-coupled block. Abstraction: a per-agent, per-version-glob table of ScreenGate{matchStrings, action(sendKeys|voteQuestion|externalAction)} — the answerHandler version-glob pattern at 804-816 already sketches this but is only used for answers, not detection. Codex onboarding: ChatGPT sign-in device flow, different copy; pi: different again. The capture/strip/debounce/injection-lock machinery is fully agent-agnostic.

### oauth-url-detection (tui-injection, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/broadcast/broadcast.go:98-105,641-682,1169-1186
- classifyURL tags URLs containing 'claude.ai' or 'auth.anthropic' as context="claude-login" in url_detected metadata (URLs scraped from the raw ttyd stdout stream with rolling 8KB buffer). The OAuth device-code gate is detected by pane text 'Paste code here' + 'oauth/authorize', URL extracted with regex `https?://[^\s]+oauth/authorize\?\S+`, published as onboarding_external with provider="claude-subscription", and the answer (the pasted code) is injected literally via send-keys -l + Enter (handler 853-868).
- **Portability:** Adapter needs AuthGateDetector{urlClassifier(patterns→provider), screenGate, answerMode}. Codex: auth.openai.com / chatgpt.com login URL + 'sign in' screens; pi: its provider. The generic external-action question flow (onboarding_external + literal injection) ports as-is.

### answer-injection-handlers (tui-injection, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/broadcast/broadcast.go:767-1128
- Job-mode unified answer injection: polls /pending-answer every 3s, dedupes by injected-question-ID set, and injects via `tmux send-keys` under a per-pane-target lock (tmuxSendLocks 50-68 serializes ALL injectors). Handlers keyed by (questionType, claudeVersionGlob) — currently all version "*", with explicit comment 'register version-specific handlers to override behaviour when the Claude Code UI changes': permission → '1'(Allow)/'3'(Deny)+Enter matching Claude's native tool-confirmation dialog; alp_pane → digit(option index+1)+Enter; onboarding_external → literal text+Enter; tool (AskUserQuestion wizard) → drives Claude's bubble-tea UI with Down/Enter/Tab per sub-question, multiSelect checkbox toggling, the invisible trailing 'Type something' row accounted for in Down-count arithmetic, Submit tab.
- **Portability:** The handler-table shape (type × version-glob → inject fn) is exactly the abstraction to promote: make it (agentKind × type × version-glob). Every key sequence here encodes Claude's TUI layout; codex approval prompts use y/n or arrow menus with different defaults. tmuxSend + locking + OTEL spans are agent-agnostic.

### chat-message-injection (tui-injection, agent-agnostic)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/broadcast/broadcast.go:1332-1453
- Chat Channel (chat-session:v1): chat.message frames are delivered to the agent by literal `tmux send-keys -l -- <text>` + 100ms + Enter into the main pane, under the shared pane lock. Assumes the agent's REPL input line is focused and submits on Enter.
- **Portability:** Nominally agent-agnostic (any REPL TUI), but implicitly assumes Claude-like input behavior (no vi-mode, no multiline confirm). Adapter may need a SubmitSequence override (e.g. codex uses Enter too; pi may need Ctrl+Enter).

### keyboard-relay (agnostic-plumbing, agent-agnostic)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/broadcast/broadcast.go:1235-1330
- Viewer keyboard input relay: PIN-hash (sha256 of VIBECAST_KEYBOARD_PIN) validated messages {type:input|special-key,data,key,paneId,pinHash} → tmux send-keys (literal or allow-listed named keys). Fully agent-agnostic.

### terminal-relay-core (agnostic-plumbing, agent-agnostic)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/broadcast/broadcast.go:138-287,1130-1272
- ConnectBroadcast retry loop: ttyd WS (protocol 'tty', token fetch, init with TIOCGWINSZ size), server broadcaster WS (sessionId/broadcastId/paneId/token query), dims polling via tmux display-message #{pane_width/height}, 0x30 binary stdout relay, pane-death/pane-lost OTEL reporting (pane_dead probe, list-windows diagnostics), metaCh drain. All agent-agnostic; the only Claude bits inside are the detection blocks inventoried separately.

### tmux-ttyd-orchestration (agnostic-plumbing, agent-agnostic)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/stream/stream.go:455-741,735-1087
- StartStream/SpawnPane/ResumeStream/StopStream: dedicated tmux session vibecast-<sessionId> pinned to VIBECAST_PANE_COLS×ROWS (default 150x50), per-pane grouped ttyd sessions on the same tmux socket (TMUX stripped so attach works), remain-on-exit on, branded status bar, F-key bindings via control socket curl (BindFKeys), fkeybar info/help windows, viewer URL building, session-event start/end, 10s stop grace for metadata flush, ttyd/tmux teardown. Agent-agnostic except that pane 0's command is the Claude launch string.
- **Portability:** This is the Operator core to keep. The only seam is windowCmd (agent launch) + resume.

### env-propagation-claude-telemetry (env-vars, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/stream/stream.go:800-886,953-997
- StartStream propagates env into the tmux session for the agent to inherit: OTEL_* signal-specific endpoint/protocol vars 'required by Claude Code in addition to the generic OTEL_EXPORTER_OTLP_ENDPOINT', plus CLAUDE_CODE_ENABLE_TELEMETRY and CLAUDE_CODE_ENHANCED_TELEMETRY_BETA (804-825); appends vibecast.stream_id=<id> to OTEL_RESOURCE_ATTRIBUTES so agent telemetry correlates to the stream (826-837); also propagates VIBECAST_*, AGENTICS_* (JOB_ID/TOKEN/REPO_TOKEN/BASE_URL/OWNER/PROJECT_NAME/JOB_MODE/AUTO_GIT/COMMIT_MESSAGE_HINT/PROXY_*), STAGE_GIT_*, TRACEPARENT, VIBECAST_BIN (so plugin hooks find the binary). Server-returned env from session-event is injected too, with localhost→serverHost rewrite and runner-proxy OTEL endpoint precedence.
- **Portability:** Only the two CLAUDE_CODE_* names are Claude's; codex telemetry uses `codex -c otel.*` config not env; pi differs. Adapter contributes AgentTelemetryEnv(). Everything else in this block is agnostic.

### mcp-config-injection (mcp, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/mcp/mcp.go:1-113
- InjectMCPConfig/InjectPluginMCP write/merge `.mcp.json` in the CWD (Claude's project-scoped MCP config format: mcpServers.<name>.{command,args,env}) registering `vibecast mcp serve` (+ per-plugin entries with server-provided env, called from StartStream stream.go:990-996). RemoveMCPConfig deletes the entry on shutdown (cmd/root.go:188). The shipped claude-plugin/.mcp.json also registers the server via ${CLAUDE_PLUGIN_ROOT}.
- **Portability:** The vibecast MCP server itself is standard stdio JSON-RPC (agent-agnostic); only the REGISTRATION format/location is Claude's. Codex: ~/.codex/config.toml [mcp_servers.*] or `codex mcp add`; pi: its config. Adapter: McpRegistrar.Install/Remove.

### mcp-server-tools (mcp, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/mcp/serve.go:141-983
- `vibecast mcp serve` stdio MCP server (protocol 2024-11-05). Tools: restart_claude (name + description 'Restart Claude Code…'), get_broadcast_status, stop_broadcast (conclusion enum success|failure|cancelled; auto-git push with AGENTICS_REPO_TOKEN, refuses while `.claude/`-excluded worktree dirty, refuses while pane shows '\d+ local agents?' (485-493 — Claude status-bar sniff), captures gitCommit/gitBranch, uploads workspace zip, then POSTs control /stop-broadcast), chat_reply (→ control /chat-reply → chat.reply WS frame), share_image/share_media (metadata image_share/media_share + /image-queued control call or media upload), list_sessions/select_session, change_broadcast_url, configure_otel, debug_env. Prompts restart/status/stop map to the tools.
- **Portability:** Protocol + most tools are agent-neutral. Claude-couplings: tool name restart_claude, '.claude/' exclusion in the dirty check (each agent has its own config-dir junk to exclude), the 'local agents' pane sniff, and prose in descriptions. These are exactly the agent's 'conclusion reporting' contract with the Runner — must remain stable across agents since ALP relies on stop_broadcast.

### stop-conclusion-pipeline (completion, agent-agnostic)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/control/control.go:92-125
- Control socket /stop-broadcast accepts {message, conclusion, gitCommit, gitBranch, gitPushError} → ControlStopMsg → TUI (tui/update.go:237-243) → StopStream → POST /api/lives/session-event event=end with those fields + jobId. Also invocable via `vibecast stop-broadcast --message --conclusion` (cmd/stopbroadcast.go) and auto-invoked by the Stop-hook fallback (conclusion=incomplete). This is how the Runner/server learn the job's outcome.
- **Portability:** Already agent-neutral; keep as the canonical conclusion channel for all agents.

### control-socket (agnostic-plumbing, agent-agnostic)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/control/control.go:36-467
- Unix socket ~/.vibecast/control.sock HTTP server the Runner/MCP/fkeybar talk to: /restart-claude, /status (sessionId, broadcastId, url, pinCode, viewers, uptime, phase, pendingImages), /stop-broadcast, /chat-reply, /change-server, /image-queued, /configure-otel, /fkey, /events (SSE), /panes, /start-stream. Agnostic except the /restart-claude route name and claudeSessionID plumbed through RestartFunc.

### session-store (agnostic-plumbing, agent-agnostic)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/session/session.go:17-207
- ~/.vibecast (VIBECAST_HOME override) session files: write/read/delete, FindSessionByWorkspace (used by every hook to map cwd→stream), FindActiveSession(s) by live PID, CleanStaleSessions, ResolveTargetSession for MCP. Agnostic except the ClaudeSessionID fields in the schema and ScanClaudeSessions (inventoried separately).

### sync-command (transcript, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/cmd/sync.go:22-233
- `vibecast sync --session-id <stream>` discovers Claude session jsonl files under ~/.claude/projects/<encoded-cwd>/ (resolveSessionPath 105-127) and uploads raw lines in 200-line chunks to POST /api/lives/sync {sessionId, lines[]} with Bearer auth; server returns per-record-type counts.
- **Portability:** Transport is agnostic; discovery path + the fact the server parses Claude-format lines are not. Ties into the same TranscriptReader/SessionStore adapter as transcript-discovery.

### runner-cli-contract (env-vars, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/cmd/root.go:64-102
- CLI/env contract with the Runner: flags --session-id/--broadcast-id/--resume/--attr/--plugin; env SESSION_ID, BROADCAST_ID, VIBECAST_RESUME_SESSION_ID (prior CLAUDE session UUID for --resume). Plus the documented Runner env table in docs/alp-operator.md (VIBECAST_APPEND_SYSTEM_PROMPT, VIBECAST_EXTRA_PLUGINS, CLAUDE_VERSION, CLAUDE_AUTO_UPDATE_DISABLED, INITIAL_PROMPT, AGENTICS_*).
- **Portability:** VIBECAST_RESUME_SESSION_ID carries a Claude UUID today; make it opaque agent-session-id + add AGENT_KIND selector env (e.g. VIBECAST_AGENT=claude|codex|pi) that picks the adapter. pks-cli Runner launch scripts must be updated in lockstep.

### ui-copy-claude-naming (cosmetic, claude-specific)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/fkeybar/bar.go:83-84,376-410,480,666,700,906-940
- fkeybar and TUI copy hardcode Claude: 'Launch tmux + Claude', 'Pick a previous Claude session', 'Starting Claude Code', pane labels 'Claude — <name>', 'Sessions are stored in ~/.claude/projects/', help screen 'Claude Code' section with /help //model shortcuts, 'Restart Claude in current Coding Agent'. Same strings in internal/tui/views.go:78-79,129,166,220 and model.go:47-55 (ClaudeSessions, ClaudeResumeID, ClaudeSessionID fields), types.go:64-70 ClaudeSessionInfo, types.go:125 RestartClaude callback, types.go:204 ClaudeRestartedMsg.
- **Portability:** Pure naming/copy; replace with adapter-provided DisplayName() and generic field names during the refactor.

### otel-init (agnostic-plumbing, agent-agnostic)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/telemetry/telemetry.go:20-149
- OTEL tracer init from OTEL_EXPORTER_OTLP_ENDPOINT, dynamic ConfigureOTEL, traceparent inject/extract (TRACEPARENT env → hook child spans). Agent-agnostic. PluginDir() (151-164) is the one Claude-coupled function here (locates claude-plugin next to the binary).

### auth-and-server-host (agnostic-plumbing, agent-agnostic)

- **File:** /workspaces/agentic-live-www/external/vibecast/internal/util/util.go:67-130
- GetServerHost (AGENTICS_SERVER/AGENTIC_SERVER/agentics.dk), viewer/join URL building, project owner/name from AGENTICS_PROJECT, session-id generation/extraction; internal/auth (Keycloak device-flow login, token refresh) — all agent-agnostic relay/identity plumbing.

## Event schema notes

ALL SERVER-BOUND EVENTS, FIELD BY FIELD (file paths relative to /workspaces/agentic-live-www/external/vibecast).

TRANSPORT A — POST {scheme}://{serverHost}/api/lives/metadata (built in internal/hooks/hooks.go:348-391 HookPostMetadata; Content-Type application/json; optional Authorization: Bearer <keycloak token>; scheme http iff localhost). Every payload has: sessionId (vibecast 8-char stream id, NOT the agent's), type:"metadata", subtype:<string>, timestamp (Unix SECONDS except url_detected, see #14). Subtypes:

1. "prompt" (hooks.go:404-420, from Claude UserPromptSubmit hook): {sessionId, type, subtype:"prompt", prompt:<string>, timestamp} + optional transcriptLines:[<raw Claude transcript JSONL objects>], usage:{input_tokens,output_tokens,cache_read_input_tokens,cache_creation_input_tokens} (ints, summed over increment; hooks.go:304-345).

2. "session_start" (hooks.go:436-454, from SessionStart hook): {sessionId, type, subtype, source:<Claude hook "source": startup|resume|clear|compact>, claudeSessionId:<Claude UUID from envelope session_id>, timestamp} + optional sessionSummary:<first user prompt text from transcript> (hooks.go:445-447), transcriptLines.

3. "assistant_response" — two emitters: (a) streaming per assistant text block (hooks.go:233-244 postAssistantTextBlocks, fired from any hook that consumes a transcript increment): {sessionId, type, subtype, text, timestamp} + optional usage; skips isSidechain lines. (b) Stop hook final (hooks.go:902-920): {sessionId, type, subtype, timestamp} + optional text (all assistant text blocks joined with \n, hooks.go:1328-1357), usage, transcriptLines.

4. "plan" (hooks.go:626-634, PreToolUse of tool_name==ExitPlanMode): {sessionId, type, subtype, planMarkdown:<tool_input.plan>, timestamp}.

5. "tool_use" (hooks.go:643-660, PreToolUse async): {sessionId, type, subtype, toolName, toolInput:<parsed tool_input JSON>, toolUseId, claudeSessionId, transcriptPath, timestamp} + optional transcriptLines.

6. "tool_use_end" (hooks.go:702-725, PostToolUse): {sessionId, type, subtype, toolName, toolInput, toolResponse:<parsed JSON, or raw string truncated to 2000 chars (hooks.go:681-692)>, toolUseId, claudeSessionId, transcriptPath, timestamp} + optional usage, transcriptLines.

7. "subagent_start" (hooks.go:757-776): {sessionId, type, subtype, agentId, agentType, prompt:<tool_input.prompt>, description:<tool_input.description>, subagentType:<tool_input.subagent_type>, promptSuffix:<SUBAGENT_PROMPT_SUFFIX(_FILE) env>, transcriptPath, timestamp} + optional transcriptLines.

8. "subagent_stop" (hooks.go:806-830): {sessionId, type, subtype, agentId, agentType, transcriptPath, agentTranscriptPath, prompt:<subagent's first user prompt>, timestamp} + optional transcriptLines, agentTranscriptLines (increment of the agent's own transcript), toolUseIds:[<ids of tool_use blocks in agent transcript>] (hooks.go:1297-1326).

9. "task_created" (hooks.go:848-861): {sessionId, type, subtype, taskId, taskSubject, taskDescription, teammateName, teamName, timestamp}.

10. "task_completed" (hooks.go:878-890): {sessionId, type, subtype, taskId, taskSubject, teammateName, teamName, timestamp}.

11. "permission_request" (hooks.go:1069-1085): {sessionId, type, subtype, toolName, toolInput, toolUseId (Claude's tool_use_id, or synthetic "perm-<streamId>-<unixMillis>" when absent, hooks.go:1048-1052), question:<"Tool(file_path|path|command)" label, hooks.go:1023-1031>, timestamp} + optional permissionSuggestions:<passthrough of Claude's permission_suggestions>. After POST, the hook BLOCKS and polls GET /api/lives/question-vote?sessionId=..&toolUseId=.. every 2s for 31s (hooks.go:1091-1190); response shape {resolvedAnswer:string|null, voteDeadline:int64}. "deny" → stdout {"decision":"deny","reason":...} exit 1; anything else/timeout → exit 0 (allow).

12. "pre_compact" (hooks.go:1222-1237): {sessionId, type, subtype, trigger:"auto"|"manual", timestamp} + optional customInstructions, transcriptLines.

13. "post_compact" (hooks.go:1256-1274): {sessionId, type, subtype, timestamp} + optional summary (Claude's compact summary truncated to 500 chars + "…"), transcriptLines.

14. "url_detected" (internal/broadcast/broadcast.go:107-128, from scanning ttyd stdout): {type, subtype:"url_detected", sessionId, url, context:"claude-login"|"" (classifyURL broadcast.go:98-105 matches claude.ai / auth.anthropic), timestamp:<Unix MILLISECONDS — inconsistent with all other subtypes>}. No auth header (plain http.Post).

15. "alp_pane" (broadcast.go:582-601 helper; theme picker 604-619, login-method picker 623-635, session-too-large menu 690-728): {sessionId, type, subtype:"alp_pane", paneQuestionId:"alp-pane-<sha256(seed)[:8] hex>", question:<string>, options:[<strings matching Claude's on-screen menu order>], timestamp}. Server stores as pendingQuestion; answer comes back via pending-answer poll (Transport D).

16. "onboarding_external" (broadcast.go:654-682, OAuth device-code gate): {sessionId, type, subtype, questionId:"onboarding-<sha256[:8]>", question:"Sign in to Claude Code to continue", actionUrl:<extracted oauth/authorize URL>, actionLabel:"Open sign-in URL", answerLabel:"Paste the code from the browser", provider:"claude-subscription", timestamp}. Also emits stdout line "[onboarding-prompt] kind=oauth provider=claude-subscription questionId=.. url=.." parsed by the pks-cli Runner (broadcast.go:681).

17. "image_share" (internal/mcp/serve.go:727-738, share_image/share_media MCP tool, images <1MB): {sessionId, type, subtype, imageId:<8 hex>, imageData:<data:mime;base64,...>, caption, status:"pending", timestamp}.

18. "media_share" (serve.go:794-808, after PUT-less POST upload): {sessionId, type, subtype, mediaId, url, mimeType, mediaType:"image"|"video"|"document"|"other", fileName, caption, status:"pending", timestamp}.

TRANSPORT B — metadata TEXT FRAMES on the broadcaster WebSocket (metaCh drained at broadcast.go:1131-1138 onto WS /api/lives/broadcast/ws?sessionId=..&broadcastId=..&paneId=..&token=..; connection itself carries session identity so no sessionId field):
19. "stream_info" (internal/stream/stream.go:1042-1058 fresh start; 1346-1363 resume): {type:"metadata", subtype:"stream_info", owner, project, workspace, startedAt} + optional systemPrompt:<raw VIBECAST_APPEND_SYSTEM_PROMPT(_FILE) text>.
20. "capabilities" (stream.go:1061-1071; 1365-1375): {type:"metadata", subtype:"capabilities", keyboard:<bool — VIBECAST_KEYBOARD_PIN set>}.
21. "active_pane" (stream.go:455-468, NOT wrapped in metadata): {type:"active_pane", paneId}.
Also on this WS: resize dims {columns:int, rows:int} text frames (broadcast.go:275-283) and relayed binary 0x30 stdout frames.

TRANSPORT C — POST /api/lives/session-event (no Authorization header; identity via user claims):
- start, fresh (stream.go:938-950): {sessionId, broadcastId, event:"start", attributes:<map from `--attr K V` CLI flags>, user:<Keycloak token claims, optional>}. Response consumed: {ok, pin, env:{k:v}} — env (OTEL endpoints etc.) is written into the tmux session for the agent, with localhost→serverHost rewrite and OTEL_RESOURCE_ATTRIBUTES merge (stream.go:953-997).
- start, resume (stream.go:1122-1174): {sessionId, event:"start", user}. Response additionally consumed: {claudeSessionId:string|null, project, workspace, startedAt, env} — claudeSessionId is the server-side recovery path for `claude --resume`.
- end (stream.go:1410-1450, from StopStream after a 10s flush grace): {sessionId, event:"end"} + optional message, conclusion ("success"|"failure"|"cancelled"|"incomplete"), gitCommit, gitBranch, gitPushError, jobId (from AGENTICS_JOB_ID). message/conclusion originate from the stop_broadcast MCP tool (serve.go:440-613) or `vibecast stop-broadcast --message --conclusion` (cmd/stopbroadcast.go), via control-socket POST /stop-broadcast → ControlStopMsg (internal/control/control.go:92-125, tui/update.go:237-243).

TRANSPORT D — other server calls: GET /api/lives/question-vote?sessionId&toolUseId (hooks.go:1095) → {resolvedAnswer, voteDeadline}; GET /api/lives/sessions/{id}/pending-answer (broadcast.go:1018-1060, 3s poll) → {questionId, questionType:"permission"|"alp_pane"|"onboarding_external"|<tool>, options:[], answer, multiSelect, subQuestions:[{toolUseId,question,options,multiSelect,resolvedAnswer}]}; POST /_relay/snapshot {sessionId, snapshot:<ANSI capture-pane text>} (broadcast.go:353, 730-740, every 2s for 10min then 15s); PUT /api/lives/sessions/{id}/workspace-archive (zip body, Bearer AGENTICS_TOKEN; serve.go:100-118); POST /api/lives/media/upload?sessionId&mimeType&fileName → {mediaId,url,storedName} (serve.go:763-792); POST /api/lives/sync {sessionId, lines:[<raw ~/.claude jsonl lines>]} in 200-line chunks → {ok, counts} (cmd/sync.go:170-232); POST /api/lives/image-approve {sessionId, imageId, approved} (stream.go:1506-1533); WS /api/lives/chat/channel/ws?jobId=..&token=.. with frames {type:"chat.message"|"chat.reply"|"chat.end", jobId, text?, reason?} (types.go:49-54, broadcast.go:1332-1453, control.go:132-172); WS /api/lives/chat/ws viewer chat {type,username,text,sessionId,timestamp,count} (types.go:32-39).

NORMALIZATION NOTES for an agent-agnostic schema: (a) claudeSessionId appears in session_start, tool_use, tool_use_end and the resume response — rename to agentSessionId with agent kind alongside; (b) transcriptLines/agentTranscriptLines are RAW Claude-format JSONL objects (types user/assistant/tool_use/tool_result/thinking/result, message.content[] blocks, message.usage, isSidechain) — the server and viewer parse this format, so a normalized event schema must either define a neutral transcript-line shape or tag lines with a format discriminator; (c) usage field names are Anthropic API names (cache_read_input_tokens etc.) — a neutral schema needs promptTokens/completionTokens/cacheRead/cacheWrite mapping; (d) timestamp units are inconsistent (url_detected is ms, everything else s); (e) toolName values are Claude tool names (Bash, Write, Edit, ExitPlanMode, AskUserQuestion, Task/subagents) — viewer rendering keyed off them needs a tool taxonomy mapping per agent; (f) subtypes pre_compact/post_compact, subagent_*, task_* and permission_request are Claude-lifecycle-shaped; codex/pi adapters may emit none, some, or need new subtypes.

## Recommendations

1) Introduce a single AgentAdapter interface in a new internal/agent package, selected by a VIBECAST_AGENT env (claude|codex|pi, default claude), covering the five hard seams found: (a) Launch: BuildCommand/BuildResumeCommand(opts{workdir, agentSessionID, resumeID, model+tier+effort, systemPromptFile, initialPromptFile, permissionMode, extraArgs}) returning argv — keep the existing rc-wrapper/cd-prefix/tmux mechanics outside the adapter; (b) VersionManager: EnsureVersion(pin string) replacing ensureClaudeUpToDate's `claude update|install` with per-agent commands, keeping sync.Once + fail-open + span; (c) SessionIdentity: PreassignID() (Claude UUIDv4 via --session-id) vs DiscoverID() (codex rollout id from its sessions dir/event stream), plus ResumeSupport; (d) EventSource: how normalized lifecycle events are produced — Claude keeps the hook plugin + `vibecast hook`; codex needs a sidecar that tails `codex exec --json` or the rollout file (it has no blocking hooks, so sync guards must degrade to sandbox/approval policy); pi installs its own hook config; (e) TranscriptReader + SessionStore: per-agent transcript path scheme and line format → normalized lines.

2) Normalize the event schema at the vibecast→server boundary rather than per-consumer: rename claudeSessionId→agentSessionId (keep claudeSessionId as a dual-write alias during migration since www-site's store/restore depends on it), add agent:{kind,version} to session_start and session-event start, map usage token names to neutral keys, fix the url_detected ms-vs-s timestamp inconsistency, and tag transcriptLines with a format discriminator (or translate to a neutral line shape) because the viewer currently parses raw Claude JSONL.

3) Generalize the TUI-automation tables, don't rewrite them: broadcast.go already has the right shape in answerHandler{questionType, versionGlob, inject} — promote it to (agentKind, questionType, versionGlob) and add the missing twin table for DETECTION: ScreenGate{agentKind, versionGlob, matchStrings[], action} covering trust/bypass/tour/theme/login/OAuth/session-size. All capture/strip/debounce/pane-lock/OTEL machinery stays shared. Expect codex/pi to need far fewer gates (no plugin-dir trust dialog), but different auth-URL classifiers (auth.openai.com etc.) in classifyURL.

4) Keep stop_broadcast/chat_reply/get_broadcast_status as the agent-neutral Operator contract — both Codex CLI and pi support MCP stdio servers, so the vibecast MCP server ports as-is; only the registration writer (.mcp.json vs ~/.codex/config.toml [mcp_servers] vs pi config) needs an McpRegistrar per adapter. Rename restart_claude→restart_agent (keep alias). Replace the two Claude-TUI sniffs that gate stopping ('\\d+ local agents' in hooks.go:952 and serve.go:487) with an adapter BusySignal() or drop them for non-Claude agents.

5) Migration order that minimizes risk: first mechanical renames (types.SessionFile fields, RestartClaude callback, VIBECAST_CLAUDE_* → VIBECAST_AGENT_* with back-compat reads, UI copy via adapter DisplayName); then extract launch+version+resume into the Claude adapter with byte-identical output (golden tests exist: capability_test.go, autoupdate_test.go, guard_test.go give the pattern); then the ScreenGate/answer tables; last the EventSource abstraction, since codex's lack of synchronous hooks forces real design decisions (guard enforcement and stop-blocking semantics don't exist there — decide whether those become Operator-side policies, e.g. moving the Bash kill-guard into a codex sandbox config or an approval callback). Also update the Runner (pks-cli) launch-script contract (VIBECAST_RESUME_SESSION_ID, CLAUDE_VERSION) and docs/alp-operator.md in the same change, since they are the external consumers of the Claude-named env vars.

