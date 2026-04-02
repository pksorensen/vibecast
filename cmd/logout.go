package cmd

import "github.com/pksorensen/vibecast/internal/auth"

// RunLogout handles the "vibecast logout" subcommand.
func RunLogout() {
	auth.HandleLogoutCommand()
}
