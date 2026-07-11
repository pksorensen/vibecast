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

// PrepareInput carries the runtime paths a config-seed needs. stream.go builds it once
// per stream (in StartStream, before the first SpawnPane) and passes it to the selected
// adapter's Prepare.
type PrepareInput struct {
	BaseDir      string // parent dir under which to write an isolated agent config home
	Workspace    string // the work dir to pre-trust (so no folder-trust dialog blocks)
	VibecastBin  string // absolute vibecast binary path (baked into hooks + the MCP command)
	VibecastHome string // VIBECAST_HOME — forwarded into codex's sanitized MCP subprocess env
}

// Adapter is the pluggable coding-agent seam.
type Adapter interface {
	// Kind returns the agent identity.
	Kind() Kind
	// BinaryName is the executable the caller resolves with exec.LookPath.
	BinaryName() string
	// Prepare seeds an isolated, pre-trusted config home for the agent — the production
	// analog of the conformance harness's host-side config_seed. It writes whatever the
	// agent needs to fire vibecast's lifecycle hooks + MCP tools (codex: a CODEX_HOME with
	// hooks.json + config.toml MCP/trust) and returns the env vars the launch path must set
	// on the pane's environment (e.g. {"CODEX_HOME": dir}). Claude needs none (its hooks/MCP
	// ride in --plugin-dir and its config home comes from the runner env), so it returns
	// (nil, nil). Called once per stream before SpawnPane; must be idempotent (a restart in
	// the same process may call it again) and a no-op when the config home is already
	// vibecast-seeded (so the conformance harness's host-side seed is never double-applied).
	Prepare(in PrepareInput) (map[string]string, error)
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
	case KindPi:
		return piAdapter{}, nil
	default:
		return nil, fmt.Errorf("unknown VIBECAST_AGENT %q (supported: claude, codex, pi)", kind)
	}
}

// Selected resolves the adapter from VIBECAST_AGENT (default claude).
func Selected() (Adapter, error) {
	return For(Kind(strings.TrimSpace(os.Getenv("VIBECAST_AGENT"))))
}
