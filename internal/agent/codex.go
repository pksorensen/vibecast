package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/pksorensen/vibecast/internal/util"
)

// codexAdapter builds the launch/resume commands for OpenAI Codex CLI (0.142.x). It is the
// second agent behind the internal/agent seam; the conformance suite
// (VIBECAST_CONFORMANCE_AGENTS=codex) is its spec. codex_test.go pins the exact command
// strings.
//
// Three deliberate differences from the Claude adapter, all grounded in
// docs/agents/research/codex.md:
//   - No `--dangerously-skip-permissions`. The enforcement floor is approval_policy=on-request
//     + the PreToolUse guard hook. Codex additionally runs with -s danger-full-access
//     (sandbox_mode ONLY — see codexSandboxFlag), because its default workspace-write OS
//     sandbox cannot initialize in the ALP Runner container. vibecast must STILL never pass
//     `--dangerously-bypass-approvals-and-sandbox` — that flag ALSO kills approvals, and a
//     golden-test invariant forbids it.
//   - No session-id pre-assignment. Codex generates its own UUIDv7 and surfaces it via the
//     SessionStart hook (discover-identity), so a fresh launch carries no session-id flag
//     even when spec.AgentSessionID is set.
//   - Resume is `codex resume <uuid>` (a thread id), falling back to `codex resume --last`
//     when no valid id is known — not claude's `--resume/--continue`.
//
// Every launch carries --dangerously-bypass-hook-trust: vibecast writes a hooks.json into
// the codex config layer (see CodexHooksJSON) to wire the SessionStart discover-identity
// hook + PreToolUse guard, and codex would otherwise block on an interactive "hooks need
// review" gate. This flag is hook-trust ONLY — a separate axis from the sandbox posture
// (which -s danger-full-access sets) and from approvals (which stay on-request). The
// forbidden --dangerously-bypass-approvals-and-sandbox collapses all three at once; vibecast
// never passes it. --dangerously-bypass-hook-trust is both a global option and a `resume`
// subcommand option, and hooks.json persists in CODEX_HOME across a resume, so both launch
// paths carry it.
//
// v1 covers the launch surface: model + station system-prompt append
// (developer_instructions) + initial prompt + resume + hook wiring. Effort and guard-tuning
// land in later conformance-driven increments.
type codexAdapter struct{}

// codexHookTrustFlag skips codex's interactive hooks-review gate so vibecast's generated
// hooks.json loads non-interactively. Hook-trust only — NOT the sandbox/approval bypass.
const codexHookTrustFlag = " --dangerously-bypass-hook-trust"

// codexSandboxFlag runs codex with sandbox_mode=danger-full-access (the `-s`/`--sandbox`
// flag ONLY — never --dangerously-bypass-approvals-and-sandbox, which additionally sets
// approval=never and is forbidden by a golden-test invariant).
//
// Why this is deliberate, not a weakening: codex enforces its default workspace-write sandbox
// by wrapping every model-generated write/shell command in its bundled codex-linux-sandbox,
// which requires an unprivileged user namespace + a uid_map write. The ALP Runner container
// (and this devcontainer) denies that write — `unshare -Ur true` fails with
// `write failed /proc/self/uid_map: Operation not permitted`. Under workspace-write that makes
// EVERY apply_patch/write fail inside the sandbox; codex then stalls on an unanswerable
// "retry without sandbox?" prompt (conformance C05 timed out this way). No codex config fixes
// it (network_access=true doesn't help — the block is the userns, not the netns).
//
// danger-full-access removes only that OS backstop. It is NOT the approval bypass:
// approval_policy stays on-request, and — verified empirically — vibecast's PreToolUse guard
// hook STILL FIRES under danger-full-access, so the semantic dangerous-command block (C08)
// remains the enforcement floor. The container itself is the isolation boundary; the OS
// sandbox beneath the guard hook was redundant here and could not initialize regardless.
// Operator-signed-off: "we trust LLMs to run in this environment." Set on both launch and
// resume (-s is valid on the resume subcommand too). See docs/agents/research/codex.md.
const codexSandboxFlag = " -s danger-full-access"

// codexMCPToolExposureFlags forces vibecast's MCP tools to be exposed directly in the model's
// toolset instead of being hidden behind codex's tool_search meta-tool.
//
// Codex 0.142.x ships two enabled features — tool_search_always_defer_mcp_tools and
// tool_suggest — that DEFER MCP-server tools: the tools are loaded and reachable, but the
// model sees only a `tool_search.tool_search_tool` and must "search" to surface them. gpt-5.5
// searches unreliably, so vibecast's completion-signal tools (stop_broadcast — the C07 path)
// were invoked only sometimes, and the model would report "the tool is not available in this
// session." Disabling BOTH features (neither alone suffices) puts vibecast/<tool> back in the
// direct toolset. -c dotted overrides are used (not a config.toml [features] table) so they
// compose cleanly with any real user config, which may already carry a [features] section.
//
// Trade-off: this opts vibecast-launched codex out of the large-toolset context optimization.
// For vibecast's ~10 MCP tools plus codex builtins that is negligible and buys deterministic
// tool-calling, which the Operator contract requires. See docs/agents/research/codex.md and
// the C07 conformance scenario.
const codexMCPToolExposureFlags = " -c features.tool_suggest=false -c features.tool_search_always_defer_mcp_tools=false"

// codexJobModeInstructions is a developer-instructions preamble carried by every
// vibecast-launched codex running in job mode (AGENTICS_JOB_MODE=1 → LaunchSpec.JobMode). It
// bridges codex's tool model to vibecast's Operator completion contract: the job is done only
// when the agent calls the stop_broadcast MCP tool with a conclusion.
//
// Empirically load-bearing (conformance C07). With the vibecast MCP server registered and its
// tools exposed directly (codexMCPToolExposureFlags), a bare "call stop_broadcast now" user
// prompt still made gpt-5.5 call the tool 0/5 times — it silently finished, and sometimes
// reported the tool "is not available." Prepending this mandate to developer_instructions made
// it 5/5. The tool_search language is a defensive fallback: if a codex version/flag-state does
// defer the tool behind the tool_search meta-tool, this tells the model to load it first
// rather than declaring it unavailable.
//
// Job-mode-gated on purpose: mandating stop_broadcast in an interactive broadcast would end
// the stream after the first task. Kept ASCII-only (no quote/$/backslash/backtick/!) so it
// rides safely inside BOTH the single-quoted and double-quoted shell forms below.
const codexJobModeInstructions = "VIBECAST JOB MODE. This is an unattended broadcast job. Your broadcast lifecycle tools (stop_broadcast, get_broadcast_status, and related controls) are provided by the vibecast MCP server. Codex may defer MCP-server tools behind the tool_search tool rather than listing them directly; if a tool such as stop_broadcast is not in your visible tool list, call tool_search to load it first, then call it. When the job is complete, your final action MUST be to call stop_broadcast with a conclusion (for example conclusion=success) and a one-line summary message. Never end the session, and never report that stop_broadcast is unavailable, without first calling it or using tool_search to load it."

func (codexAdapter) Kind() Kind                  { return KindCodex }
func (codexAdapter) BinaryName() string          { return "codex" }
func (codexAdapter) DiscoversOwnSessionID() bool { return true }

func (codexAdapter) BuildCommand(binPath string, spec LaunchSpec) (string, error) {
	cmd := binPath + codexHookTrustFlag + codexMCPToolExposureFlags + codexSandboxFlag
	cmd += codexModelFlag(spec.Model, spec.ModelTier)
	cmd += codexDeveloperInstructionsFlag(spec.SystemPromptFile, spec.SystemPromptInline, spec.JobMode)
	cmd += codexInitialPromptArg(spec.InitialPromptFile)
	return cmd, nil
}

func (codexAdapter) BuildResumeCommand(binPath string, spec LaunchSpec, agentSessionID string) (string, error) {
	devInstr := codexDeveloperInstructionsFlag(spec.SystemPromptFile, spec.SystemPromptInline, spec.JobMode)
	initialPrompt := codexInitialPromptArg(spec.InitialPromptFile)
	if agentSessionID != "" && util.IsUUID(agentSessionID) {
		return binPath + " resume" + codexHookTrustFlag + codexMCPToolExposureFlags + codexSandboxFlag + devInstr + " " + agentSessionID + initialPrompt, nil
	}
	if agentSessionID != "" {
		logDebug("[codex-cmd] dropping resume %q: not a UUID, falling back to --last\n", agentSessionID)
	}
	return binPath + " resume" + codexHookTrustFlag + codexMCPToolExposureFlags + codexSandboxFlag + devInstr + " --last" + initialPrompt, nil
}

// codexModelFlag maps the per-station model config to `codex -m <model>`. Codex model names
// are freeform strings (no fixed tier aliases like Claude's opus/sonnet/haiku), so a
// specific model wins, and an otherwise-unmapped tier is passed through as the model name
// for codex to validate. Empty → no flag (codex default).
func codexModelFlag(model, tier string) string {
	if m := strings.TrimSpace(model); m != "" {
		return " -m '" + strings.ReplaceAll(m, "'", "'\"'\"'") + "'"
	}
	if t := strings.TrimSpace(tier); t != "" {
		return " -m '" + strings.ReplaceAll(t, "'", "'\"'\"'") + "'"
	}
	return ""
}

// codexDeveloperInstructionsFlag maps the station's appended system prompt to codex's
// `-c developer_instructions=<value>` override. Verified with `codex debug prompt-input`:
// the value lands in a developer-role message that APPENDS to codex's base instructions
// (never replaces them — that would be model_instructions_file, deliberately avoided).
//
// Codex parses the `-c` value as TOML and falls back to the raw string literal when the
// parse fails (per `codex -c --help`), so multi-line station prose with embedded quotes,
// newlines, backslashes, and even a leading `[section]` rides through `"$(cat 'file')"`
// verbatim — probed empirically. This is the same file-first, shell-reads-at-exec shape as
// claudeAppendSystemPromptFlag; no Go-side escaping is needed. (Edge case: a prompt that is
// itself a single valid TOML token — e.g. exactly `true` or `"x"` — would be TOML-coerced;
// real station prompts are always multi-word prose and never are.)
//
// In job mode (jobMode) the codexJobModeInstructions mandate is PREPENDED to whatever station
// prompt is present. Both ride in the single -c developer_instructions override — a second -c
// developer_instructions would clobber (last dotted override wins), not merge — so the mandate
// and the station prose are concatenated into one value, separated by a blank line. The
// mandate is ASCII-only, so it is literal inside both the double-quoted (file) and
// single-quoted (inline/mandate-only) shell forms.
func codexDeveloperInstructionsFlag(file, inline string, jobMode bool) string {
	preamble := ""
	if jobMode {
		preamble = codexJobModeInstructions
	}
	// Prefer file-based (avoids shell quoting issues with special chars/JSON), matching claude.
	if file != "" {
		escapedPath := strings.ReplaceAll(file, "'", "'\"'\"'")
		if preamble != "" {
			return " -c developer_instructions=\"" + preamble + "\n\n$(cat '" + escapedPath + "')\""
		}
		return " -c developer_instructions=\"$(cat '" + escapedPath + "')\""
	}
	if inline != "" {
		escaped := strings.ReplaceAll(inline, "'", "'\"'\"'")
		if preamble != "" {
			return " -c developer_instructions='" + preamble + "\n\n" + escaped + "'"
		}
		return " -c developer_instructions='" + escaped + "'"
	}
	if preamble != "" {
		return " -c developer_instructions='" + preamble + "'"
	}
	return ""
}

// codexInitialPromptArg passes the initial job prompt as a positional argument, read from
// the file so multi-line content survives without shell-escaping or send-keys timing. The
// positional prompt auto-submits in the codex TUI. A missing or empty file DROPS the
// argument (codex would otherwise sit idle at the composer). Mirrors claudeInitialPromptArg.
func codexInitialPromptArg(file string) string {
	if file == "" {
		return ""
	}
	info, err := os.Stat(file)
	if err != nil || info.Size() == 0 {
		logDebug("[codex-cmd] VIBECAST_INITIAL_PROMPT_FILE=%s missing or empty (err=%v) — dropping prompt arg\n", file, err)
		return ""
	}
	escapedPath := strings.ReplaceAll(file, "'", "'\"'\"'")
	return " \"$(cat '" + escapedPath + "')\""
}

// codexHookEntry builds one hooks.json matcher block wiring a codex lifecycle event to
// `<bin> hook <sub>`. Codex shell-splits the command string (verified against 0.142.x), so
// the claude-style single-string form with args works unchanged.
func codexHookEntry(bin, sub string) map[string]any {
	return map[string]any{
		"hooks": []map[string]any{
			{"type": "command", "command": bin + " hook " + sub},
		},
	}
}

// CodexHooksJSON returns the hooks.json that wires codex's lifecycle events to the vibecast
// hook subcommands. Codex uses the same hooks.json schema as Claude Code (event → array of
// matcher blocks, each with a `hooks` list of `{type:"command", command}`), so this mirrors
// claude-plugin/hooks/hooks.json but with the vibecast binary path baked absolute — codex
// has no ${CLAUDE_PLUGIN_ROOT}-style expansion guarantee.
//
// The launch path writes this into the codex config layer ($CODEX_HOME/hooks.json) and
// launches with --dangerously-bypass-hook-trust (see codexHookTrustFlag) so the hooks load
// without the interactive review gate. SessionStart carries the discover-identity flow (the
// hook reports codex's self-generated UUIDv7, which vibecast records into the session file);
// PreToolUse carries both the process-kill guard and the tool-metadata emitter.
func CodexHooksJSON(vibecastBin string) []byte {
	hooks := map[string]any{
		"SessionStart":     []any{codexHookEntry(vibecastBin, "session")},
		"UserPromptSubmit": []any{codexHookEntry(vibecastBin, "prompt")},
		"PreToolUse": []any{
			codexHookEntry(vibecastBin, "guard"),
			codexHookEntry(vibecastBin, "tool"),
		},
		"PostToolUse": []any{codexHookEntry(vibecastBin, "post-tool")},
		"Stop":        []any{codexHookEntry(vibecastBin, "stop")},
	}
	out, _ := json.MarshalIndent(map[string]any{"hooks": hooks}, "", "  ")
	return out
}

// CodexMCPServersTOML returns the config.toml fragment that registers the vibecast MCP
// server (`<bin> mcp serve`) with codex, to be appended to CODEX_HOME/config.toml.
//
// Two codex-specific differences from claude's plugin .mcp.json, both verified empirically
// against codex-cli 0.142.5, both load-bearing:
//
//  1. Explicit env forwarding. Claude inherits the pane's environment for its plugin MCP
//     server, so it resolves the control socket from VIBECAST_HOME with no extra wiring.
//     Codex sanitizes the MCP subprocess environment down to ~9 base vars (PATH, HOME, PWD,
//     …), dropping every VIBECAST_* the pane exported — confirmed with an env-dumping probe
//     server. So the env the server needs (at minimum VIBECAST_HOME, which resolves
//     $VIBECAST_HOME/.vibecast/control.sock) MUST be re-declared in this `env` table; codex
//     forwards exactly the keys listed here and nothing else. Omit it and every tool call
//     fails with "control server unavailable".
//
//  2. default_tools_approval_mode = "approve". In the interactive TUI, codex gates EVERY MCP
//     tool call behind an "Allow the vibecast MCP server to run tool …" dialog. vibecast runs
//     unattended (job mode), so an unanswered dialog stalls the whole session. Claude's
//     --dangerously-skip-permissions auto-approves all its tools; this is the codex analog,
//     scoped to vibecast's OWN control-plane MCP tools (stop_broadcast, restart_claude, …) —
//     the agent signalling lifecycle back to vibecast, not doing work in the workspace. It is
//     NOT the approval bypass: the agent's shell + apply_patch calls stay gated by
//     approval=on-request + the PreToolUse guard hook. (The OS sandbox is disabled via
//     -s danger-full-access because it can't initialize in the runner container — see
//     codexSandboxFlag — so the guard hook, not the OS sandbox, is the enforcement floor.)
//     --dangerously-bypass-approvals-and-sandbox would remove approvals AND the guard-gated
//     approval path too; this never touches either.
//
// env keys are emitted sorted for deterministic output (golden-test stable). An empty env
// map omits the `env` line entirely.
func CodexMCPServersTOML(vibecastBin string, env map[string]string) string {
	var b strings.Builder
	b.WriteString("\n[mcp_servers.vibecast]\n")
	fmt.Fprintf(&b, "command = %s\n", tomlBasicString(vibecastBin))
	b.WriteString("args = [\"mcp\", \"serve\"]\n")
	if len(env) > 0 {
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s = %s", k, tomlBasicString(env[k])))
		}
		fmt.Fprintf(&b, "env = { %s }\n", strings.Join(parts, ", "))
	}
	// Auto-approve vibecast's own control tools (never the workspace sandbox — see doc above).
	b.WriteString("default_tools_approval_mode = \"approve\"\n")
	return b.String()
}

// tomlBasicString wraps s as a TOML basic string, escaping backslash and double-quote (the
// only two characters that can break out of a quoted value for the filesystem paths and env
// values we emit). Mirrors the quoted-key escaping used for project trust entries.
func tomlBasicString(s string) string {
	esc := strings.ReplaceAll(s, `\`, `\\`)
	esc = strings.ReplaceAll(esc, `"`, `\"`)
	return `"` + esc + `"`
}
