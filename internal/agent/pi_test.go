package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// piUUIDv7 is a syntactically valid UUIDv7 (version nibble 7) — the shape pi assigns to a
// session id (observed e.g. 019f4cf7-2e79-709c-…). IsUUIDv4 rejects it; IsUUID accepts it.
const piUUIDv7 = "019f4cf7-2e79-709c-964e-df61087f43d3"

// TestPiBuildCommandGolden pins the fresh-launch command strings. pi launches with NO special
// flags — no permission-skip (pi has no permission system), no hook-trust, no MCP exposure, no
// extension flag (vibecast.ts is auto-discovered from $PI_CODING_AGENT_DIR/extensions/). It never
// pre-assigns a session id (discover-identity via the extension's session_start), so AgentSessionID
// is ignored on a fresh launch even when set.
func TestPiBuildCommandGolden(t *testing.T) {
	ad := piAdapter{}
	tests := []struct {
		name string
		spec LaunchSpec
		want string
	}{
		{
			name: "bare",
			spec: LaunchSpec{},
			want: "pi",
		},
		{
			name: "session id ignored on fresh launch (discover-identity)",
			spec: LaunchSpec{AgentSessionID: piUUIDv7},
			want: "pi",
		},
		{
			name: "explicit model",
			spec: LaunchSpec{Model: "gpt-5.5"},
			want: "pi --model 'gpt-5.5'",
		},
		{
			name: "provider/id model form (the Foundry path)",
			spec: LaunchSpec{Model: "pks-foundry/gpt-5.5"},
			want: "pi --model 'pks-foundry/gpt-5.5'",
		},
		{
			name: "model tier passed through as model name",
			spec: LaunchSpec{ModelTier: "sonnet"},
			want: "pi --model 'sonnet'",
		},
		{
			name: "explicit model wins over tier",
			spec: LaunchSpec{Model: "pks-foundry/gpt-5.5", ModelTier: "sonnet"},
			want: "pi --model 'pks-foundry/gpt-5.5'",
		},
		{
			name: "model with single quote is escaped",
			spec: LaunchSpec{Model: "weird'model"},
			want: "pi --model 'weird'\"'\"'model'",
		},
		{
			name: "system prompt file appends via --append-system-prompt",
			spec: LaunchSpec{SystemPromptFile: "/tmp/sys.txt"},
			want: "pi --append-system-prompt \"$(cat '/tmp/sys.txt')\"",
		},
		{
			name: "inline system prompt appends via --append-system-prompt",
			spec: LaunchSpec{SystemPromptInline: "be terse"},
			want: "pi --append-system-prompt 'be terse'",
		},
		{
			name: "system prompt file wins over inline",
			spec: LaunchSpec{SystemPromptFile: "/tmp/sys.txt", SystemPromptInline: "ignored"},
			want: "pi --append-system-prompt \"$(cat '/tmp/sys.txt')\"",
		},
		{
			name: "model then system prompt ordering (flags before positional)",
			spec: LaunchSpec{Model: "pks-foundry/gpt-5.5", SystemPromptFile: "/tmp/sys.txt"},
			want: "pi --model 'pks-foundry/gpt-5.5' --append-system-prompt \"$(cat '/tmp/sys.txt')\"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ad.BuildCommand("pi", tt.spec)
			if err != nil {
				t.Fatalf("BuildCommand error: %v", err)
			}
			if got != tt.want {
				t.Errorf("BuildCommand mismatch\n got: %q\nwant: %q", got, tt.want)
			}
			// Safety invariant: pi has no permission system, so vibecast must not invent a
			// bypass flag; the guard is the extension's tool_call block. A --dangerously-*
			// creeping in would be a copy-paste regression from the claude/codex adapters.
			if !strings.HasPrefix(got, "pi") {
				t.Errorf("command must start with the pi binary: %q", got)
			}
			if strings.Contains(got, "--dangerously") {
				t.Errorf("pi has no permission system — no --dangerously-* flag belongs here: %q", got)
			}
		})
	}
}

// TestPiInitialPromptArg pins the positional-prompt behavior: a present non-empty file is appended
// as a positional arg (auto-submits on pi startup); a missing or empty file is dropped. Mirrors the
// claude/codex adapters' stat-drop semantics.
func TestPiInitialPromptArg(t *testing.T) {
	ad := piAdapter{}
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

	got, _ := ad.BuildCommand("pi", LaunchSpec{InitialPromptFile: present})
	want := "pi \"$(cat '" + present + "')\""
	if got != want {
		t.Errorf("present prompt file\n got: %q\nwant: %q", got, want)
	}

	// prompt + model preserves flag-before-positional ordering
	got, _ = ad.BuildCommand("pi", LaunchSpec{Model: "pks-foundry/gpt-5.5", InitialPromptFile: present})
	want = "pi --model 'pks-foundry/gpt-5.5' \"$(cat '" + present + "')\""
	if got != want {
		t.Errorf("model + prompt\n got: %q\nwant: %q", got, want)
	}

	for _, f := range []string{empty, missing, ""} {
		got, _ := ad.BuildCommand("pi", LaunchSpec{InitialPromptFile: f})
		if got != "pi" {
			t.Errorf("prompt file %q should be dropped, got: %q", f, got)
		}
	}
}

// TestPiBuildResumeCommandGolden pins the resume command strings. pi resumes by its UUIDv7 via
// `--session <id>` (installed 0.73.1; --session-id is ≥0.76/Node22); an unknown/invalid id falls
// back to `--continue` (most recent for cwd). The initial prompt (a resume nudge) trails.
func TestPiBuildResumeCommandGolden(t *testing.T) {
	ad := piAdapter{}

	got, _ := ad.BuildResumeCommand("pi", LaunchSpec{}, piUUIDv7)
	want := "pi --session " + piUUIDv7
	if got != want {
		t.Errorf("resume uuidv7\n got: %q\nwant: %q", got, want)
	}

	got, _ = ad.BuildResumeCommand("pi", LaunchSpec{}, "short123")
	want = "pi --continue"
	if got != want {
		t.Errorf("resume non-uuid falls back to --continue\n got: %q\nwant: %q", got, want)
	}

	got, _ = ad.BuildResumeCommand("pi", LaunchSpec{}, "")
	if got != want {
		t.Errorf("empty resume id falls back to --continue\n got: %q\nwant: %q", got, want)
	}

	// resume nudge prompt trails the id
	dir := t.TempDir()
	nudge := filepath.Join(dir, "nudge.txt")
	if err := os.WriteFile(nudge, []byte("continue where you left off"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _ = ad.BuildResumeCommand("pi", LaunchSpec{InitialPromptFile: nudge}, piUUIDv7)
	want = "pi --session " + piUUIDv7 + " \"$(cat '" + nudge + "')\""
	if got != want {
		t.Errorf("resume with nudge\n got: %q\nwant: %q", got, want)
	}

	// The station system prompt + model are re-applied on resume, before the positional --session.
	got, _ = ad.BuildResumeCommand("pi", LaunchSpec{Model: "pks-foundry/gpt-5.5", SystemPromptFile: "/tmp/sys.txt"}, piUUIDv7)
	want = "pi --model 'pks-foundry/gpt-5.5' --append-system-prompt \"$(cat '/tmp/sys.txt')\" --session " + piUUIDv7
	if got != want {
		t.Errorf("resume with model + system prompt\n got: %q\nwant: %q", got, want)
	}
}

// TestForResolvesPi ensures VIBECAST_AGENT=pi resolves to the pi adapter.
func TestForResolvesPi(t *testing.T) {
	ad, err := For(KindPi)
	if err != nil {
		t.Fatalf("pi should resolve: %v", err)
	}
	if ad.Kind() != KindPi {
		t.Errorf("Kind() = %q, want pi", ad.Kind())
	}
	if ad.BinaryName() != "pi" {
		t.Errorf("BinaryName() = %q, want pi", ad.BinaryName())
	}
	if !ad.DiscoversOwnSessionID() {
		t.Errorf("pi DiscoversOwnSessionID() = false, want true (discover-identity via session_start)")
	}
}
