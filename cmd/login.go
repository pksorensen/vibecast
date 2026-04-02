package cmd

import "github.com/pksorensen/vibecast/internal/auth"

// RunLogin handles the "vibecast login" subcommand.
func RunLogin() {
	auth.HandleLoginCommand()
}
