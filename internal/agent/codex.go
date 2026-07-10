package agent

import (
	"encoding/json"
	"os"
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
//   - No `--dangerously-skip-permissions`. Codex's guard backstop is
//     sandbox_mode=workspace-write + approval_policy=on-request (seeded in config.toml),
//     with the PreToolUse guard hook on top. vibecast must NEVER launch codex with
//     `--dangerously-bypass-approvals-and-sandbox` — that removes the sandbox backstop.
//   - No session-id pre-assignment. Codex generates its own UUIDv7 and surfaces it via the
//     SessionStart hook (discover-identity), so a fresh launch carries no session-id flag
//     even when spec.AgentSessionID is set.
//   - Resume is `codex resume <uuid>` (a thread id), falling back to `codex resume --last`
//     when no valid id is known — not claude's `--resume/--continue`.
//
// Every launch carries --dangerously-bypass-hook-trust: vibecast writes a hooks.json into
// the codex config layer (see CodexHooksJSON) to wire the SessionStart discover-identity
// hook + PreToolUse guard, and codex would otherwise block on an interactive "hooks need
// review" gate. This is hook-trust ONLY — it never touches the sandbox/approval backstop
// (that would require --dangerously-bypass-approvals-and-sandbox, which vibecast must never
// pass). The flag is both a global option and a `resume` subcommand option, and hooks.json
// persists in CODEX_HOME across a resume, so both launch paths carry it.
//
// v1 covers the launch surface: model + station system-prompt append
// (developer_instructions) + initial prompt + resume + hook wiring. Effort and guard-tuning
// land in later conformance-driven increments.
type codexAdapter struct{}

// codexHookTrustFlag skips codex's interactive hooks-review gate so vibecast's generated
// hooks.json loads non-interactively. Hook-trust only — NOT the sandbox/approval bypass.
const codexHookTrustFlag = " --dangerously-bypass-hook-trust"

func (codexAdapter) Kind() Kind                  { return KindCodex }
func (codexAdapter) BinaryName() string          { return "codex" }
func (codexAdapter) DiscoversOwnSessionID() bool { return true }

func (codexAdapter) BuildCommand(binPath string, spec LaunchSpec) (string, error) {
	cmd := binPath + codexHookTrustFlag
	cmd += codexModelFlag(spec.Model, spec.ModelTier)
	cmd += codexDeveloperInstructionsFlag(spec.SystemPromptFile, spec.SystemPromptInline)
	cmd += codexInitialPromptArg(spec.InitialPromptFile)
	return cmd, nil
}

func (codexAdapter) BuildResumeCommand(binPath string, spec LaunchSpec, agentSessionID string) (string, error) {
	devInstr := codexDeveloperInstructionsFlag(spec.SystemPromptFile, spec.SystemPromptInline)
	initialPrompt := codexInitialPromptArg(spec.InitialPromptFile)
	if agentSessionID != "" && util.IsUUID(agentSessionID) {
		return binPath + " resume" + codexHookTrustFlag + devInstr + " " + agentSessionID + initialPrompt, nil
	}
	if agentSessionID != "" {
		logDebug("[codex-cmd] dropping resume %q: not a UUID, falling back to --last\n", agentSessionID)
	}
	return binPath + " resume" + codexHookTrustFlag + devInstr + " --last" + initialPrompt, nil
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
func codexDeveloperInstructionsFlag(file, inline string) string {
	// Prefer file-based (avoids shell quoting issues with special chars/JSON), matching claude.
	if file != "" {
		escapedPath := strings.ReplaceAll(file, "'", "'\"'\"'")
		return " -c developer_instructions=\"$(cat '" + escapedPath + "')\""
	}
	if inline != "" {
		escaped := strings.ReplaceAll(inline, "'", "'\"'\"'")
		return " -c developer_instructions='" + escaped + "'"
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
