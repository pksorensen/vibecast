package broadcast

import (
	"regexp"
	"strings"
)

// Claude prints its sign-in URL as an OSC 8 hyperlink:
//
//	ESC ] 8 ; id=xxx ; <URL> ESC \  <display text>  ESC ] 8 ; ; ESC \
//
// Two properties of that shape decide how it must be read. The target is one
// unbroken string, while the display copy is laid out to the pane width — so on a
// narrow pane it is the display copy that loses its tail. And ESC is not
// whitespace, so a `\S+` match started inside the target runs straight through the
// terminator and swallows the display copy too.
//
// Measured in a 120-column job pane on 25-11-2026, that is exactly what reached the
// dashboard's "Open sign-in URL" button:
//
//	https://claude.ai/oauth/authorize?z-…%1B\https://claude.ai/oauth/authorize?z-…%1B]8;;%1B
//
// claude.ai answered "Invalid OAuth request — Missing client_id parameter", so the
// gate surfaced a button that could not work.
var (
	// osc8TargetRe captures the link target. Terminator is ESC \ (ST) or BEL.
	osc8TargetRe = regexp.MustCompile("\x1b\\]8;[^;]*;([^\x1b\a]*)(?:\x1b\\\\|\a)")
	// oauthPlainRe is the fallback for a pane with no hyperlink. It excludes ESC
	// from the match so a stray escape cannot be glued onto the URL.
	oauthPlainRe = regexp.MustCompile(`https?://[^\s\x1b]+oauth/authorize\?[^\s\x1b]+`)
)

// extractOAuthURL pulls claude's sign-in URL out of a pane capture. rendered is the
// raw capture (escapes intact); plain is the same text with CSI codes stripped.
// Returns "" when no URL is present, which the caller reports as a failed
// extraction rather than posting an empty action URL.
func extractOAuthURL(rendered, plain string) string {
	for _, m := range osc8TargetRe.FindAllStringSubmatch(rendered, -1) {
		if strings.Contains(m[1], "oauth/authorize") {
			return m[1]
		}
	}
	// Newlines go first: a URL too long for the pane is wrapped across visual lines.
	return oauthPlainRe.FindString(strings.ReplaceAll(plain, "\n", ""))
}
