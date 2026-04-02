package cmd

import (
	"context"
	"os"

	"github.com/pksorensen/vibecast/internal/hooks"
	"github.com/pksorensen/vibecast/internal/telemetry"
)

// RunHook handles the "vibecast hook" subcommand.
func RunHook() {
	otelShutdown, _ := telemetry.InitOTEL(context.Background())
	defer otelShutdown()
	hooks.HandleHookCommand(os.Args[2:])
}
