package main

import (
	"os"

	"github.com/pksorensen/vibecast/cmd"
)

func init() {
	// Force a UTF-8 locale so transitive deps (lipgloss/termenv/runewidth and friends)
	// don't fall back to East-Asian-width or non-UTF-8 charset handling. In minimal
	// containers $LANG/$LC_CTYPE often default to "POSIX" / empty, which causes
	// reversed-video glyphs like ↑↓⏎●◀▶ to render as "__".
	if os.Getenv("LANG") == "" {
		_ = os.Setenv("LANG", "C.UTF-8")
	}
	if os.Getenv("LC_ALL") == "" {
		_ = os.Setenv("LC_ALL", "C.UTF-8")
	}
}

func main() {
	cmd.Execute()
}
