package broadcast

import (
	"regexp"
	"strings"
	"testing"
)

// The exact bytes captured from a live job pane on 25-11-2026 (tmux capture-pane -e,
// 120 columns, claude 2.1.332): a CSI color code, the OSC 8 hyperlink with the URL as
// its target, the same URL again as display text, then the hyperlink close.
const paneWithHyperlink = "\x1b[38;5;246m\x1b]8;id=1rfycga;" +
	"https://claude.ai/oauth/authorize?z-BNKsYBb1HhhMjovMmjEHrVkadkWItiDO7D9lrBrga6k" +
	"\x1b\\https://claude.ai/oauth/authorize?z-BNKsYBb1HhhMjovMmjEHrVkadkWItiDO7D9lrBrga6k" +
	"\x1b[39m\x1b]8;;\x1b\\\n\n Paste code here if prompted\n"

const wantURL = "https://claude.ai/oauth/authorize?z-BNKsYBb1HhhMjovMmjEHrVkadkWItiDO7D9lrBrga6k"

func csiStripped(s string) string {
	return regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`).ReplaceAllString(s, "")
}

func TestExtractOAuthURLFromHyperlink(t *testing.T) {
	got := extractOAuthURL(paneWithHyperlink, csiStripped(paneWithHyperlink))
	if got != wantURL {
		t.Fatalf("got %q, want %q", got, wantURL)
	}
}

// Guards the regression itself: whatever we hand the dashboard must be one URL, with
// no escape bytes and no second copy glued on. The old `\S+` match failed all three.
func TestExtractOAuthURLCarriesNoEscapes(t *testing.T) {
	got := extractOAuthURL(paneWithHyperlink, csiStripped(paneWithHyperlink))
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("URL carries an ESC byte: %q", got)
	}
	if strings.Count(got, "https://") != 1 {
		t.Errorf("URL is doubled: %q", got)
	}
	if strings.Contains(got, "]8;") {
		t.Errorf("URL carries an OSC terminator: %q", got)
	}
}

// Demonstrates what the previous pattern did with the same input, so the test says why
// the helper exists rather than only that it works.
func TestOldPatternSwallowedTheHyperlink(t *testing.T) {
	old := regexp.MustCompile(`https?://[^\s]+oauth/authorize\?\S+`)
	got := old.FindString(strings.ReplaceAll(csiStripped(paneWithHyperlink), "\n", ""))
	if !strings.ContainsRune(got, 0x1b) {
		t.Skip("the old pattern no longer reproduces the bug on this input")
	}
	if strings.Count(got, "https://") < 2 {
		t.Errorf("expected the old pattern to swallow both copies, got %q", got)
	}
}

// A pane without hyperlink support: the URL is plain text, wrapped by the pane width.
func TestExtractOAuthURLFallsBackToWrappedPlainText(t *testing.T) {
	pane := " Browser didn't open? Use the URL below to sign in\nhttps://claude.ai/oauth/authorize?code=true&client_id=abc\n&response_type=code&state=xyz\n Paste code here\n"
	got := extractOAuthURL(pane, pane)
	want := "https://claude.ai/oauth/authorize?code=true&client_id=abc&response_type=code&state=xyz"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExtractOAuthURLReturnsEmptyWhenAbsent(t *testing.T) {
	pane := "\x1b[38;5;246m Welcome to Claude Code\x1b[39m\n esc to interrupt\n"
	if got := extractOAuthURL(pane, csiStripped(pane)); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// An OSC 8 sequence terminated by BEL instead of ESC \ — both are legal.
func TestExtractOAuthURLAcceptsBelTerminator(t *testing.T) {
	pane := "\x1b]8;;" + wantURL + "\asign in\x1b]8;;\a"
	if got := extractOAuthURL(pane, pane); got != wantURL {
		t.Fatalf("got %q, want %q", got, wantURL)
	}
}
