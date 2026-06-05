package stream

import "testing"

// TestClaudeAutoUpdateDisabled locks the opt-out parsing: only the recognised
// truthy values disable the auto-update-to-latest behavior; everything else
// (including empty/unset) keeps the default ON.
func TestClaudeAutoUpdateDisabled(t *testing.T) {
	disabled := []string{"1", "true", "TRUE", "True", "yes", "YES", "on", "On", " 1 ", "  true  "}
	enabled := []string{"", "0", "false", "no", "off", "2", "disabled", "  ", "enable"}

	for _, v := range disabled {
		t.Setenv("CLAUDE_AUTO_UPDATE_DISABLED", v)
		if !claudeAutoUpdateDisabled() {
			t.Errorf("CLAUDE_AUTO_UPDATE_DISABLED=%q: expected disabled=true, got false", v)
		}
	}
	for _, v := range enabled {
		t.Setenv("CLAUDE_AUTO_UPDATE_DISABLED", v)
		if claudeAutoUpdateDisabled() {
			t.Errorf("CLAUDE_AUTO_UPDATE_DISABLED=%q: expected disabled=false, got true", v)
		}
	}
}
