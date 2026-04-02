package cmd

import (
	"fmt"
	"os"

	"github.com/pksorensen/vibecast/internal/mcp"
)

// RunMCP handles the "vibecast mcp" subcommand.
func RunMCP() {
	if len(os.Args) > 2 && os.Args[2] == "serve" {
		mcp.HandleMCPServe()
		return
	}
	fmt.Fprintf(os.Stderr, "usage: vibecast mcp serve\n")
	os.Exit(1)
}
