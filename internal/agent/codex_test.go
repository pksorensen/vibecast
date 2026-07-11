package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// codexUUIDv7 is a syntactically valid UUIDv7 (version nibble 7) — the shape codex assigns
// to a session/thread id. IsUUIDv4 rejects it; IsUUID accepts it. Pinning a v7 here guards
// against a future regression that swaps the codex resume guard back to IsUUIDv4.
const codexUUIDv7 = "019f4cf6-5e8d-7abc-8def-0123456789ab"

// cxLaunch / cxResume are the invariant command prefixes every codex launch/resume carries:
// hook-trust bypass + the two feature-disable overrides that keep vibecast's MCP tools in the
// model's direct toolset (see codexMCPToolExposureFlags) + the plugins-off override (see
// codexPluginDisableFlag — no per-launch marketplace clone) + the -s danger-full-access sandbox
// posture (see codexSandboxFlag — the OS sandbox can't init in the runner container, so the
// PreToolUse guard hook is the floor). These are spelled out as literals here — NOT imported
// from the production const — so a regression in the production flag value (a typo, a dropped
// flag, a re-enabled feature, a silently changed sandbox mode) breaks these golden assertions.
const (
	cxLaunch = "codex --dangerously-bypass-hook-trust -c features.tool_suggest=false -c features.tool_search_always_defer_mcp_tools=false -c features.plugins=false -s danger-full-access"
	cxResume = "codex resume --dangerously-bypass-hook-trust -c features.tool_suggest=false -c features.tool_search_always_defer_mcp_tools=false -c features.plugins=false -s danger-full-access"
)

// TestCodexBuildCommandGolden pins the fresh-launch command strings. Codex launches with no
// permission-skip flag but always carries --dangerously-bypass-hook-trust (vibecast ships a
// hooks.json; this skips the interactive hooks-review gate — hook-trust only) and
// -s danger-full-access (the OS sandbox can't init in the runner container, so the PreToolUse
// guard hook is the floor — never the --dangerously-bypass-approvals-and-sandbox bypass). It
// never pre-assigns a session id (discover-identity via the SessionStart hook), so
// AgentSessionID is ignored on a fresh launch even when set.
func TestCodexBuildCommandGolden(t *testing.T) {
	ad := codexAdapter{}
	tests := []struct {
		name string
		spec LaunchSpec
		want string
	}{
		{
			name: "bare",
			spec: LaunchSpec{},
			want: cxLaunch,
		},
		{
			name: "session id ignored on fresh launch (discover-identity)",
			spec: LaunchSpec{AgentSessionID: codexUUIDv7},
			want: cxLaunch,
		},
		{
			name: "explicit model",
			spec: LaunchSpec{Model: "gpt-5.5"},
			want: cxLaunch + " -m 'gpt-5.5'",
		},
		{
			name: "model tier passed through as model name",
			spec: LaunchSpec{ModelTier: "gpt-5.5-codex"},
			want: cxLaunch + " -m 'gpt-5.5-codex'",
		},
		{
			name: "explicit model wins over tier",
			spec: LaunchSpec{Model: "o3", ModelTier: "gpt-5.5"},
			want: cxLaunch + " -m 'o3'",
		},
		{
			name: "model with single quote is escaped",
			spec: LaunchSpec{Model: "weird'model"},
			want: cxLaunch + " -m 'weird'\"'\"'model'",
		},
		{
			name: "system prompt file appends via developer_instructions",
			spec: LaunchSpec{SystemPromptFile: "/tmp/sys.txt"},
			want: cxLaunch + " -c developer_instructions=\"$(cat '/tmp/sys.txt')\"",
		},
		{
			name: "inline system prompt appends via developer_instructions",
			spec: LaunchSpec{SystemPromptInline: "be terse"},
			want: cxLaunch + " -c developer_instructions='be terse'",
		},
		{
			name: "system prompt file wins over inline",
			spec: LaunchSpec{SystemPromptFile: "/tmp/sys.txt", SystemPromptInline: "ignored"},
			want: cxLaunch + " -c developer_instructions=\"$(cat '/tmp/sys.txt')\"",
		},
		{
			name: "model then developer_instructions ordering (flags before positional)",
			spec: LaunchSpec{Model: "gpt-5.5", SystemPromptFile: "/tmp/sys.txt"},
			want: cxLaunch + " -m 'gpt-5.5' -c developer_instructions=\"$(cat '/tmp/sys.txt')\"",
		},
		{
			// Job mode with no station prompt: the stop_broadcast mandate rides alone in
			// developer_instructions (single-quoted; the mandate is ASCII-safe). This is the
			// C07 path — without it gpt-5.5 finishes without signalling completion.
			name: "job mode injects stop_broadcast mandate (no station prompt)",
			spec: LaunchSpec{JobMode: true},
			want: cxLaunch + " -c developer_instructions='" + codexJobModeInstructions + "'",
		},
		{
			// Job mode + inline station prompt: mandate PREPENDED, station prose appended
			// after a blank line, both in the one single-quoted value.
			name: "job mode prepends mandate before inline station prompt",
			spec: LaunchSpec{JobMode: true, SystemPromptInline: "be terse"},
			want: cxLaunch + " -c developer_instructions='" + codexJobModeInstructions + "\n\nbe terse'",
		},
		{
			// Job mode + station prompt FILE: double-quoted so $(cat ...) still expands, with
			// the mandate literal before it. A real newline separates them (safe under sh -c).
			name: "job mode prepends mandate before station prompt file",
			spec: LaunchSpec{JobMode: true, SystemPromptFile: "/tmp/sys.txt"},
			want: cxLaunch + " -c developer_instructions=\"" + codexJobModeInstructions + "\n\n$(cat '/tmp/sys.txt')\"",
		},
		{
			// Not job mode: no mandate (mandating stop_broadcast in an interactive broadcast
			// would end the stream after one task).
			name: "interactive (non-job) mode carries no mandate",
			spec: LaunchSpec{SystemPromptInline: "be terse"},
			want: cxLaunch + " -c developer_instructions='be terse'",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ad.BuildCommand("codex", tt.spec)
			if err != nil {
				t.Fatalf("BuildCommand error: %v", err)
			}
			if got != tt.want {
				t.Errorf("BuildCommand mismatch\n got: %q\nwant: %q", got, tt.want)
			}
			// Safety invariant: hook-trust and the OS sandbox may be relaxed, but the APPROVAL
			// backstop must NEVER be — --dangerously-bypass-approvals-and-sandbox collapses
			// approvals + the guard-gated path and is forbidden. -s danger-full-access (below)
			// relaxes only the OS sandbox; approval=on-request + the PreToolUse guard hook stay.
			if strings.Contains(got, "--dangerously-bypass-approvals-and-sandbox") {
				t.Errorf("command must never bypass approvals: %q", got)
			}
			// The deliberate sandbox posture (see codexSandboxFlag): danger-full-access ONLY,
			// because codex's workspace-write OS sandbox can't initialize in the runner container.
			// This must be the -s flag form, never smuggled in via the approval-bypass flag.
			if !strings.Contains(got, "-s danger-full-access") {
				t.Errorf("command must set -s danger-full-access (OS sandbox can't init in the runner container): %q", got)
			}
			// C07 invariant: codex 0.142.x hides MCP tools behind a tool_search meta-tool
			// unless BOTH of these features are disabled. Dropping either one regresses
			// vibecast's completion-signal tool-calling back to unreliable.
			if !strings.Contains(got, "-c features.tool_suggest=false") {
				t.Errorf("command must disable tool_suggest to expose MCP tools directly: %q", got)
			}
			if !strings.Contains(got, "-c features.tool_search_always_defer_mcp_tools=false") {
				t.Errorf("command must disable tool_search_always_defer_mcp_tools to expose MCP tools directly: %q", got)
			}
			// Plugins are disabled so a fresh CODEX_HOME never stages a marketplace clone (a
			// per-launch network/latency/failure surface, and the source of a temp-dir cleanup
			// race in conformance). See codexPluginDisableFlag.
			if !strings.Contains(got, "-c features.plugins=false") {
				t.Errorf("command must disable the plugins feature (no per-launch marketplace clone): %q", got)
			}
		})
	}
}

// TestCodexInitialPromptArg pins the positional-prompt behavior: a present non-empty file is
// appended as a positional arg (auto-submits in the codex TUI); a missing or empty file is
// dropped. Mirrors the Claude adapter's stat-drop semantics.
func TestCodexInitialPromptArg(t *testing.T) {
	ad := codexAdapter{}
	dir := t.TempDir()

	present := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(present, []byte("do the thing"), 0o644); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "nope.txt")

	got, _ := ad.BuildCommand("codex", LaunchSpec{InitialPromptFile: present})
	want := cxLaunch + " \"$(cat '" + present + "')\""
	if got != want {
		t.Errorf("present prompt file\n got: %q\nwant: %q", got, want)
	}

	// prompt + model preserves flag-before-positional ordering
	got, _ = ad.BuildCommand("codex", LaunchSpec{Model: "gpt-5.5", InitialPromptFile: present})
	want = cxLaunch + " -m 'gpt-5.5' \"$(cat '" + present + "')\""
	if got != want {
		t.Errorf("model + prompt\n got: %q\nwant: %q", got, want)
	}

	for _, f := range []string{empty, missing, ""} {
		got, _ := ad.BuildCommand("codex", LaunchSpec{InitialPromptFile: f})
		if got != cxLaunch {
			t.Errorf("prompt file %q should be dropped, got: %q", f, got)
		}
	}
}

// TestCodexBuildResumeCommandGolden pins the resume command strings. Codex resumes a thread
// by its UUIDv7; an unknown/invalid id falls back to `resume --last`. The initial prompt (a
// resume nudge) trails the id as a positional arg.
func TestCodexBuildResumeCommandGolden(t *testing.T) {
	ad := codexAdapter{}

	got, _ := ad.BuildResumeCommand("codex", LaunchSpec{}, codexUUIDv7)
	want := cxResume + " " + codexUUIDv7
	if got != want {
		t.Errorf("resume uuidv7\n got: %q\nwant: %q", got, want)
	}

	got, _ = ad.BuildResumeCommand("codex", LaunchSpec{}, "short123")
	want = cxResume + " --last"
	if got != want {
		t.Errorf("resume non-uuid falls back to --last\n got: %q\nwant: %q", got, want)
	}

	got, _ = ad.BuildResumeCommand("codex", LaunchSpec{}, "")
	if got != want {
		t.Errorf("empty resume id falls back to --last\n got: %q\nwant: %q", got, want)
	}

	// resume nudge prompt trails the id
	dir := t.TempDir()
	nudge := filepath.Join(dir, "nudge.txt")
	if err := os.WriteFile(nudge, []byte("continue where you left off"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _ = ad.BuildResumeCommand("codex", LaunchSpec{InitialPromptFile: nudge}, codexUUIDv7)
	want = cxResume + " " + codexUUIDv7 + " \"$(cat '" + nudge + "')\""
	if got != want {
		t.Errorf("resume with nudge\n got: %q\nwant: %q", got, want)
	}

	// The station system prompt is re-applied on resume, before the positional thread id.
	got, _ = ad.BuildResumeCommand("codex", LaunchSpec{SystemPromptFile: "/tmp/sys.txt"}, codexUUIDv7)
	want = cxResume + " -c developer_instructions=\"$(cat '/tmp/sys.txt')\" " + codexUUIDv7
	if got != want {
		t.Errorf("resume with system prompt\n got: %q\nwant: %q", got, want)
	}

	// Job mode re-applies the stop_broadcast mandate on resume too (a resumed job still owes
	// the completion signal), before the positional thread id.
	got, _ = ad.BuildResumeCommand("codex", LaunchSpec{JobMode: true}, codexUUIDv7)
	want = cxResume + " -c developer_instructions='" + codexJobModeInstructions + "' " + codexUUIDv7
	if got != want {
		t.Errorf("resume in job mode\n got: %q\nwant: %q", got, want)
	}
}

// TestCodexJobModeInstructions guards the load-bearing SEMANTICS of the job-mode mandate
// independently of its exact wording (which the golden tests pin verbatim): it must name the
// stop_broadcast completion tool, mandate the call, mention the tool_search fallback, and — so
// it rides safely inside both shell-quoting forms — contain no character that would break out
// of a single- or double-quoted string.
func TestCodexJobModeInstructions(t *testing.T) {
	m := codexJobModeInstructions
	for _, tok := range []string{"stop_broadcast", "MUST", "tool_search", "conclusion"} {
		if !strings.Contains(m, tok) {
			t.Errorf("job-mode mandate missing load-bearing token %q", tok)
		}
	}
	// Shell-safety invariant: none of these may appear, or the mandate could break out of the
	// single-quoted (') or double-quoted (" $ ` \) developer_instructions value, or trigger
	// history expansion (!) under an interactive shell.
	for _, bad := range []string{"'", "\"", "$", "`", "\\", "!"} {
		if strings.Contains(m, bad) {
			t.Errorf("job-mode mandate contains shell-unsafe %q — must stay ASCII-quote-safe", bad)
		}
	}
}

// TestCodexHooksJSON pins the generated hooks.json: valid JSON, the claude-compatible
// schema shape (event → matcher blocks → command hooks), the vibecast binary path baked
// absolute into every command, and the SessionStart→`hook session` discover-identity wiring
// plus the two PreToolUse blocks (guard + tool).
func TestCodexHooksJSON(t *testing.T) {
	const bin = "/opt/vibecast/vibecast"
	raw := CodexHooksJSON(bin)

	var doc struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("hooks.json is not valid JSON: %v\n%s", err, raw)
	}

	want := map[string][]string{
		"SessionStart":     {bin + " hook session"},
		"UserPromptSubmit": {bin + " hook prompt"},
		"PreToolUse":       {bin + " hook guard", bin + " hook tool"},
		"PostToolUse":      {bin + " hook post-tool"},
		"Stop":             {bin + " hook stop"},
	}
	for event, cmds := range want {
		blocks, ok := doc.Hooks[event]
		if !ok {
			t.Errorf("missing event %q", event)
			continue
		}
		if len(blocks) != len(cmds) {
			t.Errorf("event %q: got %d blocks, want %d", event, len(blocks), len(cmds))
			continue
		}
		for i, block := range blocks {
			if len(block.Hooks) != 1 {
				t.Errorf("event %q block %d: got %d command hooks, want 1", event, i, len(block.Hooks))
				continue
			}
			if block.Hooks[0].Type != "command" {
				t.Errorf("event %q block %d: type = %q, want command", event, i, block.Hooks[0].Type)
			}
			if block.Hooks[0].Command != cmds[i] {
				t.Errorf("event %q block %d: command = %q, want %q", event, i, block.Hooks[0].Command, cmds[i])
			}
		}
	}

	// Absolute binary path everywhere — codex has no ${CLAUDE_PLUGIN_ROOT} expansion.
	if strings.Contains(string(raw), "${") {
		t.Errorf("hooks.json must not rely on shell/env expansion: %s", raw)
	}
}

// TestCodexMCPServersTOML pins the config.toml fragment that registers the vibecast MCP
// server. The load-bearing invariant: because codex sanitizes the MCP subprocess env (unlike
// claude, which inherits the pane env), VIBECAST_HOME MUST appear in the explicit `env` table
// — without it the server can't resolve the control socket and stop_broadcast (C07) fails.
func TestCodexMCPServersTOML(t *testing.T) {
	const bin = "/opt/vibecast/vibecast"

	got := CodexMCPServersTOML(bin, map[string]string{"VIBECAST_HOME": "/base/home"})
	want := "\n[mcp_servers.vibecast]\n" +
		"command = \"/opt/vibecast/vibecast\"\n" +
		"args = [\"mcp\", \"serve\"]\n" +
		"env = { VIBECAST_HOME = \"/base/home\" }\n" +
		"default_tools_approval_mode = \"approve\"\n"
	if got != want {
		t.Errorf("MCP TOML mismatch\n got: %q\nwant: %q", got, want)
	}

	// The vibecast control tools are auto-approved (job mode is unattended), but this must
	// never be the sandbox/approval bypass that guards the agent's shell commands.
	if !strings.Contains(got, "default_tools_approval_mode = \"approve\"") {
		t.Errorf("vibecast MCP tools must be pre-approved for unattended job mode: %q", got)
	}
	if strings.Contains(got, "dangerously-bypass") {
		t.Errorf("MCP config must never carry a sandbox/approval bypass: %q", got)
	}

	// Multiple env keys are emitted sorted (deterministic / golden-stable).
	got = CodexMCPServersTOML(bin, map[string]string{
		"VIBECAST_SESSION_ID": "abc",
		"VIBECAST_HOME":       "/base/home",
	})
	if !strings.Contains(got, "env = { VIBECAST_HOME = \"/base/home\", VIBECAST_SESSION_ID = \"abc\" }") {
		t.Errorf("env keys should be sorted: %q", got)
	}

	// A path containing a double-quote is TOML-escaped so it can't break out of the value.
	got = CodexMCPServersTOML(bin, map[string]string{"VIBECAST_HOME": `/o"dd/home`})
	if !strings.Contains(got, `VIBECAST_HOME = "/o\"dd/home"`) {
		t.Errorf("value not escaped: %q", got)
	}

	// Empty env omits the env line entirely (never emits a bare `env = { }`).
	got = CodexMCPServersTOML(bin, nil)
	if strings.Contains(got, "env =") {
		t.Errorf("empty env must omit the env line: %q", got)
	}
}

// TestForResolvesCodex ensures VIBECAST_AGENT=codex resolves to the codex adapter.
func TestForResolvesCodex(t *testing.T) {
	ad, err := For(KindCodex)
	if err != nil {
		t.Fatalf("codex should resolve: %v", err)
	}
	if ad.Kind() != KindCodex {
		t.Errorf("Kind() = %q, want codex", ad.Kind())
	}
	if ad.BinaryName() != "codex" {
		t.Errorf("BinaryName() = %q, want codex", ad.BinaryName())
	}
}
