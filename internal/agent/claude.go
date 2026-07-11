package agent

import (
	"fmt"
	"os"
	"strings"

	"github.com/pksorensen/vibecast/internal/util"
)

// claudeAdapter builds the launch/resume commands for Claude Code. It is a byte-identical
// extraction of the previous inline stream.go builders; claude_test.go pins the exact
// command strings so VIBECAST_AGENT=claude (and unset) stays a no-op refactor.
type claudeAdapter struct{}

func (claudeAdapter) Kind() Kind                  { return KindClaude }
func (claudeAdapter) BinaryName() string          { return "claude" }
func (claudeAdapter) DiscoversOwnSessionID() bool { return false }

func (claudeAdapter) BuildCommand(binPath string, spec LaunchSpec) (string, error) {
	cmd := binPath + " --dangerously-skip-permissions"
	cmd += claudePluginFlags(spec.PluginDirs)
	cmd += claudeModelFlag(spec.Model, spec.ModelTier)
	cmd += claudeEffortFlag(spec.Effort)
	cmd += claudeDevChannelFlag()
	cmd += claudeAppendSystemPromptFlag(spec.SystemPromptFile, spec.SystemPromptInline)
	cmd += claudeInitialPromptArg(spec.InitialPromptFile)
	cmd += claudeSessionIDFlag(spec.AgentSessionID)
	return cmd, nil
}

func (claudeAdapter) BuildResumeCommand(binPath string, spec LaunchSpec, agentSessionID string) (string, error) {
	pluginFlags := claudePluginFlags(spec.PluginDirs)
	modelFlag := claudeModelFlag(spec.Model, spec.ModelTier)
	effortFlag := claudeEffortFlag(spec.Effort)
	channelFlag := claudeDevChannelFlag()
	promptFlag := claudeAppendSystemPromptFlag(spec.SystemPromptFile, spec.SystemPromptInline)
	initialPrompt := claudeInitialPromptArg(spec.InitialPromptFile)
	if agentSessionID != "" && util.IsUUIDv4(agentSessionID) {
		return binPath + " --dangerously-skip-permissions" + pluginFlags + modelFlag + effortFlag + channelFlag + promptFlag + " --resume " + agentSessionID + initialPrompt, nil
	}
	if agentSessionID != "" {
		logDebug("[claude-cmd] dropping --resume %q: not a UUIDv4, falling back to --continue\n", agentSessionID)
	}
	return binPath + " --dangerously-skip-permissions" + pluginFlags + modelFlag + effortFlag + channelFlag + promptFlag + " --continue" + initialPrompt, nil
}

func claudePluginFlags(dirs []string) string {
	flags := ""
	for _, dir := range dirs {
		if dir = strings.TrimSpace(dir); dir != "" {
			flags += " --plugin-dir " + dir
		}
	}
	return flags
}

func claudeAppendSystemPromptFlag(file, inline string) string {
	// Prefer file-based approach to avoid shell quoting issues with special chars/JSON.
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

// claudeInitialPromptArg returns a shell fragment that passes the initial job prompt as a
// positional argument to Claude, read from the file so arbitrary multi-line content is
// handled without shell-escaping or send-keys timing issues. A missing or empty file DROPS
// the argument (an empty positional arg would land Claude on the welcome screen with no
// task). The OTEL "initial_prompt.missing" span is emitted at the stream.go boundary.
func claudeInitialPromptArg(file string) string {
	if file == "" {
		return ""
	}
	info, err := os.Stat(file)
	if err != nil || info.Size() == 0 {
		logDebug("[claude-cmd] VIBECAST_INITIAL_PROMPT_FILE=%s missing or empty (err=%v) — dropping prompt arg\n", file, err)
		return ""
	}
	escapedPath := strings.ReplaceAll(file, "'", "'\"'\"'")
	// "$(cat 'path')" expands to the file content as a single argument, preserving newlines.
	return " \"$(cat '" + escapedPath + "')\""
}

// claudeSessionIDFlag returns " --session-id <id>" only when id is a valid UUIDv4. Claude
// rejects non-UUID values and exits immediately — passing vibecast's 8-char session id here
// used to kill the pane silently.
func claudeSessionIDFlag(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	if !util.IsUUIDv4(sessionID) {
		logDebug("[claude-cmd] dropping --session-id %q: not a UUIDv4\n", sessionID)
		return ""
	}
	return " --session-id " + sessionID
}

// claudeValidModelTiers are the model family aliases vibecast maps to `claude --model`.
// They double as valid --model aliases, so the mapping is identity for known tiers.
var claudeValidModelTiers = map[string]bool{"haiku": true, "sonnet": true, "opus": true}

// claudeValidEfforts are the effort levels Claude Code accepts (claude --effort <level>).
var claudeValidEfforts = map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}

// claudeModelFlag maps the per-station model config to a `claude --model` flag. A specific
// model id wins when set (passed through for the provider to honor or reject); otherwise the
// family tier is mapped to its alias. Unknown/empty → no flag (Claude default).
func claudeModelFlag(model, tier string) string {
	if m := strings.TrimSpace(model); m != "" {
		escaped := strings.ReplaceAll(m, "'", "'\"'\"'")
		return " --model '" + escaped + "'"
	}
	if t := strings.ToLower(strings.TrimSpace(tier)); t != "" {
		if claudeValidModelTiers[t] {
			return " --model " + t
		}
		logDebug("[claude-cmd] dropping model tier %q: unknown tier\n", tier)
	}
	return ""
}

// claudeEffortFlag maps the effort config to `claude --effort <level>`. An unknown value is
// dropped (Claude would warn and fall back to default anyway); empty → no flag.
func claudeEffortFlag(effort string) string {
	e := strings.ToLower(strings.TrimSpace(effort))
	if e == "" {
		return ""
	}
	if !claudeValidEfforts[e] {
		logDebug("[claude-cmd] dropping effort %q: not in low|medium|high|xhigh|max\n", effort)
		return ""
	}
	return " --effort " + e
}

// claudeDevChannelFlag maps VIBECAST_CLAUDE_CHANNEL to `claude --dangerously-load-development-channels
// <value>`. Set by the Runner's launch script when this session is a devcontainer-hosted devagent
// session with a `.mcp.json` agent-share entry already written (plan `snappy-wandering-mochi` Phase 3) —
// empty → no flag. Kept out of claude_test.go's golden strings (unset in tests → no-op).
func claudeDevChannelFlag() string {
	channel := strings.TrimSpace(os.Getenv("VIBECAST_CLAUDE_CHANNEL"))
	if channel == "" {
		return ""
	}
	escaped := strings.ReplaceAll(channel, "'", "'\"'\"'")
	return " --dangerously-load-development-channels '" + escaped + "'"
}

var agentDebug = os.Getenv("VIBECAST_DEBUG") != ""

func logDebug(format string, args ...any) {
	if agentDebug {
		fmt.Fprintf(os.Stderr, format, args...)
	}
}
