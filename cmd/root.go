package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/pksorensen/vibecast/internal/broadcast"
	"github.com/pksorensen/vibecast/internal/control"
	"github.com/pksorensen/vibecast/internal/mcp"
	"github.com/pksorensen/vibecast/internal/session"
	"github.com/pksorensen/vibecast/internal/stream"
	"github.com/pksorensen/vibecast/internal/telemetry"
	"github.com/pksorensen/vibecast/internal/tui"
	"github.com/pksorensen/vibecast/internal/types"
	"github.com/pksorensen/vibecast/internal/util"
)

// Execute is the main entry point for the CLI.
func Execute() {
	// Subcommand routing
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "hook":
			RunHook()
			return
		case "login":
			RunLogin()
			return
		case "logout":
			RunLogout()
			return
		case "mcp":
			RunMCP()
			return
		case "fkeybar":
			RunFKeyBar()
			return
		case "sync":
			RunSync()
			return
		case "stop-broadcast":
			RunStopBroadcast()
			return
		case "select-workspace":
			stream.SelectWorkspaceWindow()
			return
		}
	}

	// Parse flags
	var resumeStreamID string
	var resumeMode bool
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "--stream-id" && i+1 < len(os.Args) {
			resumeStreamID = os.Args[i+1]
			break
		}
		if os.Args[i] == "--resume" && i+1 < len(os.Args) {
			resumeStreamID = util.ExtractStreamID(os.Args[i+1])
			resumeMode = true
			break
		}
	}
	if resumeStreamID == "" {
		resumeStreamID = os.Getenv("STREAM_ID")
	}
	// Runner passes the prior Claude session ID so vibecast can resume it via --resume
	claudeResumeID := os.Getenv("VIBECAST_RESUME_SESSION_ID")

	// Initialize OpenTelemetry
	otelShutdown, _ := telemetry.InitOTEL(context.Background())

	// Clean up stale sessions
	session.CleanStaleSessions()

	status := &types.SharedStatus{Phase: "menu", OtelShutdown: otelShutdown}
	defer func() {
		status.Mu.Lock()
		fn := status.OtelShutdown
		status.Mu.Unlock()
		if fn != nil {
			fn()
		}
	}()

	// Create a headless Bubble Tea program (no terminal, just message processing)
	m := tui.WaitingModel(status)
	m.ResumeStreamID = resumeStreamID
	m.ResumeMode = resumeMode
	m.ClaudeResumeID = claudeResumeID

	p := tea.NewProgram(m, tea.WithoutRenderer(), tea.WithInput(nil))

	// Start control server BEFORE tmux session so fkeybar can connect
	ctrlListener, err := control.StartControlServer(status, p, stream.DoRestartClaude)
	if err != nil {
		// Only log control server errors in debug mode
		if os.Getenv("VIBECAST_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "[control] failed to start: %v\n", err)
		}
	}

	// Get our own executable path for spawning fkeybar
	vibecastPath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[error] failed to get executable path: %v\n", err)
		os.Exit(1)
	}

	// Create lobby tmux session with fkeybar --menu as the only pane
	lobbySession := "vibecast-lobby"
	exec.Command("tmux", "kill-session", "-t", lobbySession).Run()

	// Pass TERM_PROGRAM directly as an env var to fkeybar, since tmux set-environment
	// only affects NEW processes, not the one we're launching now.
	fkeybarCmd := vibecastPath + " fkeybar --info"
	tp := os.Getenv("TERM_PROGRAM")
	if tp != "" {
		fkeybarCmd = "TERM_PROGRAM=" + tp + " " + fkeybarCmd
	}
	cmd := exec.Command("tmux", "new-session", "-d", "-s", lobbySession, "-n", "info",
		"sh", "-c", fkeybarCmd,
	)
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "[error] failed to create tmux session: %v\n", err)
		fmt.Fprintf(os.Stderr, "Make sure tmux is installed.\n")
		os.Exit(1)
	}

	// Create "help" window in lobby
	helpCmd := vibecastPath + " fkeybar --help-screen"
	if tp != "" {
		helpCmd = "TERM_PROGRAM=" + tp + " " + helpCmd
	}
	exec.Command("tmux", "new-window", "-t", lobbySession, "-n", "help",
		"sh", "-c", helpCmd).Run()
	// Switch back to info window
	exec.Command("tmux", "select-window", "-t", lobbySession+":info").Run()

	// Propagate TERM_PROGRAM so fkeybar can detect VS Code
	if tp := os.Getenv("TERM_PROGRAM"); tp != "" {
		exec.Command("tmux", "set-environment", "-t", lobbySession, "TERM_PROGRAM", tp).Run()
	}

	// Style the lobby session
	exec.Command("tmux", "set-option", "-t", lobbySession, "window-size", "largest").Run()
	exec.Command("tmux", "set-option", "-t", lobbySession, "status", "on").Run()
	exec.Command("tmux", "set-option", "-t", lobbySession, "status-style", "bg=#FF6B00,fg=#000000,bold").Run()
	exec.Command("tmux", "set-option", "-t", lobbySession, "status-left", " AGENTIC LIVE ").Run()
	exec.Command("tmux", "set-option", "-t", lobbySession, "status-right", " Ctrl-b d → detach ").Run()
	exec.Command("tmux", "set-option", "-t", lobbySession, "status-justify", "centre").Run()

	// Bind F-keys in the lobby session
	stream.BindFKeys(lobbySession)

	cleanup := func() {
		control.CleanupControlSocket(ctrlListener)
		mcp.RemoveMCPConfig()
		exec.Command("tmux", "kill-session", "-t", lobbySession).Run()
		dir := session.SessionsDir()
		entries, _ := os.ReadDir(dir)
		myPID := os.Getpid()
		for _, e := range entries {
			if sf, err := session.ReadSessionFile(filepath.Join(dir, e.Name())); err == nil {
				if sf.PID == myPID {
					os.Remove(filepath.Join(dir, e.Name()))
				}
			}
		}
	}

	// Handle signals for cleanup
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cleanup()
		os.Exit(0)
	}()

	// Start chat connection goroutine — use the live stream ID once streaming starts.
	go func() {
		for i := 0; i < 60; i++ {
			time.Sleep(2 * time.Second)
			status.Mu.Lock()
			chatStreamID := status.StreamID
			status.Mu.Unlock()
			if chatStreamID == "" {
				chatStreamID = "default"
			}
			broadcast.ConnectChat(chatStreamID, p)
		}
	}()

	// Run the headless TUI in a goroutine
	done := make(chan struct{})
	go func() {
		p.Run()
		close(done)
	}()

	// Attach to the lobby tmux session — this is what the user sees
	if os.Getenv("TMUX") != "" {
		// Already inside tmux — use switch-client instead of attach
		switchCmd := exec.Command("tmux", "switch-client", "-t", lobbySession)
		if err := switchCmd.Run(); err != nil {
			_ = err // switch-client may fail if session doesn't exist yet
		}
		// Wait for the lobby session to end (fkeybar quit or user detach)
		for {
			time.Sleep(1 * time.Second)
			if err := exec.Command("tmux", "has-session", "-t", lobbySession).Run(); err != nil {
				break
			}
		}
	} else {
		attach := exec.Command("tmux", "attach", "-t", lobbySession)
		attach.Stdin = os.Stdin
		attach.Stdout = os.Stdout
		attach.Stderr = os.Stderr
		attach.Env = append(util.FilterEnv(os.Environ(), "TMUX"), "TERM="+os.Getenv("TERM"))
		attach.Run()
	}

	// Lobby ended. Check if streaming is active — if so, attach to the streaming session.
	// If not (user selected Quit), shut down.
	status.Mu.Lock()
	phase := status.Phase
	streamID := status.StreamID
	status.Mu.Unlock()

	if phase == "live" || phase == "starting" {
		streamingSession := "vibecast-" + streamID
		// Wait for streaming session to exist (may still be creating)
		for i := 0; i < 30; i++ {
			if exec.Command("tmux", "has-session", "-t", streamingSession).Run() == nil {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		// Attach to the streaming session — this is what the user sees now.
		// When they detach or the session ends, we clean up.
		if os.Getenv("TMUX") != "" {
			// Already inside tmux — switch-client
			exec.Command("tmux", "switch-client", "-t", streamingSession).Run()
			for {
				time.Sleep(1 * time.Second)
				if err := exec.Command("tmux", "has-session", "-t", streamingSession).Run(); err != nil {
					break
				}
			}
		} else {
			attach := exec.Command("tmux", "attach", "-t", streamingSession)
			attach.Stdin = os.Stdin
			attach.Stdout = os.Stdout
			attach.Stderr = os.Stderr
			attach.Env = append(util.FilterEnv(os.Environ(), "TMUX"), "TERM="+os.Getenv("TERM"))
			attach.Run()
		}
	}

	// Shut down
	p.Send(types.ControlStopMsg{})

	// Wait for the TUI to finish (up to 5s)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}

	cleanup()
}
