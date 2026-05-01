package cmd

import (
	"fmt"

	"github.com/pksorensen/vibecast/internal/fkeybar"
)

// RunViewers prints the current viewer count to stdout, with no trailing
// newline so it composes cleanly inside tmux's `#()` status format.
//
// Returns silently with `0` on any error so the status bar never shows
// scary text when the control socket is briefly unavailable.
//
// Usage:
//
//	vibecast viewers
func RunViewers() {
	c := fkeybar.NewClient()
	s, err := c.GetStatus()
	if err != nil || s == nil {
		fmt.Print("0")
		return
	}
	fmt.Printf("%d", s.Viewers)
}
