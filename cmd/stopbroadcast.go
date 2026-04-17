package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/pksorensen/vibecast/internal/control"
)

// RunStopBroadcast handles the "vibecast stop-broadcast" subcommand.
// It signals the running vibecast process to stop broadcasting via the
// control socket, mirroring the MCP stop_broadcast tool.
func RunStopBroadcast() {
	var message, conclusion string

	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--message":
			if i+1 < len(args) {
				i++
				message = args[i]
			}
		case "--conclusion":
			if i+1 < len(args) {
				i++
				conclusion = args[i]
			}
		default:
			if strings.HasPrefix(args[i], "--message=") {
				message = strings.TrimPrefix(args[i], "--message=")
			} else if strings.HasPrefix(args[i], "--conclusion=") {
				conclusion = strings.TrimPrefix(args[i], "--conclusion=")
			}
		}
	}

	sockPath := control.ControlSocketPath()
	if _, err := os.Stat(sockPath); err != nil {
		fmt.Fprintf(os.Stderr, "error: vibecast control socket not found at %s\n", sockPath)
		fmt.Fprintf(os.Stderr, "Is vibecast running?\n")
		os.Exit(1)
	}

	payload, _ := json.Marshal(map[string]string{
		"message":    message,
		"conclusion": conclusion,
	})

	resp, err := control.ControlHTTPRequestWithBody(sockPath, "POST", "/stop-broadcast", payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to send stop-broadcast: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(resp)
}
