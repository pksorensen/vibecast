# Research findings: platform-side contract (www-site + relay + runner, 2026-07-10)

> Generated from the multi-agent research workflow (session 9d7d329a, 2026-07-10). Source of truth for the design docs in this folder.

## Summary

The platform contract vibecast fulfills has four surfaces, and it is already far more agent-neutral than the naming suggests. (1) SERVER API: vibecast POSTs a normalized metadata event stream (subtypes prompt/tool_use/tool_use_end/session_start/subagent_*/plan/assistant_response/image_*/media_*/permission_request/alp_pane/onboarding_external/pre+post_compact/capabilities/dims/stream_info) to /api/lives/metadata, session lifecycle to /api/lives/session-event (start returns viewer PIN + prior claudeSessionId + a CLAUDE_CODE/OTEL env block; end deposits conclusion/git metadata for the Runner's job PATCH), raw Claude transcript JSONL to /api/lives/sync, and restore/status are read-back plumbing. (2) WS-RELAY is a pure transport: 0x30 terminal frames masked and re-wrapped as 0x50 pane frames, pane_list/broadcaster_status/terminal_resize to viewers, subtype-keyed replay buffers fed by /_relay/fanout — zero agent coupling. (3) VIEWER UI consumes the generic subtypes; its only real Claude couplings are the authoritative transcript parser (Claude JSONL + Claude Code scaffolding filters — with an already-working generic metadata fallback), ExitPlanMode/EnterPlanMode special cases, Anthropic-shaped usage fields, and vibecast's own stop_broadcast MCP tool for outcomes. (4) RUNNER DISPATCH: buildAgentDefinition resolves station/line operatorConfig (modelTier haiku|sonnet|opus, model, effort low..max, enable1mContext, claudeCredentialsScope) into the job spec; pks-cli's launch script exports AGENTICS_* + VIBECAST_* env (VIBECAST_CLAUDE_MODEL/_TIER/_EFFORT, VIBECAST_INITIAL_PROMPT_FILE, VIBECAST_APPEND_SYSTEM_PROMPT_FILE, VIBECAST_RESUME_SESSION_ID, BROADCAST_ID, CLAUDE_CODE_* telemetry toggles) and execs vibecast, which owns the mapping to `claude --model/--effort`. (5) STORAGE: claudeSessionId is a pure opaque resume token (session → task.lastJobResumeSessionId → AgentDefinition.resumeSessionId → VIBECAST_RESUME_SESSION_ID); the platform never parses it. Claude-specific leakage past vibecast is concentrated in: the claudeSessionId name, the sync/transcript JSONL parsers, the AskUserQuestion/Write tool-name switches in the metadata route, the claude_code.* OTEL cost aggregator, Claude-only model-tier/effort enums + claudeCredentialsScope in station config, and the runner's CLAUDE_CODE_* env + .claude/ workspace scaffolding.

## Integration points

### metadata-event-ingest (server-api, claude-specific)

- **File:** src/apps/www-site/src/app/api/lives/metadata/route.ts:13-923
- The single event-ingestion endpoint for all Operator metadata. Accepts the ~20-subtype union (prompt, tool_use, tool_use_end, session_start, subagent_*, task_*, plan, assistant_response, image_*, media_*, permission_request, alp_pane, onboarding_external, url_detected, pre/post_compact, capabilities, dims, stream_info), masks sensitive fields, persists to the file store, and fans out to viewers via /_relay/fanout.
- **Portability:** The subtype vocabulary itself is agent-neutral. Claude leakage: (1) claudeSessionId field on tool_use/tool_use_end/session_start persisted verbatim; (2) toolName switches on Claude Code tool names 'AskUserQuestion'/'AskFollowupQuestion' (question votes) and 'Write' + '/.claude/agents/*.md' path (agent auto-register); (3) pre_compact/post_compact lifecycle is Claude compaction; (4) chat copy 'images Claude creates' and 'Claude needs you to sign in' (context==='claude-login'). An abstraction needs: rename claudeSessionId→agentSessionId (accept both), either semantic subtypes ('question' instead of matching ask-tool names) or adapter-side tool-name mapping in vibecast, and neutral copy. The mcp__vibecast__stop_broadcast matches are vibecast's own MCP tools — already agent-agnostic.

### session-event-lifecycle (server-api, claude-specific)

- **File:** src/apps/www-site/src/app/api/lives/session-event/route.ts:43-213
- Streaming session start/end: assigns viewer PIN, registers session→broadcast, stores end-of-session completion metadata (message/conclusion/gitCommit/gitBranch/gitPushError) for the Runner's job PATCH to read. Start response returns the PRIOR claudeSessionId (CLI --resume recovery) and an OTEL env block containing CLAUDE_CODE_ENABLE_TELEMETRY=1.
- **Portability:** Lifecycle contract (start/end + completion metadata handoff to the job layer) is agent-neutral. Needs: claudeSessionId→agentSessionId in the response, and the returned env block must become per-agent (CLAUDE_CODE_ENABLE_TELEMETRY means nothing to codex/pi; OTEL_* endpoint/protocol lines are reusable). Could return env keyed by an agent hint or let vibecast own telemetry env entirely.

### restore-replay (server-api, agent-agnostic)

- **File:** src/apps/www-site/src/app/api/lives/restore/route.ts:6-216
- Rebuilds wire-format metadata messages from the store (prompts, toolUses, toolUseEnds, sessionEvents, subagentEvents, plans, assistantResponses, images, media, chat, dims, capabilities) for ws-relay to preload replay buffers on broadcaster reconnect.
- **Portability:** Fully generic — it re-emits the normalized subtypes with generic field names (it even drops claudeSessionId on re-emission). No changes needed for other agents.

### sync-claude-jsonl (server-api, claude-specific)

- **File:** src/apps/www-site/src/app/api/lives/sync/route.ts:22-151 (+ src/lib/sync-parser.ts 72-250)
- Bulk backfill endpoint: accepts up to 500 RAW Claude Code transcript JSONL lines per request; parseClaudeJSONL derives prompts/toolUses/toolUseEnds/assistantResponses/plans/sessionEvents/sessionMeta (incl. claudeSessionId, cwd→workspace, slug→project) with per-uuid dedup.
- **Portability:** Hard-coupled to Claude Code's transcript.jsonl format (uuid, message.content blocks, tool_use/tool_result, ExitPlanMode→plan). For codex/pi either: (a) vibecast adapter converts the foreign transcript to the same derived events via /metadata and skips /sync entirely (zero platform change — the metadata path is a complete substitute), or (b) add a transcriptFormat discriminator + per-format parser. Option (a) is the minimal path.

### job-patch-resume (server-api, claude-specific)

- **File:** src/apps/www-site/src/app/api/owners/[owner]/projects/[project]/runs/[runId]/jobs/[jobId]/route.ts:70-260
- Authoritative Runner job-completion endpoint. Reads the session by streamId to harvest claudeSessionId → stamps task.lastJobResumeSessionId and TaskComment.resumeSessionId so retry/continue jobs get AgentDefinition.resumeSessionId (→ VIBECAST_RESUME_SESSION_ID).
- **Portability:** The resume token is treated as opaque — never parsed. Only naming is Claude-specific (claudeSessionId/resumeSessionId chain). Rename to agentSessionId/agentResumeToken; the mechanic works for any agent that supports resume-by-id (codex has session ids; agents without resume simply never set it).

### otel-cost-pipeline (server-api, claude-specific)

- **File:** src/apps/www-site/src/lib/otel-aggregator.ts:100-180 (+ task-dispatch.ts computeCostSummary 697-739)
- Task costSummary (stamped at settle) is computed from OTLP log bodies named claude_code.user_prompt / claude_code.api_request / claude_code.tool_use / claude_code.tool_result / claude_code.api_error and the claude_code.active_time.total metric, emitted by Claude Code's built-in telemetry.
- **Portability:** Non-Claude agents emit none of these, so costSummary silently degrades to source:'none' (settle never blocks — graceful). To keep cost/usage tracking per agent: add per-agent event-name tables to the aggregator, or have adapters emit normalized usage in tool_use_end/assistant_response `usage` fields and derive cost server-side from those instead.

### relay-broadcaster-ws (relay, agent-agnostic)

- **File:** src/apps/www-site/ws-relay/index.ts:1548-1798
- Broadcaster WS contract: query params sessionId/broadcastId/paneId; binary 0x30 terminal frames (masked, re-wrapped in 0x50 pane frames per virtual paneId 'sessionId:paneId'); text frames {type:'metadata'} forwarded to /api/lives/metadata, {type:'active_pane'}, bare {columns,rows} dims. On last-pane close it POSTs session-event 'end' and notifies viewers.
- **Portability:** Pure terminal + JSON-envelope transport — nothing Claude-specific. Any agent wrapped by vibecast's tmux/ttyd pipeline (or any broadcaster speaking this frame protocol) works unchanged.

### relay-fanout-buffers (relay, agent-agnostic)

- **File:** src/apps/www-site/ws-relay/index.ts:2016-2148 (fanout), 1804-1940 (viewer replay), 1442-1528 (restore)
- /_relay/fanout parses each metadata message, injects savedAt, and files it into subtype-keyed replay buffers (prompt≤20, tool_use≤100, tool_use_end≤100, session_start≤20, subagent≤50, plan≤20, assistant_response≤50, image/media≤50). Viewer connect replays broadcaster_status, pane_list, dims, then all events savedAt-sorted.
- **Portability:** The buffer switch enumerates the normalized subtypes with generic names — a new agent emitting the same subtypes gets replay for free. Only a brand-new subtype would need a buffer entry (unknown subtypes still fan out live, they just aren't replayed to late joiners beyond latest-per-subtype).

### viewer-transcript-parser (viewer-ui, claude-specific)

- **File:** src/apps/www-site/src/lib/conversation.ts:98-222
- buildConversationFromTranscript — the viewer's AUTHORITATIVE conversation path — walks raw Claude Code transcript.jsonl (isSidechain, message.role, content blocks text/tool_use/tool_result, Anthropic usage) and filters Claude Code scaffolding (<system-reminder, <command-, Caveat:, skill-launch text) out of user beats.
- **Portability:** Format-coupled to Claude JSONL AND to Claude Code's injected-text conventions. The fallback path buildConversationFromMetadata (lines 226-305) builds the identical beat shape from the generic metadata stream — a non-Claude agent that never uploads transcriptLines renders correctly via the fallback with zero changes. Full parity needs either per-format transcript parsers or adapter-side conversion to a normalized transcript.

### viewer-metadata-processing (viewer-ui, claude-specific)

- **File:** src/apps/www-site/src/lib/activity-log-utils.ts:154-414 (+ app/live/[broadcastId]/components/LiveViewer.tsx subtype switch ~2300-2400)
- Activity-log/metadata processors switch on the generic subtypes. Tool-name assumptions: ExitPlanMode/EnterPlanMode render instant-complete and drive 'Implementing: <plan>' session labeling; session outcome extraction is keyed on mcp__vibecast__stop_broadcast / mcp__plugin_vibecast_vibecast__stop_broadcast toolInput.{conclusion,message}; TokenUsage rollups use Anthropic field names (input_tokens, cache_read/creation_input_tokens).
- **Portability:** Mostly neutral. ExitPlanMode/EnterPlanMode special-cases are harmless no-ops for agents that never emit them (adapters can emit the 'plan' subtype directly). stop_broadcast outcome extraction is vibecast-tool-based, so it already works for any agent running under vibecast's MCP server. Usage shape: adapters should normalize their token counts into the Anthropic-shaped usage object — cheaper than changing every consumer. No hardcoded 'Claude' strings in the activity feed itself (only font/comment references to Claude Code TUI glyphs in LiveViewer.tsx).

### dispatch-agent-definition (runner-dispatch, claude-specific)

- **File:** src/apps/www-site/src/lib/agent-definition.ts:223-495 (+ agentics-store.ts AgentDefinition 85-205)
- buildAgentDefinition assembles the job spec the Runner claims: repository/branch/prompt/appendSystemPrompt (interpolated templates), plugins (cloned as Claude --plugin-dir), agents (pre-installed into .claude/agents/), skillIds, claudeCredentialsScope ('task'|'project'|'runner' Docker volume, ADR 0004), operatorConfig { modelTier: haiku|sonnet|opus, model, effort: low..max, enable1mContext, autoApproveImageUploads, disableBackgroundTasks }, devcontainer template/files, needs[] capabilities, broadcastId, resumeSessionId. Delivery/fan-out prompt suffixes reference mcp__vibecast__* tools and $AGENTICS_* env.
- **Portability:** This is THE place an `agent` choice must land. Claude leakage: claudeCredentialsScope (per-agent credential volumes needed), modelTier/effort enums are Claude Code's aliases/levels (VALID_TIERS/VALID_EFFORTS validation at lines 468-475), plugins/agents/skills are Claude Code plugin-system concepts, prompt suffixes hardcode Write-tool + mcp__vibecast__ tool names (the latter is fine — vibecast-owned). Abstraction: add operatorConfig.agent (or top-level agentRuntime), make tier/effort validation per-agent (or free-form, dropped by vibecast like today), generalize credentialsScope to agentCredentialsScope keyed by agent.

### station-config-model-plumbing (runner-dispatch, claude-specific)

- **File:** src/apps/www-site/src/lib/store.ts:921-961 (OperatorConfig/KanbanColumn) + AssemblyLineData.settings
- Where per-station/per-line agent capability config lives today: KanbanColumn.operatorConfig { modelTier 'haiku'|'sonnet'|'opus', model (exact id), effort 'low'..'max', enable1mContext, disableBackgroundTasks, autoApproveImageUploads } with assembly-line settings fallbacks (settings.modelTier/effort/enable1mContext/claudeCredentialsScope).
- **Portability:** The natural home for `agent?: 'claude'|'codex'|'pi'` — station override → line settings default → 'claude' fallback, exactly mirroring how modelTier/effort resolve today. Type unions for tiers/efforts would need widening (or free-string + per-agent catalogs) since codex/pi have different model/effort vocabularies. Everything else here (autoApprove, timeouts) is agent-neutral.

### dispatch-ceremony-needs (runner-dispatch, agent-agnostic)

- **File:** src/apps/www-site/src/lib/task-dispatch.ts:253-433, 596-688
- dispatchStationJob — the agent-agnostic station-dispatch ceremony (join hooks, mock virtualization, createRun). The `needs` capability-string mechanism (chat-session:v1, chat-llm:v1) gates jobs to Runners declaring a capability, with no new matching logic required per capability.
- **Portability:** Nothing Claude-specific. The existing needs mechanism is the ready-made vehicle for agent selection at the Runner level: dispatch with needs:['agent-runtime:codex'] so only Runners with codex installed/authenticated claim the job — zero changes to findQueuedJobs/claimJob.

### runner-launch-script (runner-dispatch, claude-specific)

- **File:** external/pks-cli/src/Commands/Agentics/Runner/AgenticsRunnerStartCommand.cs:1298-1560 (spawn mode) + 3390-3680 (in-process mode)
- The Runner builds start.sh: exports AGENTICS_SERVER/AGENTIC_SERVER, AGENTICS_PROJECT/OWNER/PROJECT_NAME/JOB_ID/TOKEN/BASE_URL, AGENTICS_JOB_MODE=1, AGENTICS_CHAT_SESSION, VIBECAST_HOME/KEYBOARD_PIN/DEBUG/BIN, VIBECAST_INITIAL_PROMPT_FILE, VIBECAST_APPEND_SYSTEM_PROMPT_FILE, VIBECAST_RESUME_SESSION_ID, VIBECAST_EXTRA_PLUGINS, VIBECAST_AUTO_APPROVE_IMAGES, VIBECAST_CLAUDE_MODEL / VIBECAST_CLAUDE_MODEL_TIER / VIBECAST_CLAUDE_EFFORT, VIBECAST_CLAUDE_CHANNEL, BROADCAST_ID, STAGE_GIT_URL/TOKEN/DIR, AGENTICS_AUTO_GIT/COMMIT_MESSAGE_HINT/REPO_TOKEN, TRACEPARENT, OTEL_* + CLAUDE_CODE_ENABLE_TELEMETRY/CLAUDE_CODE_ENHANCED_TELEMETRY_BETA/CLAUDE_CODE_DISABLE_BACKGROUND_TASKS (and CLAUDE_CODE_DISABLE_1M_CONTEXT), writes .claude/settings.local.json (permissions + enableAllProjectMcpServers), workspace CLAUDE.md, .mcp.json (agent-share channel), then `exec ${VIBECAST_BIN:-npx --yes vibecast}`.
- **Portability:** The env-var boundary is the right seam: the Runner deliberately only passes resolved values through and 'vibecast owns the mapping to claude --model/--effort' (comment at 1470-1472; confirmed in vibecast internal/stream/stream.go buildModelFlag/buildEffortFlag). For multi-agent: rename VIBECAST_CLAUDE_MODEL* → VIBECAST_MODEL*/VIBECAST_AGENT (vibecast maps per agent), and make the Claude-only bits conditional per agent: CLAUDE_CODE_* env, .claude/settings.local.json + CLAUDE.md + .claude/agents plugin dir injection, claude credentials volume, and Claude first-run-gate detectors. Everything AGENTICS_*, BROADCAST_ID, prompt files, STAGE_GIT_*, OTEL endpoint plumbing is agent-neutral and stays as-is.

### vibecast-agent-boundary (runner-dispatch, claude-specific)

- **File:** external/vibecast/internal/stream/stream.go:189-226 (+ cmd/root.go, internal/broadcast/broadcast.go)
- vibecast consumes the runner env (VIBECAST_CLAUDE_MODEL/_TIER/_EFFORT → `claude --model/--effort` flags with drop-unknown validation; BROADCAST_ID; VIBECAST_RESUME_SESSION_ID; prompt files) and sets VIBECAST_SESSION_ID into the tmux session env. The Operator-owns-the-agent-CLI principle is explicit here.
- **Portability:** This is where the multi-agent abstraction should live per the existing design: vibecast reads an agent selector (new VIBECAST_AGENT env) and builds the codex/pi/claude invocation + hook adapters internally, translating each agent's events into the platform's normalized metadata subtypes. The platform contract upstream of this file already tolerates it.

### storage-claude-session-id (storage, claude-specific)

- **File:** src/apps/www-site/src/lib/store.ts:295-324 (SessionData), 553/565/611 (ToolUseData/ToolUseEndData/SessionEventData), 1192 (TaskData.lastJobResumeSessionId), 1300 (TaskComment.resumeSessionId)
- claudeSessionId persisted on sessions, tool-use records, and session events; propagated to TaskData.lastJobResumeSessionId and TaskComment.resumeSessionId, then back out via AgentDefinition.resumeSessionId → VIBECAST_RESUME_SESSION_ID. The platform NEVER parses its format — it is an opaque resume token stored, echoed on session-event start, and threaded to retry/continue jobs.
- **Portability:** Naming-only coupling. Rename to agentSessionId (with read-time alias for existing JSON files — the file store has years of records under the old key) and the whole resume chain works for any agent with resumable sessions. Agents without resume just never send it; the chain already tolerates null everywhere.

### storage-raw-transcript (storage, claude-specific)

- **File:** src/apps/www-site/src/lib/store.ts:appendTranscriptLines / transcript.jsonl per session
- transcriptLines/agentTranscriptLines arriving on metadata events (and via /sync) are persisted verbatim as Claude Code JSONL; consumed later by conversation.ts buildConversationFromTranscript and the session detail pages.
- **Portability:** Storage itself is schema-less (verbatim lines) — the coupling is in producers/consumers. Minimal abstraction: stamp a transcriptFormat on the session (default 'claude-jsonl') so consumers can pick a parser or fall back to the metadata-derived conversation; or require adapters to upload transcripts pre-converted to Claude-JSONL-compatible shape (what a codex adapter could do inside vibecast).

### viewer-question-vote-loop (server-api, claude-specific)

- **File:** src/apps/www-site/src/app/api/lives/question-vote + metadata route 155-296, 698-826
- The interactive human-in-the-loop channel: question/permission/alp_pane/onboarding_external events become QuestionVote records + chat vote cards; vibecast/the runner polls GET /question-vote (and pending-answer) for resolvedAnswer and injects the answer/decision back into the agent pane or hook exit code.
- **Portability:** The vote/poll/inject protocol is agent-neutral (alp_pane + onboarding_external were built exactly for arbitrary interactive CLI prompts). Claude specifics: permission_request semantics assume Claude Code PermissionRequest hooks ({"decision":"deny"} exit contract lives in vibecast) and question events assume AskUserQuestion input shape (questions[]/options[{label,description}]/multiSelect). A codex/pi adapter must map its ask/approval mechanism into these shapes — doable entirely inside vibecast; no platform change.

## Event schema notes

TRANSPORT: vibecast POSTs JSON to /api/lives/metadata (or sends {type:'metadata',...} text frames over the broadcaster WS, which ws-relay forwards verbatim to that route adding sessionId/broadcastId/paneId). Envelope fields on every event: sessionId (REQUIRED — the vibecast stream/session id, NOT the agent's own session id; 400 if missing), broadcastId (optional; else looked up via getSession(sessionId).broadcastId), subtype (string; 'unknown' if absent), timestamp (epoch SECONDS, optional), savedAt (epoch ms, injected server/relay-side for replay ordering), paneId (relay-added). Unless subtype is in the skip list (tool_use_end, assistant_response, permission_request, alp_pane, onboarding_external, pre_compact, post_compact, dims) the route 404s when no broadcaster is connected. maskText() is applied to prompt, toolInput, toolResponse, planMarkdown, assistant text, transcriptLines. Every event is fanned out to viewers verbatim via /_relay/fanout after persistence.

SUBTYPE CATALOG (field-by-field, as accepted today):
- prompt: { prompt: string, usage?: TokenUsage } → prompts.jsonl + session.latestPrompt. Counts as activity.
- tool_use: { toolName: string, toolInput?: object, toolUseId?: string, claudeSessionId?: string, transcriptPath?: string }. Server switches on toolName: 'AskUserQuestion'|'AskFollowupQuestion' → pendingQuestion + vote cards (toolInput.question | toolInput.questions[{question, header?, options[{label,description}], multiSelect?}], toolInput.options).
- tool_use_end: same fields + toolResponse?: string|object, usage?. Server switches on toolName: 'Write' with toolInput.file_path matching /.claude/agents/*.md → agent auto-register (gray-matter frontmatter: name, description, teamId, skills); 'mcp__vibecast__stop_broadcast'|'mcp__plugin_vibecast_vibecast__stop_broadcast' → orphan-agent reconciliation (and viewer-side outcome extraction from toolInput.{conclusion,message}).
- session_start: { source?: string ('startup'|'clear'|'resume'|'sync'|...), claudeSessionId?: string, sessionSummary?: string } → saves claudeSessionId on the session for --resume.
- subagent_start / subagent_stop: { agentId: string, agentType?: string, transcriptPath?, agentTranscriptPath? (stop), toolUseIds?: string[] (stop), prompt? (stop) } → in/decrements active-subagent count (idle-timeout adaptation).
- task_created / task_completed: { taskId, taskSubject?, taskDescription? (created), teammateName?, teamName? } (Claude Code teammate/Task-tool concept).
- plan: { planMarkdown: string }.
- assistant_response: { text: string, usage?: TokenUsage }.
- image_share: { imageId, imageData (base64 data), caption?, status? } (+ station operatorConfig.autoApproveImageUploads short-circuit); image_approved / image_rejected: { imageId, caption? }.
- media_share: { mediaId, url, mimeType, mediaType? ('other' default), fileName?, caption? }; media_approved / media_rejected same keys.
- permission_request: { question? (falls back to toolName), toolUseId? (synthetic perm-{sessionId}-{ts} if absent — Claude Code PermissionRequest hooks omit it) } → Allow/Deny vote; server regexes question for ^(Write|Edit)\((path)\)$ to extract filePath.
- alp_pane: { paneQuestionId, question, options: string[] } — interactive-terminal-prompt vote (runner injects resolved option number).
- onboarding_external: { questionId, question, actionUrl?, actionLabel?, answerLabel?, provider? } — OAuth/paste-back dialogs, 15-min deadline.
- url_detected: { url, context? } — context === 'claude-login' renders 'Claude needs you to sign in'.
- pre_compact / post_compact: no extra fields — sets/clears a 5-min compact deadline (Claude compaction lifecycle) + Vibe chat notices.
- capabilities: whole msg persisted on session (keyboard support etc.).
- dims: { dims: { columns, rows } } (also derivable from bare {columns, rows} text frames on the broadcaster WS).
- stream_info: { owner? ('default'), project, workspace?, taskId?, assemblyLineId?, systemPrompt? } → session+project linkage.
- SIDE ARRAYS on any event: transcriptLines?: object[] and agentTranscriptLines?: object[] — RAW Claude Code transcript JSONL lines, masked and persisted verbatim to transcript.jsonl (the viewer's authoritative conversation source).

TokenUsage everywhere = { input_tokens?, output_tokens?, cache_read_input_tokens?, cache_creation_input_tokens? } — Anthropic's usage shape, consumed by the viewer's token rollups.

SESSION-EVENT (POST /api/lives/session-event): { sessionId, broadcastId?, event: 'start'|'end', user?: {sub, preferred_username, email, picture}, attributes?: Record<string,string> }. 'start' response: { ok, pin, claudeSessionId (prior value or null — CLI --resume recovery), project, workspace, startedAt, env: { CLAUDE_CODE_ENABLE_TELEMETRY:'1', OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_EXPORTER_OTLP_PROTOCOL:'http/json', OTEL_RESOURCE_ATTRIBUTES:'vibecast.session_id=...', OTEL_METRICS/LOGS_EXPORTER, intervals } (+ VIBEGAME_* when attributes['game-id']) }. 'end' body: { message?, conclusion?, gitCommit?, gitBranch?, gitPushError? } — stored on the session for the Runner's PATCH /jobs/[jobId] to read (completionMessage/conclusion/git info); Runner PATCH body is { status: 'in_progress'|'completed', conclusion?, completionReason?, logs?, sessionId|streamId }.

SYNC (POST /api/lives/sync): { sessionId, lines: string[] (≤500 raw Claude Code JSONL lines) } — parseClaudeJSONL expects Claude's format exactly: per-line uuid, type ('user'|'assistant'), sessionId (→ claudeSessionId), timestamp (ISO), cwd, slug, message.role, message.content as string or blocks {type:'text'|'tool_use'|'tool_result', name, id, input, tool_use_id, content}, message.usage; ExitPlanMode tool_use → plan record.

RESTORE (GET /api/lives/restore?sessionId=): returns wire-ready JSON-string arrays (streamInfo, prompts[≤20], toolUses[≤100], toolUseEnds[≤100], sessionEvents, subagentEvents[≤50], plans, assistantResponses, images, media, chat[≤200], dims, capabilities) which ws-relay preloads into per-pane replay buffers on broadcaster reconnect; viewers get them replayed savedAt-sorted on connect.

BROADCASTER WS (/api/lives/broadcast/ws?sessionId=&broadcastId=&paneId=): binary frames — 0x30 = terminal output (masked, re-wrapped as 0x50 pane frame [0x50][len][sessionId:paneId][payload] for viewers); text frames — {type:'metadata'} (forwarded to Next), {type:'active_pane', paneId}, bare {columns, rows} (dims). Relay→viewer messages: pane_list {panes:[{paneId:'sessionId:paneId', name, active, connectedAt}]}, broadcaster_status {connected}, terminal_resize {paneId, columns, rows}; viewer→relay: {type:'input'|'special-key', paneId} forwarded to the broadcaster for PIN validation. Relay closes the loop by POSTing session-event 'end' on last-pane disconnect. This entire layer is agent-agnostic (any tmux/ttyd-wrapped process works).

## Recommendations

MINIMAL PLATFORM CHANGES (everything else can stay vibecast-internal):

1. Agent selection plumbing (the one genuinely new platform feature): add `agent?: 'claude' | 'codex' | 'pi'` to OperatorConfig (store.ts, station level) and AssemblyLineData.settings (line default), resolve in buildAgentDefinition exactly like modelTier/effort resolve today (station → line → default 'claude') into AgentDefinition.operatorConfig.agent. The Runner passes it through as one new env var (VIBECAST_AGENT=codex) — mirroring the existing 'runner passes resolved values, vibecast owns the CLI mapping' principle stated in AgenticsRunnerStartCommand.cs:1470 and implemented in vibecast stream.go buildModelFlag. Optionally also stamp needs:['agent-runtime:codex'] at dispatch so only Runners with that agent installed claim the job — the needs mechanism (chat-session:v1 precedent) needs zero new matching logic. UI: one dropdown in the station inspector next to the existing model-tier picker.

2. Rename-level generalizations (mechanical, back-compat aliased): claudeSessionId → agentSessionId across metadata/session-event/sync ingestion, SessionData, TaskData.lastJobResumeSessionId, jobs PATCH — the platform already treats it as an opaque resume token, so this is naming only; keep reading the old key from existing JSON files. Likewise claudeCredentialsScope → agentCredentialsScope, and VIBECAST_CLAUDE_MODEL/_TIER/_EFFORT → VIBECAST_MODEL/_MODEL_TIER/_EFFORT (vibecast interprets per agent; keep old names as fallbacks). Widen the modelTier/effort enums to free strings validated per agent — vibecast already has drop-unknown behavior, so server-side validation can simply relax.

3. Per-agent runner script sections: gate CLAUDE_CODE_* env exports, .claude/settings.local.json, workspace CLAUDE.md, .claude/agents plugin injection, and the claude credentials volume behind agent==='claude' in AgenticsRunnerStartCommand; add codex/pi equivalents there (their config files, their credentials volume). The AGENTICS_*, BROADCAST_ID, prompt-file, STAGE_GIT_*, OTEL-endpoint plumbing is already agent-neutral.

4. Optional (graceful-degradation today, small platform change for parity): (a) OTEL cost aggregator — codex/pi don't emit claude_code.* log bodies, so task.costSummary degrades to source:'none' without breaking settle; add per-agent event-name maps or derive cost from the metadata usage fields when adapters supply them. (b) session-event's returned env block — make the telemetry env per-agent or drop it and let vibecast own telemetry env entirely. (c) Neutral copy: 'images Claude creates' / 'Claude needs you to sign in' in the metadata route's chat messages.

STAYS VIBECAST-INTERNAL (no platform change needed):
- Event normalization: the metadata subtype vocabulary is agent-neutral; a codex/pi adapter inside vibecast translates that agent's hooks/stream into the same subtypes (prompt/tool_use/tool_use_end/assistant_response/plan/...), just as the Claude hook layer does today. Relay buffers and the viewer then work unchanged.
- Interactive loops: map the foreign agent's ask/approval mechanisms onto the existing shapes — emit toolName 'AskUserQuestion'-shaped tool_use (or the purpose-built alp_pane/onboarding_external subtypes, which were designed for arbitrary interactive CLI prompts) and reuse the question-vote poll/inject protocol.
- Transcripts: simplest path is to NOT upload transcriptLines for non-Claude agents — the viewer's buildConversationFromMetadata fallback produces the identical beat shape from the live metadata stream, so conversation rendering keeps working with zero platform change. (Full-fidelity historical replay for other agents later = a transcriptFormat discriminator + per-format parser, or adapter-side conversion.)
- Usage/token fields: adapters normalize token counts into the Anthropic-shaped usage object ({input_tokens, output_tokens, cache_*}) — one adapter-side mapping beats touching every viewer consumer.
- Model/effort mapping, first-run gates, login detectors, tmux/ttyd, stop_broadcast MCP tools: all already vibecast-owned and agent-parameterizable inside vibecast.

Net: the platform needs (1) one new config field threaded station→line→AgentDefinition→one runner env var, (2) a handful of renames with back-compat aliases, (3) per-agent conditionals in the runner launch script. Everything event-shaped already generalizes because vibecast — by design, per the Operator model — is the only component that ever talks to the coding agent directly.

