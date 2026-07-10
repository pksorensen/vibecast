package stream

import "testing"

// TestSpecFromEnv covers the env boundary: specFromEnv reads the per-station launch
// configuration from the environment into the agent-neutral LaunchSpec. The flag-string
// logic itself is tested in internal/agent (claude_flags_test.go, claude_test.go).
func TestSpecFromEnv(t *testing.T) {
	t.Setenv("VIBECAST_CLAUDE_MODEL", "claude-opus-4-8")
	t.Setenv("VIBECAST_CLAUDE_MODEL_TIER", "sonnet")
	t.Setenv("VIBECAST_CLAUDE_EFFORT", "high")
	t.Setenv("VIBECAST_APPEND_SYSTEM_PROMPT_FILE", "/tmp/sp.txt")
	t.Setenv("VIBECAST_APPEND_SYSTEM_PROMPT", "inline")
	t.Setenv("VIBECAST_INITIAL_PROMPT_FILE", "/tmp/ip.txt")

	spec := specFromEnv()
	if spec.Model != "claude-opus-4-8" {
		t.Errorf("Model = %q", spec.Model)
	}
	if spec.ModelTier != "sonnet" {
		t.Errorf("ModelTier = %q", spec.ModelTier)
	}
	if spec.Effort != "high" {
		t.Errorf("Effort = %q", spec.Effort)
	}
	if spec.SystemPromptFile != "/tmp/sp.txt" {
		t.Errorf("SystemPromptFile = %q", spec.SystemPromptFile)
	}
	if spec.SystemPromptInline != "inline" {
		t.Errorf("SystemPromptInline = %q", spec.SystemPromptInline)
	}
	if spec.InitialPromptFile != "/tmp/ip.txt" {
		t.Errorf("InitialPromptFile = %q", spec.InitialPromptFile)
	}
}

// TestSpecFromEnvExtraPlugins verifies VIBECAST_EXTRA_PLUGINS is split on ':', trimmed,
// and empties are skipped. (The telemetry plugin dir, prepended first, is absent in tests.)
func TestSpecFromEnvExtraPlugins(t *testing.T) {
	t.Setenv("VIBECAST_EXTRA_PLUGINS", "/a : /b::/c")
	spec := specFromEnv()
	want := []string{"/a", "/b", "/c"}
	if len(spec.PluginDirs) != len(want) {
		t.Fatalf("PluginDirs = %v, want %v", spec.PluginDirs, want)
	}
	for i, w := range want {
		if spec.PluginDirs[i] != w {
			t.Errorf("PluginDirs[%d] = %q, want %q", i, spec.PluginDirs[i], w)
		}
	}
}
