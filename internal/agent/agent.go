// Package agent is vibecast's pluggable coding-agent seam. An Adapter turns an
// agent-neutral LaunchSpec into the shell command that starts (or resumes) a coding
// agent inside a tmux pane. The agent is selected at startup by VIBECAST_AGENT
// (claude | codex | pi, default claude); an unknown value fails fast before any tmux
// session is created.
//
// v1 exposes only the launch-command surface (BuildCommand/BuildResumeCommand). The
// ingestion, guard, gates, version, and tools seams described in
// docs/agents/adapter-spec.md are extracted in later steps. The Claude adapter is a
// byte-identical extraction of the previous inline stream.go logic — see
// claude_test.go for the golden command strings that pin that guarantee.
package agent

import (
	"fmt"
	"os"
	"strings"
)

// Kind identifies an agent implementation.
type Kind string

const (
	KindClaude Kind = "claude"
	KindCodex  Kind = "codex"
	KindPi     Kind = "pi"
)

// LaunchSpec is the agent-neutral description of a station launch. stream.go builds it
// from the environment (see SpecFromEnv) and the adapter maps it to native CLI flags.
// Fields carry raw values; each adapter normalizes (trim/lowercase/validate) exactly as
// that agent requires.
type LaunchSpec struct {
	AgentSessionID     string   // pre-assigned session id for a fresh launch (claude --session-id)
	Model              string   // exact model id (VIBECAST_MODEL / VIBECAST_CLAUDE_MODEL)
	ModelTier          string   // tier alias (VIBECAST_MODEL_TIER / VIBECAST_CLAUDE_MODEL_TIER)
	Effort             string   // reasoning effort (VIBECAST_EFFORT / VIBECAST_CLAUDE_EFFORT)
	SystemPromptFile   string   // VIBECAST_APPEND_SYSTEM_PROMPT_FILE
	SystemPromptInline string   // VIBECAST_APPEND_SYSTEM_PROMPT
	InitialPromptFile  string   // VIBECAST_INITIAL_PROMPT_FILE
	PluginDirs         []string // claude --plugin-dir list (telemetry plugin dir + VIBECAST_EXTRA_PLUGINS)
	JobMode            bool     // AGENTICS_JOB_MODE=1 — unattended job; the completion signal (stop_broadcast) is the contract
}

// Adapter is the pluggable coding-agent seam.
type Adapter interface {
	// Kind returns the agent identity.
	Kind() Kind
	// BinaryName is the executable the caller resolves with exec.LookPath.
	BinaryName() string
	// DiscoversOwnSessionID reports whether the agent generates its own session id at
	// runtime (surfaced via the SessionStart hook) rather than accepting a pre-assigned
	// one. When true, the launch path records an empty session-id placeholder and the
	// SessionStart hook writes back the discovered id (session.RecordDiscoveredSessionID);
	// when false (claude), the launch path pre-assigns the id and passes it to BuildCommand.
	DiscoversOwnSessionID() bool
	// BuildCommand returns the shell command that launches a fresh agent session.
	// binPath is the exec.LookPath-resolved absolute binary path. The caller is
	// responsible for the `cd <workdir> && ` prefix and (restart path only) the
	// exit-code echo wrapper — mirroring today's SpawnPane/DoRestartClaude asymmetry.
	BuildCommand(binPath string, spec LaunchSpec) (string, error)
	// BuildResumeCommand returns the shell command that resumes an existing session.
	// agentSessionID is the agent's own prior session id to resume.
	BuildResumeCommand(binPath string, spec LaunchSpec, agentSessionID string) (string, error)
}

// For returns the adapter for a kind. The empty string defaults to claude. An
// unrecognized kind is an error (fail fast before any tmux session exists).
func For(kind Kind) (Adapter, error) {
	switch kind {
	case KindClaude, "":
		return claudeAdapter{}, nil
	case KindCodex:
		return codexAdapter{}, nil
	default:
		return nil, fmt.Errorf("unknown VIBECAST_AGENT %q (supported: claude, codex)", kind)
	}
}

// Selected resolves the adapter from VIBECAST_AGENT (default claude).
func Selected() (Adapter, error) {
	return For(Kind(strings.TrimSpace(os.Getenv("VIBECAST_AGENT"))))
}
