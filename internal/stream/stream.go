package stream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/pksorensen/vibecast/internal/auth"
	"github.com/pksorensen/vibecast/internal/broadcast"
	"github.com/pksorensen/vibecast/internal/control"
	"github.com/pksorensen/vibecast/internal/hooks"
	"github.com/pksorensen/vibecast/internal/session"
	"github.com/pksorensen/vibecast/internal/telemetry"
	"github.com/pksorensen/vibecast/internal/types"
	"github.com/pksorensen/vibecast/internal/util"
)

var debugLog = os.Getenv("VIBECAST_DEBUG") != ""

func logDebug(format string, args ...interface{}) {
	if debugLog {
		fmt.Fprintf(os.Stderr, format, args...)
	}
}

func buildPluginFlags() string {
	flags := ""
	if dir := telemetry.PluginDir(); dir != "" {
		flags += " --plugin-dir " + dir
	}
	if extra := os.Getenv("VIBECAST_EXTRA_PLUGINS"); extra != "" {
		for _, dir := range strings.Split(extra, ":") {
			dir = strings.TrimSpace(dir)
			if dir != "" {
				flags += " --plugin-dir " + dir
			}
		}
	}
	return flags
}

func buildAppendSystemPromptFlag() string {
	// Prefer file-based approach to avoid shell quoting issues with special chars/JSON
	if file := os.Getenv("VIBECAST_APPEND_SYSTEM_PROMPT_FILE"); file != "" {
		escapedPath := strings.ReplaceAll(file, "'", "'\"'\"'")
		return " --append-system-prompt \"$(cat '" + escapedPath + "')\""
	}
	if prompt := os.Getenv("VIBECAST_APPEND_SYSTEM_PROMPT"); prompt != "" {
		// Shell-escape single quotes in the prompt value
		escaped := strings.ReplaceAll(prompt, "'", "'\"'\"'")
		return " --append-system-prompt '" + escaped + "'"
	}
	return ""
}

// buildInitialPromptArg returns a shell fragment that passes the initial job prompt as
// a positional argument to Claude. The prompt is read from the file path set in
// VIBECAST_INITIAL_PROMPT_FILE so that arbitrary multi-line content is handled safely
// without shell-escaping or tmux send-keys timing issues.
func buildInitialPromptArg() string {
	if file := os.Getenv("VIBECAST_INITIAL_PROMPT_FILE"); file != "" {
		// Shell-escape single quotes in the file path
		escapedPath := strings.ReplaceAll(file, "'", "'\"'\"'")
		// "$(cat 'path')" expands to file content as a single argument, preserving newlines.
		// Claude Code treats this as the initial user message and stays interactive.
		return " \"$(cat '" + escapedPath + "')\""
	}
	return ""
}

func buildClaudeCommand(claudePath string, sessionID string) string {
	cmd := claudePath + " --dangerously-skip-permissions"
	cmd += buildPluginFlags()
	cmd += buildAppendSystemPromptFlag()
	cmd += buildInitialPromptArg()
	if sessionID != "" {
		cmd += " --session-id " + sessionID
	}
	return cmd
}

func buildClaudeResumeCommand(claudePath string, sessionID string) string {
	pluginFlags := buildPluginFlags()
	promptFlag := buildAppendSystemPromptFlag()
	if sessionID != "" {
		return claudePath + " --dangerously-skip-permissions" + pluginFlags + promptFlag + " --resume " + sessionID
	}
	return claudePath + " --dangerously-skip-permissions" + pluginFlags + promptFlag + " --continue"
}

// DoRestartClaude performs the actual restart logic.
// Kills the existing Claude process and respawns the pane with a fresh Claude.
func DoRestartClaude(sessionName string, resume bool, claudeSessionID string, paneId ...string) error {
	windowName := "main"
	if len(paneId) > 0 && paneId[0] != "" {
		windowName = paneId[0]
	}
	target := sessionName + ":" + windowName + ".0" // top pane (Claude), not the fkeybar
	logDebug("[restart] starting restart for target=%s\n", target)

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		logDebug("[restart] claude not found: %v\n", err)
		return fmt.Errorf("claude not found: %w", err)
	}

	var newCmd string
	if resume && claudeSessionID != "" {
		newCmd = buildClaudeResumeCommand(claudePath, claudeSessionID)
	} else {
		newCmd = buildClaudeCommand(claudePath, claudeSessionID)
	}

	// cd to git root
	if gitRoot, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
		root := strings.TrimSpace(string(gitRoot))
		newCmd = "cd " + root + " && " + newCmd
	}

	logDebug("[restart] respawning pane with: %s\n", newCmd)

	// respawn-pane -k kills the existing process and starts a new one in the same pane
	out, err := exec.Command("tmux", "respawn-pane", "-k", "-t", target,
		"sh", "-c", newCmd).CombinedOutput()
	if err != nil {
		logDebug("[restart] respawn-pane failed: %v (output: %s)\n", err, string(out))
		return fmt.Errorf("respawn-pane failed: %w", err)
	}

	logDebug("[restart] restart complete\n")
	return nil
}

// RestartClaude returns a tea.Cmd that restarts Claude.
func RestartClaude(sessionName string, resume bool, claudeSessionID string, activePaneId ...string) tea.Cmd {
	return func() tea.Msg {
		err := DoRestartClaude(sessionName, resume, claudeSessionID, activePaneId...)
		return types.ClaudeRestartedMsg{Err: err}
	}
}

// NotifyActivePaneChange sends an active_pane metadata message to viewers.
func NotifyActivePaneChange(panes []types.PaneInfo, activeIdx int) {
	if activeIdx >= len(panes) || len(panes) == 0 {
		return
	}
	metaMsg, _ := json.Marshal(map[string]interface{}{
		"type":   "active_pane",
		"paneId": panes[activeIdx].PaneId,
	})
	select {
	case panes[0].MetaCh <- metaMsg:
	default:
	}
}

// SelectWorkspaceWindow selects the most recently active tmux window
// that is not "info" or "help". Called as `vibecast select-workspace`.
func SelectWorkspaceWindow() {
	// Get all windows with activity timestamp, index, and name
	out, err := exec.Command("tmux", "list-windows", "-F", "#{window_activity} #{window_index} #{window_name}").Output()
	if err != nil {
		return
	}
	bestIdx := ""
	bestActivity := ""
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 3 {
			continue
		}
		activity, idx, name := parts[0], parts[1], parts[2]
		if name == "info" || name == "help" {
			continue
		}
		if bestActivity == "" || activity > bestActivity {
			bestActivity = activity
			bestIdx = idx
		}
	}
	if bestIdx != "" {
		exec.Command("tmux", "select-window", "-t", ":"+bestIdx).Run()
	}
}

// BindFKeys binds F-keys at the tmux session level.
// F1 = toggle between info and workspace (pure tmux, no curl).
// F3/F4 = prev/next window (pure tmux).
// F2, F5, F6, F10 = control socket actions.
// In VS Code, uses Ctrl-b + number keys since F-keys are intercepted.
func BindFKeys(sessionName string) {
	sockPath := control.ControlSocketPath()
	inVSCode := os.Getenv("TERM_PROGRAM") == "vscode"

	// F1 toggle: if on info → go to most recent workspace, else → go to info.
	// F9 toggle: if on help → go to most recent workspace, else → go to help.
	// Uses `vibecast select-workspace` to avoid shell escaping issues in tmux bindings.
	vibecastPath, _ := os.Executable()
	selectWorkspace := fmt.Sprintf(`run-shell '%s select-workspace'`, vibecastPath)

	toggleInfoCmd := fmt.Sprintf(`if-shell -F "#{==:#{window_name},info}" "%s" "select-window -t :info"`, selectWorkspace)
	toggleHelpCmd := fmt.Sprintf(`if-shell -F "#{==:#{window_name},help}" "%s" "select-window -t :help"`, selectWorkspace)

	// Actions that go through control socket
	curlAction := func(fkey string) string {
		return fmt.Sprintf("run-shell 'curl -s --unix-socket %s -X POST http://localhost/fkey?key=%s'", sockPath, fkey)
	}

	if inVSCode {
		// Ctrl-b + number keys
		exec.Command("tmux", "bind-key", "-T", "prefix", "1", toggleInfoCmd).Run()
		exec.Command("tmux", "bind-key", "-T", "prefix", "2", curlAction("f2")).Run()
		exec.Command("tmux", "bind-key", "-T", "prefix", "3", "previous-window").Run()
		exec.Command("tmux", "bind-key", "-T", "prefix", "4", "next-window").Run()
		exec.Command("tmux", "bind-key", "-T", "prefix", "5", curlAction("f5")).Run()
		exec.Command("tmux", "bind-key", "-T", "prefix", "6", curlAction("f6")).Run()
		exec.Command("tmux", "bind-key", "-T", "prefix", "9", toggleHelpCmd).Run()
		exec.Command("tmux", "bind-key", "-T", "prefix", "0", curlAction("f10")).Run()
	} else {
		// Raw F-keys (no prefix)
		exec.Command("tmux", "bind-key", "-n", "F1", toggleInfoCmd).Run()
		exec.Command("tmux", "bind-key", "-n", "F2", curlAction("f2")).Run()
		exec.Command("tmux", "bind-key", "-n", "F3", "previous-window").Run()
		exec.Command("tmux", "bind-key", "-n", "F4", "next-window").Run()
		exec.Command("tmux", "bind-key", "-n", "F5", curlAction("f5")).Run()
		exec.Command("tmux", "bind-key", "-n", "F6", curlAction("f6")).Run()
		exec.Command("tmux", "bind-key", "-n", "F9", toggleHelpCmd).Run()
		exec.Command("tmux", "bind-key", "-n", "F10", curlAction("f10")).Run()
	}
}

// SpawnFKeyBar splits a tmux window and runs fkeybar in the bottom pane (2 lines).
// Returns the tmux pane ID of the fkeybar pane.
func SpawnFKeyBar(sessionName, windowName, streamID string) string {
	target := sessionName + ":" + windowName
	vibecastPath, err := os.Executable()
	if err != nil {
		logDebug("[fkeybar] failed to get executable path: %v\n", err)
		return ""
	}

	// Split window vertically, run fkeybar in bottom pane (2 lines high)
	// Pass TERM_PROGRAM so fkeybar can detect VS Code and show ^b prefix
	fkeybarCmd := vibecastPath + " fkeybar --stream-id " + streamID
	if tp := os.Getenv("TERM_PROGRAM"); tp != "" {
		fkeybarCmd = "TERM_PROGRAM=" + tp + " " + fkeybarCmd
	}
	cmd := exec.Command("tmux", "split-window", "-v", "-l", "2",
		"-t", target,
		"sh", "-c", fkeybarCmd,
	)
	if err := cmd.Run(); err != nil {
		logDebug("[fkeybar] failed to split window %s: %v\n", windowName, err)
		return ""
	}

	// Get the pane ID of the newly created fkeybar pane (it's the active pane after split)
	out, err := exec.Command("tmux", "display-message", "-t", target, "-p", "#{pane_id}").Output()
	fkeybarPaneID := strings.TrimSpace(string(out))
	if err != nil || fkeybarPaneID == "" {
		logDebug("[fkeybar] failed to get fkeybar pane ID\n")
	}

	// Return focus to the top pane (Claude) — pane index 0 in this window
	exec.Command("tmux", "select-pane", "-t", target+".0").Run()

	return fkeybarPaneID
}

// SpawnPane creates a new tmux window, starts ttyd, launches Claude, and begins broadcast relay.
func SpawnPane(sessionName, streamID, paneId, name string, status *types.SharedStatus, claudeResumeID string) (*types.PaneInfo, error) {
	claudeSessionID := util.GenerateUUIDv4()
	if claudeResumeID != "" {
		claudeSessionID = claudeResumeID
	}
	logDebug("[pane:%s] claude session ID: %s (resume=%v)\n", paneId, claudeSessionID, claudeResumeID != "")

	// Build the command to run directly in the tmux window (no shell prompt visible)
	var windowCmd string
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		windowCmd = "echo 'Welcome to Agentic Live! Claude Code not found - using bash.' && exec bash"
	} else if claudeResumeID != "" {
		windowCmd = buildClaudeResumeCommand(claudePath, claudeResumeID)
	} else {
		windowCmd = buildClaudeCommand(claudePath, claudeSessionID)
	}

	// cd to git root before launching Claude
	if gitRoot, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
		root := strings.TrimSpace(string(gitRoot))
		windowCmd = "cd " + root + " && " + windowCmd
	}

	// Create window with Claude as the initial command — no shell prompt visible.
	// When Claude exits, the sh process exits and the pane becomes dead.
	// Set remain-on-exit so the pane stays for potential restart via respawn-pane.
	cmd := exec.Command("tmux", "new-window", "-t", sessionName, "-n", paneId,
		"sh", "-c", windowCmd)
	cmd.Env = append(os.Environ(), "HISTFILE=") // suppress shell history echoing
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to create tmux window %s: %w", paneId, err)
	}

	// Spawn fkeybar in a bottom split pane
	fkeybarPaneID := SpawnFKeyBar(sessionName, paneId, streamID)
	if fkeybarPaneID != "" && status != nil {
		status.Mu.Lock()
		status.FKeyBarPaneIDs = append(status.FKeyBarPaneIDs, fkeybarPaneID)
		status.Mu.Unlock()
	}

	ttydPort, err := util.FindFreePort()
	if err != nil {
		return nil, fmt.Errorf("failed to find free port for ttyd: %w", err)
	}

	groupSession := sessionName + "-ttyd-" + paneId
	ttydCmd := exec.Command("ttyd",
		"--port", fmt.Sprintf("%d", ttydPort),
		"bash", "-c",
		fmt.Sprintf(
			`tmux new-session -d -t '%s' -s '%s' && tmux select-window -t '%s:%s' && tmux attach -t '%s'`,
			sessionName, groupSession, groupSession, paneId, groupSession,
		),
	)
	// Strip $TMUX so ttyd's bash can run "tmux attach" without tmux refusing to nest sessions.
	ttydCmd.Env = util.FilterEnv(os.Environ(), "TMUX")
	if err := ttydCmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start ttyd for pane %s: %w", paneId, err)
	}

	time.Sleep(500 * time.Millisecond)

	metaCh := make(chan []byte, 16)

	go broadcast.ConnectBroadcast(streamID, status, metaCh, ttydPort, paneId)

	// Update pane tracking in SharedStatus
	if status != nil {
		status.Mu.Lock()
		status.Panes = append(status.Panes, types.PaneStatus{
			PaneId: paneId,
			Name:   name,
			Active: true,
		})
		status.Mu.Unlock()
		status.BroadcastEvent(fmt.Sprintf(`{"type":"pane_added","paneId":"%s","name":"%s"}`, paneId, name))
	}

	return &types.PaneInfo{
		PaneId:          paneId,
		Name:            name,
		TmuxWindow:      paneId,
		TtydPort:        ttydPort,
		TtydPID:         ttydCmd.Process.Pid,
		ClaudeSessionID: claudeSessionID,
		MetaCh:          metaCh,
		Active:          true,
		Done:            make(chan struct{}),
	}, nil
}

// StartStream creates a new broadcast session.
func StartStream(promptSharing, shareProjectInfo bool, projectName string, resumeStreamID string, claudeResumeID string, status *types.SharedStatus) tea.Cmd {
	return func() tea.Msg {
		streamID := resumeStreamID
		if streamID == "" {
			streamID = util.GenerateStreamID()
		}

		_, span := telemetry.Tracer().Start(context.Background(), "vibecast.stream.start",
			trace.WithAttributes(
				attribute.String("stream.id", streamID),
				attribute.Bool("stream.resumed", resumeStreamID != ""),
			))
		defer span.End()

		sessionName := "vibecast-" + streamID
		serverHost := util.GetServerHost()
		span.SetAttributes(attribute.String("server.host", serverHost))

		status.Mu.Lock()
		status.ServerHost = serverHost
		status.Mu.Unlock()

		session.CleanStaleSessions()

		if out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output(); err == nil {
			prefix := sessionName + "-ttyd-"
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if strings.HasPrefix(line, prefix) {
					exec.Command("tmux", "kill-session", "-t", line).Run()
				}
			}
		}
		exec.Command("tmux", "kill-session", "-t", sessionName).Run()

		// Create streaming tmux session with "info" window running fkeybar --info
		vibecastPath, _ := os.Executable()
		infoCmd := vibecastPath + " fkeybar --info --stream-id " + streamID
		if tp := os.Getenv("TERM_PROGRAM"); tp != "" {
			infoCmd = "TERM_PROGRAM=" + tp + " " + infoCmd
		}
		// Use -c to set the session's default directory so new windows (Claude panes) inherit it
		sessionDir, _ := os.Getwd()
		cmd := exec.Command("tmux", "new-session", "-d", "-s", sessionName, "-c", sessionDir, "-n", "info",
			"sh", "-c", infoCmd)
		if err := cmd.Run(); err != nil {
			return types.StreamErrorMsg{Err: fmt.Errorf("failed to create tmux session: %w", err)}
		}

		exec.Command("tmux", "set-environment", "-t", sessionName, "VIBECAST_STREAM_ID", streamID).Run()
		// Propagate VIBECAST_HOME so hooks/sessions resolve to the correct .vibecast directory
		if vh := os.Getenv("VIBECAST_HOME"); vh != "" {
			exec.Command("tmux", "set-environment", "-t", sessionName, "VIBECAST_HOME", vh).Run()
		}
		// Propagate VIBECAST_BIN so plugin hooks can find the vibecast binary
		if exePath, err := os.Executable(); err == nil {
			exec.Command("tmux", "set-environment", "-t", sessionName, "VIBECAST_BIN", exePath).Run()
		}
		if tp := os.Getenv("TERM_PROGRAM"); tp != "" {
			exec.Command("tmux", "set-environment", "-t", sessionName, "TERM_PROGRAM", tp).Run()
		}
		if vep := os.Getenv("VIBECAST_EXTRA_PLUGINS"); vep != "" {
			exec.Command("tmux", "set-environment", "-t", sessionName, "VIBECAST_EXTRA_PLUGINS", vep).Run()
		}
		if vasp := os.Getenv("VIBECAST_APPEND_SYSTEM_PROMPT"); vasp != "" {
			exec.Command("tmux", "set-environment", "-t", sessionName, "VIBECAST_APPEND_SYSTEM_PROMPT", vasp).Run()
		}
		if vaspf := os.Getenv("VIBECAST_APPEND_SYSTEM_PROMPT_FILE"); vaspf != "" {
			exec.Command("tmux", "set-environment", "-t", sessionName, "VIBECAST_APPEND_SYSTEM_PROMPT_FILE", vaspf).Run()
		}
		if ipf := os.Getenv("VIBECAST_INITIAL_PROMPT_FILE"); ipf != "" {
			exec.Command("tmux", "set-environment", "-t", sessionName, "VIBECAST_INITIAL_PROMPT_FILE", ipf).Run()
		}
		if kbPin := os.Getenv("VIBECAST_KEYBOARD_PIN"); kbPin != "" {
			exec.Command("tmux", "set-environment", "-t", sessionName, "VIBECAST_KEYBOARD_PIN", kbPin).Run()
		}
		for _, key := range []string{
			"AGENTICS_JOB_ID",
			"AGENTICS_TOKEN",
			"AGENTICS_BASE_URL",
			"AGENTICS_OWNER",
			"AGENTICS_PROJECT_NAME",
			"AGENTICS_JOB_MODE",
			"AGENTICS_AUTO_GIT",
			"AGENTICS_COMMIT_MESSAGE_HINT",
		} {
			if val := os.Getenv(key); val != "" {
				exec.Command("tmux", "set-environment", "-t", sessionName, key, val).Run()
			}
		}
		if stageGitURL := os.Getenv("STAGE_GIT_URL"); stageGitURL != "" {
			exec.Command("tmux", "set-environment", "-t", sessionName, "STAGE_GIT_URL", stageGitURL).Run()
			exec.Command("tmux", "set-environment", "-t", sessionName, "STAGE_GIT_TOKEN", os.Getenv("STAGE_GIT_TOKEN")).Run()
			exec.Command("tmux", "set-environment", "-t", sessionName, "STAGE_DIR", os.Getenv("STAGE_DIR")).Run()
		}
		exec.Command("tmux", "set-option", "-t", sessionName, "window-size", "largest").Run()
		exec.Command("tmux", "set-option", "-t", sessionName, "status", "on").Run()
		exec.Command("tmux", "set-option", "-t", sessionName, "status-style", "bg=#FF6B00,fg=#000000,bold").Run()
		exec.Command("tmux", "set-option", "-t", sessionName, "status-left", " 🔴 AGENTIC LIVE ").Run()
		exec.Command("tmux", "set-option", "-t", sessionName, "status-left-length", "50").Run()
		exec.Command("tmux", "set-option", "-t", sessionName, "status-right", " Ctrl-b d → back to dashboard ").Run()
		exec.Command("tmux", "set-option", "-t", sessionName, "status-justify", "centre").Run()

		// Create "help" window running fkeybar --help-screen
		helpCmd := vibecastPath + " fkeybar --help-screen"
		if tp := os.Getenv("TERM_PROGRAM"); tp != "" {
			helpCmd = "TERM_PROGRAM=" + tp + " " + helpCmd
		}
		exec.Command("tmux", "new-window", "-t", sessionName, "-n", "help",
			"sh", "-c", helpCmd).Run()
		// Switch back to info window (new-window switches focus)
		exec.Command("tmux", "select-window", "-t", sessionName+":info").Run()

		// Bind F-keys at the tmux session level
		BindFKeys(sessionName)

		if authConfig, err := auth.FetchAuthConfig(serverHost); err == nil {
			if authConfig.AuthRequired {
				if _, _, authErr := auth.GetValidToken(); authErr != nil {
					return types.StreamErrorMsg{Err: fmt.Errorf("authentication required — run 'vibecast login' first")}
				}
			}
		}

		refreshCtx, refreshCancel := context.WithCancel(context.Background())
		_ = refreshCancel
		go auth.StartTokenRefreshLoop(refreshCtx)

		// Call session-event BEFORE SpawnPane so OTEL env vars are set
		// in the tmux session before Claude inherits them
		pinCode := ""
		{
			scheme := "https"
			if util.IsLocalHost(serverHost) {
				scheme = "http"
			}
			apiURL := fmt.Sprintf("%s://%s/api/lives/session-event", scheme, serverHost)

			eventBody := map[string]interface{}{
				"streamId": streamID,
				"event":    "start",
			}
			if token, claims, err := auth.GetValidToken(); err == nil && token != "" {
				eventBody["user"] = claims
			}
			bodyBytes, _ := json.Marshal(eventBody)
			resp, err := http.Post(apiURL, "application/json", bytes.NewReader(bodyBytes))
			if err == nil {
				defer resp.Body.Close()
				var result struct {
					OK  bool              `json:"ok"`
					Pin string            `json:"pin"`
					Env map[string]string `json:"env"`
				}
				if json.NewDecoder(resp.Body).Decode(&result) == nil && result.Pin != "" {
					pinCode = result.Pin
				}
				// Propagate OTEL env vars to tmux session so Claude Code inherits them.
				// The server builds URLs from its own host header (e.g. localhost:PORT).
				// Rewrite localhost/127.0.0.1 to serverHost so telemetry works from
				// inside containers where localhost != the host machine.
				for k, v := range result.Env {
					if strings.Contains(v, "://localhost") || strings.Contains(v, "://127.0.0.1") {
						if parsed, err := url.Parse(v); err == nil {
							parsed.Host = serverHost
							v = parsed.String()
						}
					}
					os.Setenv(k, v)
					exec.Command("tmux", "set-environment", "-t", sessionName, k, v).Run()
					logDebug("[stream] set env %s=%s\n", k, v)
				}
			}
			if pinCode == "" {
				logDebug("[stream] warning: could not get PIN from server\n")
			} else {
				logDebug("[stream] got PIN: %s\n", pinCode)
			}
		}

		status.Mu.Lock()
		status.PinCode = pinCode
		status.Mu.Unlock()

		mainPane, err := SpawnPane(sessionName, streamID, "main", "Main", status, claudeResumeID)
		if err != nil {
			return types.StreamErrorMsg{Err: fmt.Errorf("failed to spawn main pane: %w", err)}
		}
		claudeSessionID := mainPane.ClaudeSessionID

		// Switch to info window so broadcaster sees PIN/join code first
		exec.Command("tmux", "select-window", "-t", sessionName+":info").Run()

		cwd, _ := os.Getwd()
		if gitRoot, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
			cwd = strings.TrimSpace(string(gitRoot))
		}
		projName := util.GetProjectName(projectName)
		projOwner := util.GetProjectOwner()
		sf := types.SessionFile{
			StreamID:        streamID,
			ServerHost:      serverHost,
			Workspace:       cwd,
			Owner:           projOwner,
			Project:         projName,
			StartedAt:       time.Now().Unix(),
			PID:             os.Getpid(),
			ClaudeSessionID: claudeSessionID,
			Panes: []types.SessionFilePaneEntry{
				{PaneID: mainPane.PaneId, ClaudeSessionID: claudeSessionID},
			},
		}
		session.WriteSessionFile(sf)

		metaCh := mainPane.MetaCh

		if shareProjectInfo {
			go func() {
				time.Sleep(2 * time.Second)
				streamInfo, _ := json.Marshal(map[string]interface{}{
					"type":      "metadata",
					"subtype":   "stream_info",
					"owner":     projOwner,
					"project":   projName,
					"workspace": cwd,
					"startedAt": sf.StartedAt,
				})
				metaCh <- streamInfo
			}()
		}

		// Broadcast capabilities (keyboard enabled if PIN is configured)
		go func() {
			time.Sleep(3 * time.Second)
			caps := map[string]interface{}{
				"type":     "metadata",
				"subtype":  "capabilities",
				"keyboard": os.Getenv("VIBECAST_KEYBOARD_PIN") != "",
			}
			capsJson, _ := json.Marshal(caps)
			metaCh <- capsJson
		}()

		viewerURL := util.BuildViewerURL(serverHost, streamID)

		return types.StreamStartedMsg{
			StreamID:        streamID,
			URL:             viewerURL,
			PID:             mainPane.TtydPID,
			TtydPort:        mainPane.TtydPort,
			MetaCh:          metaCh,
			ClaudeSessionID: claudeSessionID,
			PinCode:         pinCode,
			MainPane:        mainPane,
		}
	}
}

// ResumeStream resumes a previous broadcast session.
func ResumeStream(streamID string, status *types.SharedStatus) tea.Cmd {
	return func() tea.Msg {
		sessionName := "vibecast-" + streamID

		_, span := telemetry.Tracer().Start(context.Background(), "vibecast.stream.resume",
			trace.WithAttributes(attribute.String("stream.id", streamID)))
		defer span.End()

		serverHost := util.GetServerHost()
		span.SetAttributes(attribute.String("server.host", serverHost))

		status.Mu.Lock()
		status.ServerHost = serverHost
		status.Mu.Unlock()

		if authConfig, err := auth.FetchAuthConfig(serverHost); err == nil {
			if authConfig.AuthRequired {
				if _, _, authErr := auth.GetValidToken(); authErr != nil {
					return types.StreamErrorMsg{Err: fmt.Errorf("authentication required — run 'vibecast login' first")}
				}
			}
		}

		refreshCtx, refreshCancel := context.WithCancel(context.Background())
		_ = refreshCancel // will be cancelled when process exits
		go auth.StartTokenRefreshLoop(refreshCtx)

		pinCode := ""
		var serverClaudeSessionID string
		var serverProject, serverWorkspace string
		var serverStartedAt int64
		var serverEnv map[string]string
		{
			scheme := "https"
			if util.IsLocalHost(serverHost) {
				scheme = "http"
			}
			apiURL := fmt.Sprintf("%s://%s/api/lives/session-event", scheme, serverHost)
			eventBody := map[string]interface{}{
				"streamId": streamID,
				"event":    "start",
			}
			if token, claims, err := auth.GetValidToken(); err == nil && token != "" {
				eventBody["user"] = claims
			}
			bodyBytes, _ := json.Marshal(eventBody)
			resp, err := http.Post(apiURL, "application/json", bytes.NewReader(bodyBytes))
			if err == nil {
				defer resp.Body.Close()
				var result struct {
					OK              bool              `json:"ok"`
					Pin             string            `json:"pin"`
					ClaudeSessionID *string           `json:"claudeSessionId"`
					Project         *string           `json:"project"`
					Workspace       *string           `json:"workspace"`
					StartedAt       *int64            `json:"startedAt"`
					Env             map[string]string `json:"env"`
				}
				if json.NewDecoder(resp.Body).Decode(&result) == nil {
					pinCode = result.Pin
					if result.ClaudeSessionID != nil {
						serverClaudeSessionID = *result.ClaudeSessionID
					}
					if result.Project != nil {
						serverProject = *result.Project
					}
					if result.Workspace != nil {
						serverWorkspace = *result.Workspace
					}
					if result.StartedAt != nil {
						serverStartedAt = *result.StartedAt
					}
					serverEnv = result.Env
				}
				// Set env vars in current process so SpawnPane inherits them
				for k, v := range serverEnv {
					os.Setenv(k, v)
					logDebug("[resume] set env %s=%s\n", k, v)
				}
			}
			if pinCode == "" {
				logDebug("[resume] warning: could not get PIN from server\n")
			}
		}

		status.Mu.Lock()
		status.PinCode = pinCode
		status.Mu.Unlock()

		tmuxAlive := exec.Command("tmux", "has-session", "-t", sessionName).Run() == nil

		sfPath := filepath.Join(session.SessionsDir(), streamID+".json")
		sf, err := session.ReadSessionFile(sfPath)
		if err != nil {
			logDebug("[resume] no local session file — using server data\n")
			sf = &types.SessionFile{
				StreamID:   streamID,
				ServerHost: serverHost,
				Project:    serverProject,
				Workspace:  serverWorkspace,
				StartedAt:  serverStartedAt,
				Panes:      []types.SessionFilePaneEntry{{PaneID: "main"}},
			}
			if sf.StartedAt == 0 {
				sf.StartedAt = time.Now().Unix()
			}
			if serverClaudeSessionID != "" {
				sf.ClaudeSessionID = serverClaudeSessionID
				sf.Panes = []types.SessionFilePaneEntry{{PaneID: "main", ClaudeSessionID: serverClaudeSessionID}}
				logDebug("[resume] recovered Claude session ID from server: %s\n", serverClaudeSessionID)
			} else if !tmuxAlive {
				logDebug("[resume] warning: no Claude session ID — Claude will start fresh\n")
			}
		}

		paneEntries := sf.Panes
		if len(paneEntries) == 0 {
			paneEntries = []types.SessionFilePaneEntry{{PaneID: "main"}}
		}

		var mainPane *types.PaneInfo

		if tmuxAlive {
			logDebug("[resume] tmux session %s is alive — reattaching\n", sessionName)

			// Propagate OTEL env vars to existing tmux session
			for k, v := range serverEnv {
				exec.Command("tmux", "set-environment", "-t", sessionName, k, v).Run()
			}

			// Bind F-keys at the tmux session level
			BindFKeys(sessionName)

			for _, pe := range paneEntries {
				target := sessionName + ":" + pe.PaneID

				if err := exec.Command("tmux", "select-window", "-t", target).Run(); err != nil {
					logDebug("[resume] skipping pane %s — tmux window not found\n", pe.PaneID)
					continue
				}

				ttydPort, err := util.FindFreePort()
				if err != nil {
					return types.StreamErrorMsg{Err: fmt.Errorf("failed to find free port: %w", err)}
				}

				ttydCmd := exec.Command("ttyd",
					"--port", fmt.Sprintf("%d", ttydPort),
					"tmux", "attach", "-t", target,
				)
				if err := ttydCmd.Start(); err != nil {
					return types.StreamErrorMsg{Err: fmt.Errorf("failed to start ttyd for pane %s: %w", pe.PaneID, err)}
				}

				time.Sleep(500 * time.Millisecond)

				metaCh := make(chan []byte, 16)
				go broadcast.ConnectBroadcast(streamID, status, metaCh, ttydPort, pe.PaneID)

				pane := &types.PaneInfo{
					PaneId:          pe.PaneID,
					Name:            pe.PaneID,
					TmuxWindow:      pe.PaneID,
					TtydPort:        ttydPort,
					TtydPID:         ttydCmd.Process.Pid,
					ClaudeSessionID: pe.ClaudeSessionID,
					MetaCh:          metaCh,
					Active:          true,
					Done:            make(chan struct{}),
				}
				if mainPane == nil {
					mainPane = pane
				}
			}
		} else {
			logDebug("[resume] tmux session %s not found — creating new session and resuming Claude\n", sessionName)

			cmd := exec.Command("tmux", "new-session", "-d", "-s", sessionName, "-n", "control")
			if err := cmd.Run(); err != nil {
				return types.StreamErrorMsg{Err: fmt.Errorf("failed to create tmux session: %w", err)}
			}

			exec.Command("tmux", "set-environment", "-t", sessionName, "VIBECAST_STREAM_ID", streamID).Run()
			// Propagate OTEL env vars to the new tmux session
			for k, v := range serverEnv {
				exec.Command("tmux", "set-environment", "-t", sessionName, k, v).Run()
			}
			exec.Command("tmux", "set-option", "-t", sessionName, "window-size", "largest").Run()
			exec.Command("tmux", "set-option", "-t", sessionName, "status", "on").Run()
			exec.Command("tmux", "set-option", "-t", sessionName, "status-style", "bg=#FF6B00,fg=#000000,bold").Run()
			exec.Command("tmux", "set-option", "-t", sessionName, "status-left", " 🔴 AGENTIC LIVE ").Run()
			exec.Command("tmux", "set-option", "-t", sessionName, "status-left-length", "50").Run()
			exec.Command("tmux", "set-option", "-t", sessionName, "status-right", " Ctrl-b d → back to dashboard ").Run()
			exec.Command("tmux", "set-option", "-t", sessionName, "status-justify", "centre").Run()

			// Bind F-keys at the tmux session level
			BindFKeys(sessionName)

			for _, pe := range paneEntries {
				pane, err := SpawnPane(sessionName, streamID, pe.PaneID, pe.PaneID, status, pe.ClaudeSessionID)
				if err != nil {
					logDebug("[resume] failed to spawn pane %s: %v\n", pe.PaneID, err)
					continue
				}
				if mainPane == nil {
					mainPane = pane
				}
			}
		}

		if mainPane == nil {
			return types.StreamErrorMsg{Err: fmt.Errorf("no valid panes found for stream %s", streamID)}
		}

		sf.PID = os.Getpid()
		sf.ServerHost = serverHost
		session.WriteSessionFile(*sf)

		if sf.Project != "" || sf.Workspace != "" {
			go func() {
				time.Sleep(2 * time.Second)
				streamInfo, _ := json.Marshal(map[string]interface{}{
					"type":      "metadata",
					"subtype":   "stream_info",
					"owner":     sf.Owner,
					"project":   sf.Project,
					"workspace": sf.Workspace,
					"startedAt": sf.StartedAt,
				})
				mainPane.MetaCh <- streamInfo
			}()
		}

		// Broadcast capabilities (keyboard enabled if PIN is configured)
		go func() {
			time.Sleep(3 * time.Second)
			caps := map[string]interface{}{
				"type":     "metadata",
				"subtype":  "capabilities",
				"keyboard": os.Getenv("VIBECAST_KEYBOARD_PIN") != "",
			}
			capsJson, _ := json.Marshal(caps)
			mainPane.MetaCh <- capsJson
		}()

		viewerURL := util.BuildViewerURL(serverHost, streamID)

		return types.StreamStartedMsg{
			StreamID:        streamID,
			URL:             viewerURL,
			PID:             mainPane.TtydPID,
			TtydPort:        mainPane.TtydPort,
			MetaCh:          mainPane.MetaCh,
			ClaudeSessionID: mainPane.ClaudeSessionID,
			PinCode:         pinCode,
			MainPane:        mainPane,
		}
	}
}

// StopStream stops the broadcast and cleans up.
// Optional stopMessage and stopConclusion are passed to the server session-event endpoint.
func StopStream(pid int, sessionName, streamID string, promptSharing bool, panes []types.PaneInfo, keepTmux bool, stopMessage ...string) tea.Cmd {
	return func() tea.Msg {
		_, span := telemetry.Tracer().Start(context.Background(), "vibecast.stream.stop",
			trace.WithAttributes(attribute.String("stream.id", streamID)))
		defer span.End()

		// Grace period: wait for final hooks (tool_use_end, assistant_response)
		// to flush their metadata before we disconnect
		fmt.Fprintf(os.Stderr, "[stop] Graceful shutdown: waiting 10s for final metadata flush...\n")
		time.Sleep(10 * time.Second)

		{
			sf := session.FindActiveSession()
			host := ""
			if sf != nil {
				host = sf.ServerHost
			}
			if host == "" {
				host = util.GetServerHost()
			}
			scheme := "https"
			if util.IsLocalHost(host) {
				scheme = "http"
			}
			apiURL := fmt.Sprintf("%s://%s/api/lives/session-event", scheme, host)
			eventData := map[string]interface{}{
				"streamId": streamID,
				"event":    "end",
			}
			// Include message and conclusion if provided
			if len(stopMessage) > 0 && stopMessage[0] != "" {
				eventData["message"] = stopMessage[0]
			}
			if len(stopMessage) > 1 && stopMessage[1] != "" {
				eventData["conclusion"] = stopMessage[1]
			}
			if len(stopMessage) > 2 && stopMessage[2] != "" {
				eventData["gitCommit"] = stopMessage[2]
			}
			if len(stopMessage) > 3 && stopMessage[3] != "" {
				eventData["gitBranch"] = stopMessage[3]
			}
			// Include jobId if set via env (runner scenario)
			if jobId := os.Getenv("AGENTICS_JOB_ID"); jobId != "" {
				eventData["jobId"] = jobId
			}
			eventBody, _ := json.Marshal(eventData)
			http.Post(apiURL, "application/json", bytes.NewReader(eventBody))
		}

		session.DeleteSessionFile(streamID)

		os.RemoveAll(hooks.TranscriptCursorDir(streamID))

		for _, pane := range panes {
			if pane.TtydPID > 0 {
				if proc, err := os.FindProcess(pane.TtydPID); err == nil {
					proc.Kill()
				}
			}
		}

		if pid > 0 {
			proc, err := os.FindProcess(pid)
			if err == nil {
				proc.Kill()
			}
		}

		if !keepTmux {
			for _, pane := range panes {
				groupSess := sessionName + "-ttyd-" + pane.PaneId
				exec.Command("tmux", "kill-session", "-t", groupSess).Run()
			}
			exec.Command("tmux", "kill-session", "-t", sessionName).Run()
		}

		return types.StreamStoppedMsg{}
	}
}

// ApproveImage sends an image approval/rejection to the server.
func ApproveImage(streamID, imageID string, approved bool) {
	sf := session.FindActiveSession()
	if sf == nil {
		return
	}
	scheme := "https"
	if util.IsLocalHost(sf.ServerHost) {
		scheme = "http"
	}
	url := fmt.Sprintf("%s://%s/api/lives/image-approve", scheme, sf.ServerHost)
	payload, _ := json.Marshal(map[string]interface{}{
		"streamId": streamID,
		"imageId":  imageID,
		"approved": approved,
	})
	req, err := http.NewRequest("POST", url, bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}
