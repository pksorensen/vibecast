package broadcast

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestLiveTrustDialog drives answerMenu against a real Claude Code trust dialog.
//
// The unit tests above prove the navigation logic against a model of the menu; this proves
// the model matches what Claude actually renders — which is the assumption that broke.
// Opt-in because it needs tmux, a claude binary and a workspace claude has not been
// trusted for:
//
//	VIBECAST_LIVE_MENU_TEST=1 CLAUDE_CONFIG_DIR=... go test -run TestLiveTrustDialog ./internal/broadcast/
func TestLiveTrustDialog(t *testing.T) {
	if os.Getenv("VIBECAST_LIVE_MENU_TEST") != "1" {
		t.Skip("set VIBECAST_LIVE_MENU_TEST=1 to run against a real claude")
	}
	dir := t.TempDir()
	sess := "menulive"
	tmux := func(args ...string) *exec.Cmd { return exec.Command("tmux", args...) }
	tmux("kill-session", "-t", sess).Run()
	if err := tmux("new-session", "-d", "-s", sess, "-x", "120", "-y", "40",
		"sh", "-c", "cd "+dir+" && claude; sleep 120").Run(); err != nil {
		t.Fatalf("tmux new-session: %v", err)
	}
	defer tmux("kill-session", "-t", sess).Run()

	target := sess + ":0.0"
	var screen string
	for i := 0; i < 40; i++ {
		time.Sleep(500 * time.Millisecond)
		out, err := tmux("capture-pane", "-p", "-t", target).Output()
		if err != nil {
			continue
		}
		screen = string(out)
		if strings.Contains(screen, "Quick safety check") {
			break
		}
	}
	if !strings.Contains(screen, "Quick safety check") {
		t.Fatalf("trust dialog never appeared; last screen:\n%s", screen)
	}
	sel, ok := highlightedMenuOption(screen)
	t.Logf("dialog appeared with %q selected (ok=%v)", sel, ok)

	if !answerMenu(tmux, target, trustMenuOption) {
		out, _ := tmux("capture-pane", "-p", "-t", target).Output()
		t.Fatalf("answerMenu failed; screen:\n%s", string(out))
	}
	time.Sleep(3 * time.Second)
	out, _ := tmux("capture-pane", "-p", "-t", target).Output()
	after := string(out)
	if strings.Contains(after, "Quick safety check") {
		t.Fatalf("trust dialog still on screen after answering:\n%s", after)
	}
	t.Logf("after answering:\n%s", after)
}
