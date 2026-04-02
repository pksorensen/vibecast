package cmd

import (
	"fmt"
	"os"
	"runtime/debug"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/pksorensen/vibecast/internal/fkeybar"
)

// RunFKeyBar runs the fkeybar subcommand.
func RunFKeyBar() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "\n[fkeybar] PANIC: %v\n%s\n", r, debug.Stack())
			os.Exit(1)
		}
	}()

	var streamID string
	var infoMode bool
	var helpMode bool

	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--stream-id":
			if i+1 < len(os.Args) {
				streamID = os.Args[i+1]
				i++
			}
		case "--info":
			infoMode = true
		// Keep --menu as alias for --info for backwards compat
		case "--menu":
			infoMode = true
		case "--help-screen":
			helpMode = true
		}
	}

	var model tea.Model
	if helpMode {
		model = fkeybar.NewHelpModel()
	} else if infoMode {
		model = fkeybar.NewInfoModel(streamID)
	} else {
		model = fkeybar.NewBarModel(streamID)
	}

	p := tea.NewProgram(model)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "[fkeybar] error: %v\n", err)
		os.Exit(1)
	}
}
