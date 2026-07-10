package agent

import (
	"os"
	"path/filepath"
	"testing"
)

const uuid = "c5e6a2bc-5aba-43fc-9d86-b8d66b481d39"

// TestClaudeBuildCommandGolden pins the exact launch command strings. These are the
// byte-for-byte outputs the previous inline stream.go builders produced; any drift here
// is a behavior change to the Claude launch path, not a refactor.
func TestClaudeBuildCommandGolden(t *testing.T) {
	ad := claudeAdapter{}
	tests := []struct {
		name string
		spec LaunchSpec
		want string
	}{
		{
			name: "bare with uuid session id",
			spec: LaunchSpec{AgentSessionID: uuid},
			want: "claude --dangerously-skip-permissions --session-id " + uuid,
		},
		{
			name: "non-uuid session id dropped",
			spec: LaunchSpec{AgentSessionID: "short123"},
			want: "claude --dangerously-skip-permissions",
		},
		{
			name: "no session id",
			spec: LaunchSpec{},
			want: "claude --dangerously-skip-permissions",
		},
		{
			name: "explicit model wins over tier",
			spec: LaunchSpec{Model: "claude-opus-4-8", ModelTier: "sonnet", AgentSessionID: uuid},
			want: "claude --dangerously-skip-permissions --model 'claude-opus-4-8' --session-id " + uuid,
		},
		{
			name: "model with single quote is escaped",
			spec: LaunchSpec{Model: "weird'model"},
			want: "claude --dangerously-skip-permissions --model 'weird'\"'\"'model'",
		},
		{
			name: "known tier maps to alias",
			spec: LaunchSpec{ModelTier: "Opus"},
			want: "claude --dangerously-skip-permissions --model opus",
		},
		{
			name: "unknown tier dropped",
			spec: LaunchSpec{ModelTier: "titanium"},
			want: "claude --dangerously-skip-permissions",
		},
		{
			name: "valid effort",
			spec: LaunchSpec{Effort: "HIGH"},
			want: "claude --dangerously-skip-permissions --effort high",
		},
		{
			name: "invalid effort dropped",
			spec: LaunchSpec{Effort: "ludicrous"},
			want: "claude --dangerously-skip-permissions",
		},
		{
			name: "plugin dirs in order",
			spec: LaunchSpec{PluginDirs: []string{"/a/plugin", "/b/plugin"}},
			want: "claude --dangerously-skip-permissions --plugin-dir /a/plugin --plugin-dir /b/plugin",
		},
		{
			name: "inline system prompt",
			spec: LaunchSpec{SystemPromptInline: "be terse"},
			want: "claude --dangerously-skip-permissions --append-system-prompt 'be terse'",
		},
		{
			name: "system prompt file preferred over inline",
			spec: LaunchSpec{SystemPromptFile: "/tmp/sp.txt", SystemPromptInline: "ignored"},
			want: "claude --dangerously-skip-permissions --append-system-prompt \"$(cat '/tmp/sp.txt')\"",
		},
		{
			name: "full flag order",
			spec: LaunchSpec{
				PluginDirs:         []string{"/p"},
				Model:              "opus",
				Effort:             "max",
				SystemPromptInline: "sys",
				AgentSessionID:     uuid,
			},
			want: "claude --dangerously-skip-permissions --plugin-dir /p --model 'opus' --effort max --append-system-prompt 'sys' --session-id " + uuid,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ad.BuildCommand("claude", tt.spec)
			if err != nil {
				t.Fatalf("BuildCommand error: %v", err)
			}
			if got != tt.want {
				t.Errorf("BuildCommand mismatch\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

// TestClaudeInitialPromptArg pins the file-stat drop behavior: a present non-empty file is
// appended as a positional arg; a missing or empty file is dropped.
func TestClaudeInitialPromptArg(t *testing.T) {
	ad := claudeAdapter{}
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

	got, _ := ad.BuildCommand("claude", LaunchSpec{InitialPromptFile: present})
	want := "claude --dangerously-skip-permissions \"$(cat '" + present + "')\""
	if got != want {
		t.Errorf("present prompt file\n got: %q\nwant: %q", got, want)
	}

	for _, f := range []string{empty, missing, ""} {
		got, _ := ad.BuildCommand("claude", LaunchSpec{InitialPromptFile: f})
		if got != "claude --dangerously-skip-permissions" {
			t.Errorf("prompt file %q should be dropped, got: %q", f, got)
		}
	}
}

// TestClaudeBuildResumeCommandGolden pins the resume/continue command strings, including
// the initial-prompt ordering (after --resume/--continue) that differs from a fresh launch.
func TestClaudeBuildResumeCommandGolden(t *testing.T) {
	ad := claudeAdapter{}

	got, _ := ad.BuildResumeCommand("claude", LaunchSpec{}, uuid)
	want := "claude --dangerously-skip-permissions --resume " + uuid
	if got != want {
		t.Errorf("resume uuid\n got: %q\nwant: %q", got, want)
	}

	got, _ = ad.BuildResumeCommand("claude", LaunchSpec{}, "short123")
	want = "claude --dangerously-skip-permissions --continue"
	if got != want {
		t.Errorf("resume non-uuid falls back to --continue\n got: %q\nwant: %q", got, want)
	}

	got, _ = ad.BuildResumeCommand("claude", LaunchSpec{}, "")
	if got != want {
		t.Errorf("empty resume id falls back to --continue\n got: %q\nwant: %q", got, want)
	}

	got, _ = ad.BuildResumeCommand("claude", LaunchSpec{Model: "opus", SystemPromptInline: "sys"}, uuid)
	want = "claude --dangerously-skip-permissions --model 'opus' --append-system-prompt 'sys' --resume " + uuid
	if got != want {
		t.Errorf("resume with flags\n got: %q\nwant: %q", got, want)
	}
}

// TestForUnknownAgentFailsFast ensures an unrecognized VIBECAST_AGENT is rejected before
// any tmux session is created, and that claude/empty resolve to the Claude adapter.
func TestForUnknownAgentFailsFast(t *testing.T) {
	if _, err := For(KindClaude); err != nil {
		t.Errorf("claude should resolve: %v", err)
	}
	if _, err := For(""); err != nil {
		t.Errorf("empty should default to claude: %v", err)
	}
	if _, err := For(Kind("mystery")); err == nil {
		t.Error("unknown agent should fail fast")
	}
}
