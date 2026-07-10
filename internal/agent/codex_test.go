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

// TestCodexBuildCommandGolden pins the fresh-launch command strings. Codex launches with no
// permission-skip flag (sandbox is the backstop) but always carries
// --dangerously-bypass-hook-trust (vibecast ships a hooks.json; this skips the interactive
// hooks-review gate — hook-trust only, never the sandbox bypass). It never pre-assigns a
// session id (discover-identity via the SessionStart hook), so AgentSessionID is ignored on
// a fresh launch even when set.
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
			want: "codex --dangerously-bypass-hook-trust",
		},
		{
			name: "session id ignored on fresh launch (discover-identity)",
			spec: LaunchSpec{AgentSessionID: codexUUIDv7},
			want: "codex --dangerously-bypass-hook-trust",
		},
		{
			name: "explicit model",
			spec: LaunchSpec{Model: "gpt-5.5"},
			want: "codex --dangerously-bypass-hook-trust -m 'gpt-5.5'",
		},
		{
			name: "model tier passed through as model name",
			spec: LaunchSpec{ModelTier: "gpt-5.5-codex"},
			want: "codex --dangerously-bypass-hook-trust -m 'gpt-5.5-codex'",
		},
		{
			name: "explicit model wins over tier",
			spec: LaunchSpec{Model: "o3", ModelTier: "gpt-5.5"},
			want: "codex --dangerously-bypass-hook-trust -m 'o3'",
		},
		{
			name: "model with single quote is escaped",
			spec: LaunchSpec{Model: "weird'model"},
			want: "codex --dangerously-bypass-hook-trust -m 'weird'\"'\"'model'",
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
			// Safety invariant: hook-trust is bypassed, but the sandbox/approval backstop
			// must NEVER be. A regression here removes codex's guard floor.
			if strings.Contains(got, "--dangerously-bypass-approvals-and-sandbox") {
				t.Errorf("command must never bypass sandbox/approvals: %q", got)
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
	want := "codex --dangerously-bypass-hook-trust \"$(cat '" + present + "')\""
	if got != want {
		t.Errorf("present prompt file\n got: %q\nwant: %q", got, want)
	}

	// prompt + model preserves flag-before-positional ordering
	got, _ = ad.BuildCommand("codex", LaunchSpec{Model: "gpt-5.5", InitialPromptFile: present})
	want = "codex --dangerously-bypass-hook-trust -m 'gpt-5.5' \"$(cat '" + present + "')\""
	if got != want {
		t.Errorf("model + prompt\n got: %q\nwant: %q", got, want)
	}

	for _, f := range []string{empty, missing, ""} {
		got, _ := ad.BuildCommand("codex", LaunchSpec{InitialPromptFile: f})
		if got != "codex --dangerously-bypass-hook-trust" {
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
	want := "codex resume --dangerously-bypass-hook-trust " + codexUUIDv7
	if got != want {
		t.Errorf("resume uuidv7\n got: %q\nwant: %q", got, want)
	}

	got, _ = ad.BuildResumeCommand("codex", LaunchSpec{}, "short123")
	want = "codex resume --dangerously-bypass-hook-trust --last"
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
	want = "codex resume --dangerously-bypass-hook-trust " + codexUUIDv7 + " \"$(cat '" + nudge + "')\""
	if got != want {
		t.Errorf("resume with nudge\n got: %q\nwant: %q", got, want)
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
