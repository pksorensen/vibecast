package broadcast

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/pksorensen/vibecast/internal/auth"
	"github.com/pksorensen/vibecast/internal/telemetry"
	"github.com/pksorensen/vibecast/internal/types"
	"github.com/pksorensen/vibecast/internal/util"
	ws "github.com/pksorensen/vibecast/internal/websocket"

	tea "github.com/charmbracelet/bubbletea"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var ansiRE = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[A-Za-z]|[^[]|][^\x07]*\x07)`)
var urlRE = regexp.MustCompile(`https?://[^\s\x00-\x1f"'<>\x1b\\]{10,}`)

func classifyURL(u string) string {
	switch {
	case strings.Contains(u, "claude.ai") || strings.Contains(u, "auth.anthropic"):
		return "claude-login"
	default:
		return ""
	}
}

func postURLDetected(serverHost, sessionID, u, context string) {
	scheme := "https"
	if util.IsLocalHost(serverHost) {
		scheme = "http"
	}
	apiURL := fmt.Sprintf("%s://%s/api/lives/metadata", scheme, serverHost)
	body, _ := json.Marshal(map[string]interface{}{
		"type":      "metadata",
		"subtype":   "url_detected",
		"sessionId": sessionID,
		"url":       u,
		"context":   context,
		"timestamp": time.Now().UnixMilli(),
	})
	resp, err := http.Post(apiURL, "application/json", bytes.NewReader(body))
	if err != nil {
		logDebug("[broadcast] url_detected post error: %v\n", err)
		return
	}
	resp.Body.Close()
	logDebug("[broadcast] url_detected: %s (context=%s)\n", u, context)
}

var debugLog = os.Getenv("VIBECAST_DEBUG") != ""

func logDebug(format string, args ...interface{}) {
	if debugLog {
		fmt.Fprintf(os.Stderr, format, args...)
	}
}

// ConnectBroadcast connects local ttyd to the cloud server and retries on disconnection.
func ConnectBroadcast(sessionID string, status *types.SharedStatus, metaCh chan []byte, ttydPort int, paneId string) {
	for attempt := 0; attempt < 120; attempt++ {
		if attempt > 0 {
			time.Sleep(2 * time.Second)
		}
		status.Mu.Lock()
		host := status.ServerHost
		broadcastID := status.BroadcastID
		status.Mu.Unlock()
		if broadcastID == "" {
			broadcastID = sessionID
		}
		connectBroadcastOnce(sessionID, broadcastID, host, status, metaCh, ttydPort, attempt, paneId)
	}
}

func connectBroadcastOnce(sessionID string, broadcastID string, serverHost string, status *types.SharedStatus, metaCh chan []byte, ttydPort int, attempt int, paneId string) {
	// 1. Connect to local ttyd
	ttydHost := fmt.Sprintf("localhost:%d", ttydPort)
	ttydConn, ttydReader, err := ws.ConnectWithProtocol(ttydHost, "/ws", "tty")
	if err != nil {
		logDebug("[broadcast] ttyd connect error: %v\n", err)
		return
	}
	defer ttydConn.Close()

	// 2. Fetch auth token from local ttyd and send init JSON
	authToken := ""
	tokenResp, err := http.Get(fmt.Sprintf("http://%s/token", ttydHost))
	if err == nil {
		var tokenData map[string]interface{}
		if json.NewDecoder(tokenResp.Body).Decode(&tokenData) == nil {
			if t, ok := tokenData["token"].(string); ok {
				authToken = t
			}
		}
		tokenResp.Body.Close()
	}

	ttydInit := map[string]interface{}{
		"AuthToken": authToken,
	}
	type winsize struct {
		Row, Col, Xpixel, Ypixel uint16
	}
	var ws_ winsize
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(syscall.Stdin),
		uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws_))); errno == 0 && ws_.Col > 0 && ws_.Row > 0 {
		ttydInit["columns"] = int(ws_.Col)
		ttydInit["rows"] = int(ws_.Row)
		logDebug("[broadcast] sending ttyd init with size %dx%d\n", ws_.Col, ws_.Row)
	} else {
		logDebug("[broadcast] WARNING: could not get terminal size (errno=%v)\n", errno)
	}
	initMsg, _ := json.Marshal(ttydInit)
	ws.SendText(ttydConn, initMsg)

	// 3. Connect to cloud server broadcast endpoint
	broadcastPath := "/api/lives/broadcast/ws?sessionId=" + sessionID + "&broadcastId=" + broadcastID + "&paneId=" + paneId
	if token, _, err := auth.GetValidToken(); err == nil && token != "" {
		broadcastPath += "&token=" + token
	}
	serverConn, serverReader, err := ws.ConnectWithProtocol(serverHost, broadcastPath, "")
	if err != nil {
		logDebug("[broadcast] server connect error: %v\n", err)
		return
	}
	defer serverConn.Close()

	// Store server connection so it can be closed externally to force reconnect
	status.Mu.Lock()
	status.ServerConn = serverConn
	status.Mu.Unlock()
	defer func() {
		status.Mu.Lock()
		if status.ServerConn == serverConn {
			status.ServerConn = nil
		}
		status.Mu.Unlock()
	}()

	logDebug("[broadcast] relay active for session %s\n", sessionID)

	done := make(chan struct{})

	// Extract the tmux socket path from $TMUX (format: "socket,pid,session").
	// When vibecast runs inside a tmux session on a non-default socket (e.g. the
	// runner's /tmp/pks-runner-aspire.sock), exec'd "tmux" subcommands need -S
	// explicitly — relying on $TMUX alone causes capture-pane to return empty.
	tmuxSocket := ""
	if tmuxEnv := os.Getenv("TMUX"); tmuxEnv != "" {
		if idx := strings.Index(tmuxEnv, ","); idx > 0 {
			tmuxSocket = tmuxEnv[:idx]
		}
	}
	tmuxCmd := func(args ...string) *exec.Cmd {
		if tmuxSocket != "" {
			return exec.Command("tmux", append([]string{"-S", tmuxSocket}, args...)...)
		}
		return exec.Command("tmux", args...)
	}

	// Goroutine: poll broadcaster's terminal size and propagate to tmux -> viewers
	go func() {
		tmuxSess := "vibecast-" + sessionID
		tmuxTarget := tmuxSess + ":" + paneId
		lastCols, lastRows := 0, 0
		lastTermCols, lastTermRows := 0, 0
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				var ws2 struct{ Row, Col, Xpixel, Ypixel uint16 }
				if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(syscall.Stdin),
					uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws2))); errno == 0 {
					tc, tr := int(ws2.Col), int(ws2.Row)
					if tc > 0 && tr > 0 && (tc != lastTermCols || tr != lastTermRows) {
						lastTermCols, lastTermRows = tc, tr
						resizeJSON, _ := json.Marshal(map[string]interface{}{"columns": tc, "rows": tr})
						ws.SendText(ttydConn, append([]byte("1"), resizeJSON...))
					}
				}

				out, err := tmuxCmd("display-message", "-t", tmuxTarget, "-p", "#{pane_width} #{pane_height}").Output()
				if err != nil {
					continue
				}
				var c, r int
				if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d %d", &c, &r); err != nil || c <= 0 || r <= 0 {
					continue
				}
				if c != lastCols || r != lastRows {
					lastCols, lastRows = c, r
					msg, _ := json.Marshal(map[string]interface{}{
						"columns": c,
						"rows":    r,
					})
					if err := ws.SendText(serverConn, msg); err != nil {
						logDebug("[broadcast] dims send error: %v\n", err)
						return
					}
					logDebug("[broadcast] terminal resized to %dx%d\n", c, r)
				}
			}
		}
	}()

	// Goroutine: periodic terminal snapshot via tmux capture-pane
	// Also handles auto-answering the Claude Code workspace trust dialog in job mode
	// and detects the "session is too large" context-size menu to broadcast as a vote card.
	jobMode := os.Getenv("AGENTICS_JOB_MODE") == "1"
	trustAnsweredSnap := false
	themeAnsweredSnap := false
	loginAnsweredSnap := false
	loginSuccessAnsweredSnap := false
	securityNotesAnsweredSnap := false
	bypassAnsweredSnap := false
	// Track posted alp_pane question so we only post once per session-size menu appearance.
	postedPaneQuestionId := ""
	// Track posted onboarding_external question so we only surface the OAuth gate once per URL.
	postedOnboardingQuestionId := ""
	// Strip ANSI escape codes for plain-text detection (selected menu items have color codes
	// between every word, breaking strings.Contains on the raw -e capture output).
	ansiStripRe := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

	// Rolling debug capture directory — keeps the last 10 pane snapshots on disk so they
	// can be inspected after the fact without needing to re-run the session.
	// Location: $VIBECAST_HOME/.vibecast/debug/captures/ (or ~/.vibecast/debug/captures/)
	vibecastHome := os.Getenv("VIBECAST_HOME")
	if vibecastHome == "" {
		vibecastHome, _ = os.UserHomeDir()
	}
	captureDebugDir := filepath.Join(vibecastHome, ".vibecast", "debug", "captures")
	os.MkdirAll(captureDebugDir, 0755)

	// savePaneCapture writes a timestamped plain-text capture file and prunes to 10 files.
	savePaneCapture := func(plain string) {
		name := fmt.Sprintf("%d-%s.txt", time.Now().UnixNano(), sessionID)
		path := filepath.Join(captureDebugDir, name)
		os.WriteFile(path, []byte(plain), 0644)

		// Prune: keep only the 10 most recent files (sorted by name = sorted by time).
		entries, err := os.ReadDir(captureDebugDir)
		if err != nil || len(entries) <= 10 {
			return
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if !e.IsDir() {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, old := range names[:len(names)-10] {
			os.Remove(filepath.Join(captureDebugDir, old))
		}
	}
	snapSchemeBase := "https"
	if util.IsLocalHost(serverHost) {
		snapSchemeBase = "http"
	}

	go func() {
		snapTmuxTarget := "vibecast-" + sessionID + ":" + paneId
		snapshotURL := fmt.Sprintf("%s://%s/_relay/snapshot", snapSchemeBase, serverHost)

		postSnapshot := func() {
			out, err := tmuxCmd("capture-pane", "-p", "-e", "-t", snapTmuxTarget).Output()
			if err != nil {
				logDebug("[broadcast] capture-pane error: %v\n", err)
				return
			}
			rendered := string(out)

			// In job mode, check for the workspace trust dialog in the rendered screen.
			// Spawn mode owns the workspace (runner created the volume + clone) so VIBECAST_ALLOWED_DIRECTORIES
			// isn't set and we trust unconditionally. In-process mode passes the explicit allowed dir for safety.
			if jobMode && !trustAnsweredSnap {
				if strings.Contains(rendered, "Quick safety check") {
					allowedDir := os.Getenv("VIBECAST_ALLOWED_DIRECTORIES")
					_, span := telemetry.Tracer().Start(
						telemetry.ContextFromTraceparent(os.Getenv("TRACEPARENT")),
						"vibecast.trust_prompt",
					)
					span.SetAttributes(
						attribute.String("session.id", sessionID),
						attribute.String("allowed_dir", allowedDir),
					)
					// Auto-trust if allowedDir is unset (spawn-mode default) OR if the prompt's
					// path matches the explicitly allowed dir (in-process mode).
					if allowedDir == "" || strings.Contains(rendered, allowedDir) {
						// The trust dialog defaults to "❯ 1. Yes, I trust this folder".
						// Send Enter alone — sending "1" + Enter in one call doesn't
						// register reliably in BubbleTea; Enter on the default is enough.
						tmuxCmd("send-keys", "-t", snapTmuxTarget+".0", "Enter").Run()
						logDebug("[broadcast] auto-answered workspace trust prompt (allowedDir=%q)\n", allowedDir)
						span.SetAttributes(attribute.Bool("auto_answered", true))
						trustAnsweredSnap = true
					} else {
						logDebug("[broadcast] trust prompt path does not match allowed dir %q — not auto-answering\n", allowedDir)
						span.SetAttributes(attribute.Bool("auto_answered", false))
					}
					span.End()
				}
			}

			// Compute plain (ANSI-stripped) text once for all detection below.
			plain := ansiStripRe.ReplaceAllString(rendered, "")

			// Save a rolling debug capture (plain text, last 10) for post-hoc inspection.
			savePaneCapture(plain)

			// Claude Code first-run gates auto-answered in job mode.
			// We do NOT pre-bake ~/.claude.json on the runner side because env-var providers
			// (ANTHROPIC_API_KEY, ANTHROPIC_BASE_URL, CLAUDE_CODE_USE_BEDROCK) suppress these
			// gates upstream — pre-baking would override that. So we let Claude run its native
			// flow and intercept here.
			if jobMode {
				// Post-OAuth confirmation screen — "Login successful. Press Enter to continue".
				// No real choice — just dismiss with Enter.
				if !loginSuccessAnsweredSnap && strings.Contains(plain, "Login successful") &&
					strings.Contains(plain, "Press Enter to continue") {
					tmuxCmd("send-keys", "-t", snapTmuxTarget+".0", "Enter").Run()
					logDebug("[broadcast] auto-answered post-OAuth login-successful screen\n")
					loginSuccessAnsweredSnap = true
				}
				// Security notes screen — appears after login, displays "Claude can make mistakes"
				// + "prompt injection risks" + "Press Enter to continue". No real choice.
				if !securityNotesAnsweredSnap && strings.Contains(plain, "Security notes") &&
					strings.Contains(plain, "Press Enter to continue") {
					tmuxCmd("send-keys", "-t", snapTmuxTarget+".0", "Enter").Run()
					logDebug("[broadcast] auto-answered security-notes screen\n")
					securityNotesAnsweredSnap = true
				}
				// Bypass-permissions confirmation. Default highlight is "1. No, exit" so a
				// blind Enter would kill Claude. Job mode runs Claude with --dangerously-skip-permissions
				// by design, so explicitly accept option 2 ("Yes, I accept"). Send the digit then Enter.
				if !bypassAnsweredSnap && strings.Contains(plain, "Bypass Permissions mode") &&
					strings.Contains(plain, "Yes, I accept") {
					tmuxCmd("send-keys", "-t", snapTmuxTarget+".0", "2").Run()
					time.Sleep(100 * time.Millisecond)
					tmuxCmd("send-keys", "-t", snapTmuxTarget+".0", "Enter").Run()
					logDebug("[broadcast] auto-answered bypass-permissions warning (option 2 — accept)\n")
					bypassAnsweredSnap = true
				}

				// Helper for posting an alp_pane vote question. Server stores it on the task
				// pendingQuestion; the team votes on the dashboard; existing alp_pane answer
				// handler injects the digit (option index + 1) + Enter back into this pane.
				postAlpPaneQuestion := func(qIdSeed, question string, options []string) {
					hash := sha256.Sum256([]byte(qIdSeed))
					paneQuestionId := fmt.Sprintf("alp-pane-%x", hash[:8])
					payload, _ := json.Marshal(map[string]interface{}{
						"sessionId":      sessionID,
						"type":           "metadata",
						"subtype":        "alp_pane",
						"paneQuestionId": paneQuestionId,
						"question":       question,
						"options":        options,
						"timestamp":      time.Now().Unix(),
					})
					metaURL := fmt.Sprintf("%s://%s/api/lives/metadata", snapSchemeBase, serverHost)
					if resp, err := http.Post(metaURL, "application/json", bytes.NewReader(payload)); err == nil {
						resp.Body.Close()
						logDebug("[broadcast] alp_pane posted paneQuestionId=%s question=%q\n", paneQuestionId, question)
					} else {
						logDebug("[broadcast] alp_pane post error: %v\n", err)
					}
				}
				// Theme picker — surface as a team-vote question. Options match Claude's menu
				// ORDER 1..7 so the alp_pane injector's "digit by index" maps correctly.
				if !themeAnsweredSnap && strings.Contains(plain, "Choose the text style") {
					postAlpPaneQuestion(
						fmt.Sprintf("onboarding-theme:%s", sessionID),
						"Choose terminal theme for Claude Code",
						[]string{
							"Auto (match terminal)",
							"Dark mode",
							"Light mode",
							"Dark mode (colorblind-friendly)",
							"Light mode (colorblind-friendly)",
							"Dark mode (ANSI colors only)",
							"Light mode (ANSI colors only)",
						},
					)
					themeAnsweredSnap = true
				}
				// Login method picker — surface as team-vote. To skip this gate entirely, set
				// ANTHROPIC_API_KEY / ANTHROPIC_BASE_URL / CLAUDE_CODE_USE_BEDROCK on the runner
				// host BEFORE the container starts; Claude then suppresses the prompt.
				if !loginAnsweredSnap && strings.Contains(plain, "Select login method:") &&
					strings.Contains(plain, "subscription") {
					postAlpPaneQuestion(
						fmt.Sprintf("onboarding-login:%s", sessionID),
						"Choose Claude Code login method",
						[]string{
							"Claude account with subscription",
							"Anthropic Console account",
							"3rd-party platform",
						},
					)
					loginAnsweredSnap = true
				}
			}

			// OAuth URL gate — Claude prints the device-code URL and waits for the user to
			// paste the returned code. Surface as an onboarding_external question to the
			// dashboard AND emit a structured log line for pks-cli to mirror in its console.
			if jobMode && postedOnboardingQuestionId == "" &&
				strings.Contains(plain, "Paste code here") &&
				strings.Contains(plain, "oauth/authorize") {

				// Pull the URL out of the pane. URL is on its own line right after
				// "Browser didn't open?" and may wrap across multiple lines on narrow panes.
				oauthURLRe := regexp.MustCompile(`https?://[^\s]+oauth/authorize\?\S+`)
				oauthURL := oauthURLRe.FindString(strings.ReplaceAll(plain, "\n", ""))
				if oauthURL == "" {
					// URL extraction failed — fall back to a generic message; user can copy from the pane.
					logDebug("[broadcast] OAuth gate detected but URL extraction failed\n")
				}

				idSource := fmt.Sprintf("onboarding-oauth:%s:%s", sessionID, oauthURL)
				hash := sha256.Sum256([]byte(idSource))
				questionId := fmt.Sprintf("onboarding-%x", hash[:8])

				question := "Sign in to Claude Code to continue"
				payload, _ := json.Marshal(map[string]interface{}{
					"sessionId":      sessionID,
					"type":           "metadata",
					"subtype":        "onboarding_external",
					"questionId":     questionId,
					"question":       question,
					"actionUrl":      oauthURL,
					"actionLabel":    "Open sign-in URL",
					"answerLabel":    "Paste the code from the browser",
					"provider":       "claude-subscription",
					"timestamp":      time.Now().Unix(),
				})
				metaURL := fmt.Sprintf("%s://%s/api/lives/metadata", snapSchemeBase, serverHost)
				if resp, err := http.Post(metaURL, "application/json", bytes.NewReader(payload)); err == nil {
					resp.Body.Close()
					postedOnboardingQuestionId = questionId
					logDebug("[broadcast] onboarding_external posted questionId=%s url=%s\n", questionId, oauthURL)
				} else {
					logDebug("[broadcast] onboarding_external metadata post error: %v\n", err)
				}
				// Also emit a structured log line so pks-cli runner can mirror it in its console.
				// Prefix is parsed by AgenticsRunnerStartCommand's vibecast.log tail.
				fmt.Printf("[onboarding-prompt] kind=oauth provider=claude-subscription questionId=%s url=%s\n", questionId, oauthURL)
			}

			// Detect the "session is too large" context-size menu and broadcast it as a vote
			// card so viewers can choose how Claude should continue. This menu appears at
			// Claude startup on resumed large sessions and is not backed by any hook.
			// Use ANSI-stripped text for detection — the selected menu item has color codes
			// injected between every word, breaking plain strings.Contains on raw output.
			// Runs in both job mode and standalone so viewers always get the vote card.
			if postedPaneQuestionId == "" {
				if strings.Contains(plain, "Resume from summary") &&
					strings.Contains(plain, "Resume full session") {

					// Extract the token-count line for a descriptive question text.
					// Format: "This session is <time> old and <N>k tokens."
					// Fall back to a generic label if the line isn't found.
					sessionSizeRe := regexp.MustCompile(`This session is [^\n]+tokens`)
					match := sessionSizeRe.FindString(plain)
					if match == "" {
						match = "Session too large"
					}
					idSource := fmt.Sprintf("session-size:%s:%s", sessionID, match)
					hash := sha256.Sum256([]byte(idSource))
					paneQuestionId := fmt.Sprintf("alp-pane-%x", hash[:8])

					question := fmt.Sprintf("%s — how should Claude continue?", match)
					options := []string{"Resume from summary", "Resume full session as-is"}

					payload, _ := json.Marshal(map[string]interface{}{
						"sessionId":      sessionID,
						"type":           "metadata",
						"subtype":        "alp_pane",
						"paneQuestionId": paneQuestionId,
						"question":       question,
						"options":        options,
						"timestamp":      time.Now().Unix(),
					})
					metaURL := fmt.Sprintf("%s://%s/api/lives/metadata", snapSchemeBase, serverHost)
					if resp, err := http.Post(metaURL, "application/json", bytes.NewReader(payload)); err == nil {
						resp.Body.Close()
						postedPaneQuestionId = paneQuestionId
						logDebug("[broadcast] alp_pane posted paneQuestionId=%s\n", paneQuestionId)
						// Injection is handled by the unified answer injection loop below.
					} else {
						logDebug("[broadcast] alp_pane metadata post error: %v\n", err)
					}
				}
			}

			body, _ := json.Marshal(map[string]string{
				"sessionId": sessionID,
				"snapshot":  rendered,
			})
			resp, err := http.Post(snapshotURL, "application/json", bytes.NewReader(body))
			if err != nil {
				logDebug("[broadcast] snapshot post error: %v\n", err)
				return
			}
			resp.Body.Close()
			logDebug("[broadcast] snapshot posted (%d bytes)\n", len(out))
		}

		// Initial-onboarding ticker: poll every 2s for the first 10 minutes so we catch
		// onboarding prompts (theme, login, OAuth, trust, post-OAuth Press-Enter screens).
		// 10 min covers a slow OAuth flow including the user opening the URL in a browser.
		// After that, drop to the regular 15s cadence to keep snapshot post rate sane.
		fastTicker := time.NewTicker(2 * time.Second)
		fastDeadline := time.After(10 * time.Minute)
		slowTicker := time.NewTicker(15 * time.Second)
		defer fastTicker.Stop()
		defer slowTicker.Stop()
		for {
			select {
			case <-done:
				postSnapshot() // final snapshot on disconnect
				return
			case <-fastTicker.C:
				postSnapshot()
			case <-fastDeadline:
				fastTicker.Stop()
			case <-slowTicker.C:
				postSnapshot()
			}
		}
	}()

	// Goroutine: unified answer injection loop (job mode only).
	// Polls the server for the current pending question's resolved answer and injects
	// the answer into Claude's pane via tmux send-keys.
	// Covers both AskUserQuestion (tool-backed) and alp_pane (pane-detected) questions.
	if jobMode {
		// tmuxSend sends one or more keys to target and records a child OTEL span under parent.
		// Keys are passed as individual arguments matching tmux send-keys semantics:
		//   tmuxSend(ctx, parent, target, "-l", "--", text)  — literal text
		//   tmuxSend(ctx, parent, target, "Enter")           — named key
		tmuxSend := func(ctx context.Context, parent trace.Span, target string, keys ...string) {
			_, s := telemetry.Tracer().Start(ctx, "vibecast.tmux.send_keys",
				trace.WithAttributes(
					attribute.String("tmux.target", target),
					attribute.String("tmux.keys", strings.Join(keys, " ")),
				))
			args := append([]string{"send-keys", "-t", target}, keys...)
			err := tmuxCmd(args...).Run()
			if err != nil {
				s.RecordError(err)
				logDebug("[answer] tmux send-keys error target=%s keys=%v: %v\n", target, keys, err)
			}
			s.End()
			_ = parent // keep reference for future parent linking
		}

		// subQuestion mirrors the per-sub-question shape the server returns for multi-step
		// AskUserQuestion batches. Each tab in the bubble-tea wizard has its own options and
		// multiSelect flag, so the injection loop needs them to navigate correctly rather than
		// typing literal text into a radio/checkbox list.
		type subQuestion struct {
			ToolUseID      string   `json:"toolUseId"`
			Question       string   `json:"question"`
			Options        []string `json:"options"`
			MultiSelect    bool     `json:"multiSelect"`
			ResolvedAnswer *string  `json:"resolvedAnswer"`
		}

		// answerHandler encapsulates injection logic for one (questionType, claudeVersionGlob) pair.
		// Version "*" matches any Claude Code version and is used as the default fallback.
		// Register version-specific handlers to override behaviour when the Claude Code UI changes.
		type answerHandler struct {
			questionType string // "permission", "alp_pane", "tool", or "*"
			version      string // glob pattern, e.g. "1.*", "2.*", or "*"
			inject       func(ctx context.Context, parent trace.Span, target, answer string, options []string, multiSelect bool, subQuestions []subQuestion)
		}

		// handlers is ordered most-specific first; first match wins.
		// To add a version-specific override, prepend it before the "*" entry:
		//   { questionType: "tool", version: "1.*", inject: legacyRadioInject }
		handlers := []answerHandler{
			{
				questionType: "permission",
				version:      "*",
				inject: func(ctx context.Context, parent trace.Span, target, answer string, _ []string, _ bool, _ []subQuestion) {
					// Claude Code native tool-use confirmation. "1" = Allow, "3" = Deny.
					key := "1"
					if strings.EqualFold(answer, "deny") || strings.EqualFold(answer, "no") {
						key = "3"
					}
					parent.SetAttributes(attribute.String("inject.method", "permission"), attribute.String("inject.key", key))
					tmuxSend(ctx, parent, target, key)
					time.Sleep(100 * time.Millisecond)
					tmuxSend(ctx, parent, target, "Enter")
					logDebug("[answer] permission injected key=%s for answer=%s\n", key, answer)
				},
			},
			{
				questionType: "alp_pane",
				version:      "*",
				inject: func(ctx context.Context, parent trace.Span, target, answer string, options []string, _ bool, _ []subQuestion) {
					// Numbered menu rendered by the ALP pane UI.
					digit := "1"
					for i, opt := range options {
						if strings.EqualFold(opt, answer) {
							digit = fmt.Sprintf("%d", i+1)
							break
						}
					}
					parent.SetAttributes(attribute.String("inject.method", "alp_pane"), attribute.String("inject.digit", digit))
					tmuxSend(ctx, parent, target, digit)
					time.Sleep(100 * time.Millisecond)
					tmuxSend(ctx, parent, target, "Enter")
					logDebug("[answer] alp_pane injected digit=%s\n", digit)
				},
			},
			{
				questionType: "onboarding_external",
				version:      "*",
				inject: func(ctx context.Context, parent trace.Span, target, answer string, _ []string, _ bool, _ []subQuestion) {
					// External-action onboarding (currently OAuth code paste). The user
					// completed the action externally and the answer is the literal string
					// to type into the prompt followed by Enter.
					parent.SetAttributes(
						attribute.String("inject.method", "onboarding_external"),
						attribute.Int("inject.answer_len", len(answer)),
					)
					tmuxSend(ctx, parent, target, "-l", "--", answer)
					time.Sleep(100 * time.Millisecond)
					tmuxSend(ctx, parent, target, "Enter")
					logDebug("[answer] onboarding_external injected (len=%d)\n", len(answer))
				},
			},
			{
				questionType: "tool",
				version:      "*",
				inject: func(ctx context.Context, parent trace.Span, target, answer string, options []string, multiSelect bool, subQuestions []subQuestion) {
					// Tool question (AskUserQuestion / AskFollowupQuestion).
					// Prefer the server-provided per-sub-question shape when present — it lets us drive
					// the bubble-tea wizard with actual navigation (Down/Enter/Tab) per tab instead of
					// typing literal text into a radio/checkbox list (which scrambles wizard state,
					// especially when the batch mixes radio and multi-select questions).
					isMultiStep := len(subQuestions) > 1
					if isMultiStep {
						parent.SetAttributes(
							attribute.String("inject.method", "multi_step"),
							attribute.Int("inject.steps", len(subQuestions)),
						)
						for idx, sq := range subQuestions {
							resolved := ""
							if sq.ResolvedAnswer != nil {
								resolved = *sq.ResolvedAnswer
							}
							if sq.MultiSelect {
								// Checkbox tab: toggle each selected option in order, then Tab to the next tab.
								rawSelected := strings.Split(resolved, " | ")
								selectedSet := make(map[string]bool)
								for _, a := range rawSelected {
									a = strings.TrimSpace(a)
									for _, opt := range sq.Options {
										if strings.EqualFold(opt, a) {
											selectedSet[opt] = true
										}
									}
								}
								cur := 0
								for i, opt := range sq.Options {
									if selectedSet[opt] {
										for cur < i {
											time.Sleep(80 * time.Millisecond)
											tmuxSend(ctx, parent, target, "Down")
											cur++
										}
										time.Sleep(100 * time.Millisecond)
										tmuxSend(ctx, parent, target, "Enter") // toggle
									}
								}
								// Tab advances to the next top-level tab (next question, or Submit if this was the last).
								time.Sleep(120 * time.Millisecond)
								tmuxSend(ctx, parent, target, "Tab")
							} else {
								// Radio tab: navigate Down to the matching option, Enter auto-advances to the next tab.
								targetIdx := 0
								for i, opt := range sq.Options {
									if strings.EqualFold(opt, resolved) {
										targetIdx = i
										break
									}
								}
								for j := 0; j < targetIdx; j++ {
									tmuxSend(ctx, parent, target, "Down")
									time.Sleep(80 * time.Millisecond)
								}
								time.Sleep(100 * time.Millisecond)
								tmuxSend(ctx, parent, target, "Enter")
							}
							parent.AddEvent(fmt.Sprintf("subquestion_%d_injected", idx),
								trace.WithAttributes(
									attribute.String("sub.tool_use_id", sq.ToolUseID),
									attribute.Bool("sub.multi_select", sq.MultiSelect),
									attribute.Int("sub.options_count", len(sq.Options)),
								))
							time.Sleep(250 * time.Millisecond)
						}
						// After the last sub-question, the wizard should be on the Submit tab with
						// "Submit answers" highlighted. Enter confirms.
						tmuxSend(ctx, parent, target, "Enter")
					} else if len(options) > 0 {
						if multiSelect {
							// Checkbox list: parse pipe-separated answer, toggle each selected option in order.
							// BubbleTea layout: [opt0][opt1]...[Type something][Submit]
							// "Type something" is always present but not in the options slice.
							rawSelected := strings.Split(answer, " | ")
							selectedSet := make(map[string]bool)
							for _, a := range rawSelected {
								a = strings.TrimSpace(a)
								for _, opt := range options {
									if strings.EqualFold(opt, a) {
										selectedSet[opt] = true
									}
								}
							}
							parent.SetAttributes(
								attribute.String("inject.method", "multi_select_checkbox"),
								attribute.Int("inject.selected_count", len(selectedSet)),
							)
							cur := 0
							lastSelectedPos := -1
							for i, opt := range options {
								if selectedSet[opt] {
									for cur < i {
										time.Sleep(80 * time.Millisecond)
										tmuxSend(ctx, parent, target, "Down")
										cur++
									}
									time.Sleep(100 * time.Millisecond)
									tmuxSend(ctx, parent, target, "Enter") // toggle this option
									lastSelectedPos = i
								}
							}
							if lastSelectedPos == -1 {
								lastSelectedPos = 0 // fallback: no match, navigate from start
							}
							// From lastSelectedPos, navigate past remaining options + "Type something" to Submit.
							remainingDown := len(options) - lastSelectedPos + 1
							for i := 0; i < remainingDown; i++ {
								time.Sleep(80 * time.Millisecond)
								tmuxSend(ctx, parent, target, "Down")
							}
							time.Sleep(100 * time.Millisecond)
							tmuxSend(ctx, parent, target, "Enter") // Submit
						} else {
							// Radio list: navigate to the matching option, Enter submits immediately.
							targetIdx := 1
							for i, opt := range options {
								if strings.EqualFold(opt, answer) {
									targetIdx = i + 1
									break
								}
							}
							parent.SetAttributes(
								attribute.String("inject.method", "select_list"),
								attribute.Int("inject.target_idx", targetIdx),
								attribute.Bool("inject.multi_select", false),
							)
							for i := 1; i < targetIdx; i++ {
								tmuxSend(ctx, parent, target, "Down")
								time.Sleep(100 * time.Millisecond)
							}
							tmuxSend(ctx, parent, target, "Enter")
						}
					} else {
						parent.SetAttributes(attribute.String("inject.method", "free_text"))
						tmuxSend(ctx, parent, target, "-l", "--", answer)
						time.Sleep(100 * time.Millisecond)
						tmuxSend(ctx, parent, target, "Enter")
					}
					logDebug("[answer] tool question injected for questionId=%s\n", answer)
				},
			},
		}

		go func() {
			type pendingAnswerResponse struct {
				QuestionID   *string       `json:"questionId"`
				QuestionType string        `json:"questionType"`
				Options      []string      `json:"options"`
				Answer       *string       `json:"answer"`
				MultiSelect  bool          `json:"multiSelect"`
				SubQuestions []subQuestion `json:"subQuestions"`
			}
			injectionTarget := "vibecast-" + sessionID + ":" + paneId + ".0"
			lastInjectedQuestionID := ""
			pollURL := fmt.Sprintf("%s://%s/api/lives/sessions/%s/pending-answer",
				snapSchemeBase, serverHost, sessionID)
			ticker := time.NewTicker(3 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					pollCtx := context.Background()
					_, pollSpan := telemetry.Tracer().Start(pollCtx, "vibecast.answer.poll",
						trace.WithAttributes(
							attribute.String("session.id", sessionID),
							attribute.String("poll.url", pollURL),
						))
					resp, err := http.Get(pollURL)
					if err != nil {
						pollSpan.RecordError(err)
						pollSpan.End()
						logDebug("[answer] poll error: %v\n", err)
						continue
					}
					var r pendingAnswerResponse
					json.NewDecoder(resp.Body).Decode(&r)
					resp.Body.Close()

					hasQuestion := r.QuestionID != nil && *r.QuestionID != ""
					hasAnswer := r.Answer != nil
					pollSpan.SetAttributes(
						attribute.Bool("poll.has_question", hasQuestion),
						attribute.Bool("poll.has_answer", hasAnswer),
						attribute.String("poll.question_type", r.QuestionType),
					)
					if hasQuestion {
						pollSpan.SetAttributes(attribute.String("poll.question_id", *r.QuestionID))
					}
					pollSpan.End()

					if !hasQuestion || !hasAnswer {
						continue
					}
					qID := *r.QuestionID
					answer := *r.Answer
					if qID == lastInjectedQuestionID {
						continue
					}
					lastInjectedQuestionID = qID
					logDebug("[answer] injecting answer %q for questionId=%s type=%s\n", answer, qID, r.QuestionType)

					injectCtx := context.Background()
					_, injectSpan := telemetry.Tracer().Start(injectCtx, "vibecast.answer.inject",
						trace.WithAttributes(
							attribute.String("session.id", sessionID),
							attribute.String("question.id", qID),
							attribute.String("question.type", r.QuestionType),
							attribute.String("answer.preview", func() string {
								if len(answer) > 80 { return answer[:80] }
								return answer
							}()),
						))

					// Select the handler: match questionType then version "*" fallback.
					questionType := r.QuestionType
					if questionType != "permission" && questionType != "alp_pane" && questionType != "onboarding_external" {
						questionType = "tool"
					}
					var selected *answerHandler
					for i := range handlers {
						h := &handlers[i]
						if h.questionType == questionType || h.questionType == "*" {
							selected = h
							break
						}
					}
					if selected != nil {
						selected.inject(injectCtx, injectSpan, injectionTarget, answer, r.Options, r.MultiSelect, r.SubQuestions)
					} else {
						logDebug("[answer] no handler for questionType=%s\n", r.QuestionType)
						injectSpan.SetAttributes(attribute.String("inject.method", "no_handler"))
					}
					injectSpan.End()
				}
			}
		}()
	}

	// Goroutine: drain metaCh and send metadata text frames to server
	go func() {
		for msg := range metaCh {
			if err := ws.SendText(serverConn, msg); err != nil {
				logDebug("[broadcast] metadata send error: %v\n", err)
				return
			}
		}
	}()

	// Goroutine A: ttyd -> server (relay all frames)
	// Also scans stdout (0x30 frames) for URLs and reports them via metadata API,
	// and auto-answers the Claude Code workspace trust dialog in job mode.
	seenURLs := map[string]bool{}
	var urlBuf strings.Builder
	trustAnswered := false
	var lastCaptureCheck time.Time
	go func() {
		defer close(done)
		for {
			opcode, payload, err := ws.ReadFrame(ttydReader)
			if err != nil {
				logDebug("[broadcast] ttyd read error: %v\n", err)
				return
			}
			switch opcode {
			case 8:
				logDebug("[broadcast] ttyd sent close frame\n")
				return
			case 9:
				ws.SendPong(ttydConn, payload)
			case 10:
			case 1:
				logDebug("[broadcast] ttyd text (not relayed): %s\n", string(payload))
			case 2:
				if len(payload) > 0 && payload[0] == 0x30 {
					if err := ws.SendBinary(serverConn, payload); err != nil {
						logDebug("[broadcast] server write error (bin): %v\n", err)
						return
					}
					// Scan for URLs in stdout, keep a rolling 8KB buffer
					urlBuf.Write(payload[1:])
					if urlBuf.Len() > 8192 {
						s := urlBuf.String()
						urlBuf.Reset()
						urlBuf.WriteString(s[len(s)-4096:])
					}
					clean := ansiRE.ReplaceAllString(urlBuf.String(), "")
					for _, u := range urlRE.FindAllString(clean, -1) {
						if !seenURLs[u] {
							seenURLs[u] = true
							ctx := classifyURL(u)
							go postURLDetected(serverHost, sessionID, u, ctx)
						}
					}
					// In job mode, auto-answer Claude Code's workspace trust dialog.
					// Claude's Bubble Tea TUI renders with cursor movements so the raw
					// VT100 stream doesn't contain "Quick safety check" as a contiguous
					// string. Use tmux capture-pane (rendered screen text) instead,
					// debounced to at most once per 500ms to avoid excess subprocess calls.
					if jobMode && !trustAnswered && time.Since(lastCaptureCheck) > 500*time.Millisecond {
						lastCaptureCheck = time.Now()
						target := fmt.Sprintf("vibecast-%s:%s.0", sessionID, paneId)
						rendered, captureErr := tmuxCmd("capture-pane", "-p", "-t", target).Output()
						if captureErr == nil {
							renderedStr := string(rendered)
							if strings.Contains(renderedStr, "Quick safety check") {
								allowedDir := os.Getenv("VIBECAST_ALLOWED_DIRECTORIES")
								_, span := telemetry.Tracer().Start(
									telemetry.ContextFromTraceparent(os.Getenv("TRACEPARENT")),
									"vibecast.trust_prompt",
								)
								span.SetAttributes(
									attribute.String("session.id", sessionID),
									attribute.String("allowed_dir", allowedDir),
								)
								if allowedDir != "" && strings.Contains(renderedStr, allowedDir) {
									tmuxCmd("send-keys", "-t", target, "Enter").Run()
									logDebug("[broadcast] auto-answered workspace trust prompt for %s\n", allowedDir)
									span.SetAttributes(attribute.Bool("auto_answered", true))
									trustAnswered = true
								} else {
									logDebug("[broadcast] trust prompt path does not match allowed dir %q — not auto-answering\n", allowedDir)
									span.SetAttributes(attribute.Bool("auto_answered", false))
								}
								span.End()
							}
						}
					}
				} else if len(payload) > 0 {
					logDebug("[broadcast] ttyd binary type 0x%02x (not relayed, %d bytes)\n", payload[0], len(payload))
				}
			}
		}
	}()

	// Goroutine B: server -> ttyd (relay viewer resize, init back to ttyd)
	// Also handles keyboard input messages from viewers (validated via PIN)
	kbPinHash := ""
	if pin := os.Getenv("VIBECAST_KEYBOARD_PIN"); pin != "" {
		h := sha256.Sum256([]byte(pin))
		kbPinHash = fmt.Sprintf("%x", h)
	}
	tmuxSessName := "vibecast-" + sessionID

	go func() {
		for {
			opcode, payload, err := ws.ReadFrame(serverReader)
			if err != nil {
				logDebug("[broadcast] server read error: %v\n", err)
				return
			}
			switch opcode {
			case 8:
				logDebug("[broadcast] server sent close frame\n")
				return
			case 9:
				ws.SendPong(serverConn, payload)
			case 10:
			case 1:
				// Check if this is a keyboard input message from a viewer
				if handleKeyboardInput(payload, kbPinHash, tmuxSessName, paneId) {
					continue // handled, don't forward to ttyd
				}
				ws.SendText(ttydConn, payload)
			case 2:
				ws.SendBinary(ttydConn, payload)
			}
		}
	}()

	<-done
	logDebug("[broadcast] relay disconnected for session %s\n", sessionID)
}

// handleKeyboardInput processes keyboard input messages from viewers.
// Returns true if the message was a keyboard message (handled or rejected).
func handleKeyboardInput(payload []byte, expectedPinHash, tmuxSession, paneId string) bool {
	var msg struct {
		Type    string `json:"type"`
		Data    string `json:"data,omitempty"`
		Key     string `json:"key,omitempty"`
		PaneID  string `json:"paneId,omitempty"`
		PinHash string `json:"pinHash"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil {
		return false
	}
	if msg.Type != "input" && msg.Type != "special-key" {
		return false
	}

	// Validate PIN hash
	if expectedPinHash == "" || msg.PinHash != expectedPinHash {
		logDebug("[keyboard] rejected: invalid PIN hash\n")
		return true // consumed but rejected
	}

	targetPane := msg.PaneID
	if targetPane == "" {
		targetPane = paneId
	}
	tmuxTarget := tmuxSession + ":" + targetPane + ".0"

	if msg.Type == "input" && msg.Data != "" && len(msg.Data) < 4096 {
		cmd := exec.Command("tmux", "send-keys", "-t", tmuxTarget, "-l", "--", msg.Data)
		if err := cmd.Run(); err != nil {
			logDebug("[keyboard] send-keys error: %v\n", err)
		}
	} else if msg.Type == "special-key" && msg.Key != "" {
		allowed := map[string]bool{
			"Enter": true, "Escape": true, "Tab": true,
			"Up": true, "Down": true, "Left": true, "Right": true,
			"BSpace": true, "C-c": true, "C-d": true,
			"C-z": true, "C-a": true, "C-e": true, "C-l": true, "Space": true,
		}
		if allowed[msg.Key] {
			cmd := exec.Command("tmux", "send-keys", "-t", tmuxTarget, msg.Key)
			if err := cmd.Run(); err != nil {
				logDebug("[keyboard] send-keys error: %v\n", err)
			}
		}
	}

	return true
}

// ConnectChat connects to the chat WebSocket and sends received messages to the TUI program.
func ConnectChat(sessionID string, program *tea.Program) {
	serverHost := func() string {
		if h := os.Getenv("AGENTICS_SERVER"); h != "" {
			return h
		}
		if h := os.Getenv("AGENTIC_SERVER"); h != "" {
			return h
		}
		return "agentics.dk"
	}()

	conn, err := ws.Connect(serverHost, "/api/lives/chat/ws")
	if err != nil {
		return
	}

	joinMsg, _ := json.Marshal(types.ChatMsg{
		Type:      "join",
		SessionID: sessionID,
		Username:  "Broadcaster",
	})
	ws.SendText(conn, joinMsg)

	reader := bufio.NewReader(conn)
	for {
		data, err := ws.ReadMessage(reader)
		if err != nil {
			return
		}
		var msg types.ChatMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		program.Send(types.ChatMsgReceived{Msg: msg})
	}
}
