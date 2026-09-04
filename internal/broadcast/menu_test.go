package broadcast

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestHighlightedMenuOption(t *testing.T) {
	// The workspace-trust dialog as Claude Code 2.1.260 actually renders it: unnumbered,
	// with the exit option selected. Pressing Enter here is what killed session 3y59cys8.
	trust := " Accessing workspace:\n /workspaces/kjeldager-drift\n\n ❯ No, exit\n   Yes, I trust this folder\n\n Enter to confirm · Esc to cancel\n"
	got, ok := highlightedMenuOption(trust)
	if !ok || got != "No, exit" {
		t.Fatalf("trust dialog: got %q, ok=%v; want %q", got, ok, "No, exit")
	}

	// Older, numbered rendering — the label carries its number, so callers must match on
	// a substring of the text rather than on the whole line.
	numbered := "Try the new fullscreen renderer?\n❯ 1. Yes, try it\n  2. Not now\n"
	got, ok = highlightedMenuOption(numbered)
	if !ok || !strings.Contains(got, "Yes, try it") {
		t.Fatalf("numbered menu: got %q, ok=%v", got, ok)
	}

	// Selection markers survive the colour codes Claude wraps them in.
	colored := "\x1b[1m ❯ \x1b[32mYes, I accept\x1b[0m\n   No, exit\n"
	if got, ok = highlightedMenuOption(colored); !ok || got != "Yes, I accept" {
		t.Fatalf("coloured menu: got %q, ok=%v", got, ok)
	}

	// No menu on screen at all.
	if _, ok = highlightedMenuOption("just a prompt\n"); ok {
		t.Fatal("expected no highlighted option on a plain screen")
	}
}

// fakeTmux stands in for the tmux binary: capture-pane prints the current screen of a
// tiny menu model, and send-keys moves its cursor. Real tmux is not available in CI, and
// the behaviour under test is the navigation, not the subprocess.
type fakeTmux struct {
	options []string
	cursor  int
	wrap    bool
	entered bool
	sends   int
}

func (f *fakeTmux) render() string {
	var b strings.Builder
	for i, o := range f.options {
		if i == f.cursor {
			b.WriteString(" ❯ " + o + "\n")
		} else {
			b.WriteString("   " + o + "\n")
		}
	}
	return b.String()
}

func (f *fakeTmux) cmd(args ...string) *exec.Cmd {
	switch args[0] {
	case "capture-pane":
		return exec.Command("printf", "%s", f.render())
	case "send-keys":
		f.sends++
		switch args[len(args)-1] {
		case "Down":
			if f.cursor+1 < len(f.options) {
				f.cursor++
			} else if f.wrap {
				f.cursor = 0
			}
		case "Up":
			if f.cursor > 0 {
				f.cursor--
			} else if f.wrap {
				f.cursor = len(f.options) - 1
			}
		case "Enter":
			f.entered = true
		}
		return exec.Command("true")
	}
	return exec.Command("true")
}

func TestAnswerMenuNavigatesToOption(t *testing.T) {
	// Exactly the case that broke: the wanted option sits below a selected "No, exit".
	f := &fakeTmux{options: []string{"No, exit", "Yes, I trust this folder"}}
	if !answerMenu(f.cmd, "t", trustMenuOption) {
		t.Fatal("expected the trust option to be selected")
	}
	if !f.entered || f.cursor != 1 {
		t.Fatalf("entered=%v cursor=%d; want Enter pressed on option 1", f.entered, f.cursor)
	}
}

func TestAnswerMenuReversesWhenListDoesNotWrap(t *testing.T) {
	// Wanted option above the selection, in a list that stops at the bottom rather than
	// wrapping — pressing Down forever would never reach it.
	f := &fakeTmux{options: []string{"Yes, I accept", "No, exit"}, cursor: 1}
	if !answerMenu(f.cmd, "t", bypassMenuOption) {
		t.Fatal("expected the accept option to be selected")
	}
	if !f.entered || f.cursor != 0 {
		t.Fatalf("entered=%v cursor=%d; want Enter pressed on option 0", f.entered, f.cursor)
	}
}

func TestAnswerMenuRefusesRatherThanGuessing(t *testing.T) {
	// The option is not on screen. Pressing Enter anyway is the bug this replaces, so the
	// contract is: leave the dialog alone and report failure.
	f := &fakeTmux{options: []string{"No, exit", "Something else"}}
	if answerMenu(f.cmd, "t", trustMenuOption) {
		t.Fatal("expected answerMenu to give up")
	}
	if f.entered {
		t.Fatal("answerMenu pressed Enter on an option it was not asked for")
	}
}

func TestMain(m *testing.M) { os.Exit(m.Run()) }
