package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// RunCycleAgent cycles through tmux windows in the current session, but skips
// the special non-agent windows ("info", "help"). Direction must be "next" or "prev".
//
// Usage:
//
//	vibecast cycle-agent next
//	vibecast cycle-agent prev
//
// Bound to F3/F4 by stream.BindFKeys; tmux runs this via run-shell so it
// inherits $TMUX and can call back into tmux.
func RunCycleAgent(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: vibecast cycle-agent next|prev")
		os.Exit(1)
	}
	direction := args[0]
	if direction != "next" && direction != "prev" {
		fmt.Fprintln(os.Stderr, "direction must be next or prev")
		os.Exit(1)
	}

	// List windows in current session: "<index> <name> <active>"
	out, err := exec.Command("tmux", "list-windows",
		"-F", "#{window_index} #{window_name} #{window_active}").Output()
	if err != nil {
		// Not running in a tmux client — silently no-op
		return
	}

	type window struct {
		index  int
		name   string
		active bool
	}
	var agents []window
	currentAgentPos := -1

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 3 {
			continue
		}
		idx, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		name := parts[1]
		// Skip non-agent windows
		if name == "info" || name == "help" || name == "control" {
			continue
		}
		w := window{index: idx, name: name, active: parts[2] == "1"}
		if w.active {
			currentAgentPos = len(agents)
		}
		agents = append(agents, w)
	}

	if len(agents) == 0 {
		return
	}

	// If currently NOT on an agent window (e.g. on info/help), jump to the
	// first agent in the requested direction. "next" → first agent, "prev" → last.
	if currentAgentPos == -1 {
		if direction == "next" {
			currentAgentPos = -1 // so +1 = 0
		} else {
			currentAgentPos = len(agents) // so -1 = last
		}
	}

	var target int
	if direction == "next" {
		target = (currentAgentPos + 1) % len(agents)
	} else {
		target = (currentAgentPos - 1 + len(agents)) % len(agents)
	}

	exec.Command("tmux", "select-window", "-t", strconv.Itoa(agents[target].index)).Run()
}
