package agent

import "testing"

func TestClaudeModelFlag(t *testing.T) {
	cases := []struct {
		name  string
		model string
		tier  string
		want  string
	}{
		{"unset", "", "", ""},
		{"tier opus", "", "opus", " --model opus"},
		{"tier sonnet", "", "sonnet", " --model sonnet"},
		{"tier haiku", "", "haiku", " --model haiku"},
		{"tier case-insensitive", "", "Opus", " --model opus"},
		{"tier unknown dropped", "", "gpt-5", ""},
		{"specific model wins over tier", "claude-opus-4-8", "sonnet", " --model 'claude-opus-4-8'"},
		{"specific alias", "haiku", "", " --model 'haiku'"},
		{"specific trimmed", "  opus  ", "", " --model 'opus'"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := claudeModelFlag(c.model, c.tier); got != c.want {
				t.Errorf("claudeModelFlag(%q, %q) = %q, want %q", c.model, c.tier, got, c.want)
			}
		})
	}
}

func TestClaudeEffortFlag(t *testing.T) {
	cases := []struct {
		name   string
		effort string
		want   string
	}{
		{"unset", "", ""},
		{"low", "low", " --effort low"},
		{"high", "high", " --effort high"},
		{"xhigh", "xhigh", " --effort xhigh"},
		{"max", "max", " --effort max"},
		{"case-insensitive", "High", " --effort high"},
		{"trimmed", "  medium  ", " --effort medium"},
		{"unknown dropped", "ultracode", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := claudeEffortFlag(c.effort); got != c.want {
				t.Errorf("claudeEffortFlag(%q) = %q, want %q", c.effort, got, c.want)
			}
		})
	}
}
