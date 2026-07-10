package agent

import (
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
// v1 covers the launch surface only (model + initial prompt + resume). The
// developer_instructions system-prompt append, effort, hook wiring (which adds
// --dangerously-bypass-hook-trust) and guard land in later conformance-driven increments.
type codexAdapter struct{}

func (codexAdapter) Kind() Kind         { return KindCodex }
func (codexAdapter) BinaryName() string { return "codex" }

func (codexAdapter) BuildCommand(binPath string, spec LaunchSpec) (string, error) {
	cmd := binPath
	cmd += codexModelFlag(spec.Model, spec.ModelTier)
	cmd += codexInitialPromptArg(spec.InitialPromptFile)
	return cmd, nil
}

func (codexAdapter) BuildResumeCommand(binPath string, spec LaunchSpec, agentSessionID string) (string, error) {
	initialPrompt := codexInitialPromptArg(spec.InitialPromptFile)
	if agentSessionID != "" && util.IsUUID(agentSessionID) {
		return binPath + " resume " + agentSessionID + initialPrompt, nil
	}
	if agentSessionID != "" {
		logDebug("[codex-cmd] dropping resume %q: not a UUID, falling back to --last\n", agentSessionID)
	}
	return binPath + " resume --last" + initialPrompt, nil
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
