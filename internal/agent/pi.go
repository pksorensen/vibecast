package agent

import (
	"os"
	"strings"

	"github.com/pksorensen/vibecast/internal/util"
)

// piAdapter builds the launch/resume commands for pi (pi-coding-agent, 0.73.1 local /
// @earendil-works ≥0.75 latest). It is the third agent behind the internal/agent seam; the
// conformance suite (VIBECAST_CONFORMANCE_AGENTS=pi) is its spec and pi_test.go pins the exact
// command strings.
//
// pi is deliberately minimal on the command line — the differences from claude/codex are what
// it does NOT carry, all grounded in docs/agents/research/pi.md:
//   - No permission-skip / sandbox / hook-trust flag. pi ships no permission system (README:
//     "no permission popups"; no sandbox), so there is nothing to bypass. The dangerous-command
//     guard is the vibecast extension's tool_call handler ({block:true,reason}), not a CLI flag.
//   - No MCP flags. pi ships no MCP; the vibecast control tools (stop_broadcast, …) are registered
//     as NATIVE pi tools by the vibecast extension (pi.registerTool), so there is no tool-exposure
//     flag to set.
//   - No extension flag. pi auto-discovers extensions from $PI_CODING_AGENT_DIR/extensions/*.ts.
//     vibecast's config layer writes vibecast.ts there (the pi analog of codex's hooks.json in
//     CODEX_HOME / claude's plugin dir), so the hook wiring loads without a command-line -e.
//   - No session-id pre-assignment. On the installed 0.73.1, `--session-id <id>` does not exist
//     (it landed in ≥0.76, which needs Node ≥22.19; this box runs Node 20). pi mints its own
//     UUIDv7 and the vibecast extension surfaces it via session_start (discover-identity), so a
//     fresh launch carries no session-id flag even when spec.AgentSessionID is set.
//   - Resume is `pi --session <uuid>` (sessions are keyed by cwd; the caller runs it in the same
//     workdir), falling back to `pi --continue` (most-recent-for-cwd) when no valid id is known.
//
// Determinism knobs (PI_SKIP_VERSION_CHECK, PI_OFFLINE) and the isolated PI_CODING_AGENT_DIR ride
// in the launch ENVIRONMENT, not the command string (see the harness / stream env plumbing), so
// they do not appear here. Job-mode completion (the stop_broadcast mandate) lands in a later
// conformance-driven increment (C07), mirroring the codex progression.
type piAdapter struct{}

func (piAdapter) Kind() Kind                  { return KindPi }
func (piAdapter) BinaryName() string          { return "pi" }
func (piAdapter) DiscoversOwnSessionID() bool { return true }

func (piAdapter) BuildCommand(binPath string, spec LaunchSpec) (string, error) {
	cmd := binPath
	cmd += piModelFlag(spec.Model, spec.ModelTier)
	cmd += piAppendSystemPromptFlag(spec.SystemPromptFile, spec.SystemPromptInline)
	cmd += piInitialPromptArg(spec.InitialPromptFile)
	return cmd, nil
}

func (piAdapter) BuildResumeCommand(binPath string, spec LaunchSpec, agentSessionID string) (string, error) {
	modelFlag := piModelFlag(spec.Model, spec.ModelTier)
	promptFlag := piAppendSystemPromptFlag(spec.SystemPromptFile, spec.SystemPromptInline)
	initialPrompt := piInitialPromptArg(spec.InitialPromptFile)
	if agentSessionID != "" && util.IsUUID(agentSessionID) {
		return binPath + modelFlag + promptFlag + " --session " + agentSessionID + initialPrompt, nil
	}
	if agentSessionID != "" {
		logDebug("[pi-cmd] dropping --session %q: not a UUID, falling back to --continue\n", agentSessionID)
	}
	return binPath + modelFlag + promptFlag + " --continue" + initialPrompt, nil
}

// piModelFlag maps the per-station model config to `pi --model <model>`. pi model names are
// freeform and support the `provider/id` form (e.g. "pks-foundry/gpt-5.5"), so a specific model
// wins and an otherwise-unmapped tier is passed through as the model name for pi to validate.
// Empty → no flag (pi falls back to its configured default provider/model). Single-quoted with
// the standard '\'' escape so a provider/id with odd characters cannot break out.
func piModelFlag(model, tier string) string {
	if m := strings.TrimSpace(model); m != "" {
		return " --model '" + strings.ReplaceAll(m, "'", "'\"'\"'") + "'"
	}
	if t := strings.TrimSpace(tier); t != "" {
		return " --model '" + strings.ReplaceAll(t, "'", "'\"'\"'") + "'"
	}
	return ""
}

// piAppendSystemPromptFlag maps the station's appended system prompt to `pi --append-system-prompt`.
// pi appends (does not replace — that is --system-prompt, deliberately avoided). File-first to
// avoid shell-quoting issues with special chars/JSON, read at exec via "$(cat 'path')" — the same
// shape as claudeAppendSystemPromptFlag. Empty → no flag.
func piAppendSystemPromptFlag(file, inline string) string {
	if file != "" {
		escapedPath := strings.ReplaceAll(file, "'", "'\"'\"'")
		return " --append-system-prompt \"$(cat '" + escapedPath + "')\""
	}
	if inline != "" {
		escaped := strings.ReplaceAll(inline, "'", "'\"'\"'")
		return " --append-system-prompt '" + escaped + "'"
	}
	return ""
}

// piInitialPromptArg passes the initial job prompt as a positional argument, read from the file so
// multi-line content survives without shell-escaping or send-keys timing. The positional prompt
// auto-submits on pi startup (verified). A missing or empty file DROPS the argument (pi would
// otherwise sit idle at the editor). Mirrors claudeInitialPromptArg / codexInitialPromptArg.
func piInitialPromptArg(file string) string {
	if file == "" {
		return ""
	}
	info, err := os.Stat(file)
	if err != nil || info.Size() == 0 {
		logDebug("[pi-cmd] VIBECAST_INITIAL_PROMPT_FILE=%s missing or empty (err=%v) — dropping prompt arg\n", file, err)
		return ""
	}
	escapedPath := strings.ReplaceAll(file, "'", "'\"'\"'")
	return " \"$(cat '" + escapedPath + "')\""
}
