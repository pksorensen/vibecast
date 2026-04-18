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
	// Track posted alp_pane question so we only post once per session-size menu appearance.
	postedPaneQuestionId := ""
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
					if allowedDir != "" && strings.Contains(rendered, allowedDir) {
						// The trust dialog defaults to "❯ 1. Yes, I trust this folder".
						// Send Enter alone — sending "1" + Enter in one call doesn't
						// register reliably in BubbleTea; Enter on the default is enough.
						tmuxCmd("send-keys", "-t", snapTmuxTarget+".0", "Enter").Run()
						logDebug("[broadcast] auto-answered workspace trust prompt for %s\n", allowedDir)
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

		// Fast initial ticker: check every 500ms for the first 30s so we catch the
		// trust prompt quickly, then switch to the regular 15s cadence.
		fastTicker := time.NewTicker(500 * time.Millisecond)
		fastDeadline := time.After(30 * time.Second)
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
		go func() {
			type pendingAnswerResponse struct {
				QuestionID   *string  `json:"questionId"`
				QuestionType string   `json:"questionType"`
				Options      []string `json:"options"`
				Answer       *string  `json:"answer"`
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
					_, pollSpan := telemetry.Tracer().Start(context.Background(), "vibecast.answer.poll",
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

					_, injectSpan := telemetry.Tracer().Start(context.Background(), "vibecast.answer.inject",
						trace.WithAttributes(
							attribute.String("session.id", sessionID),
							attribute.String("question.id", qID),
							attribute.String("question.type", r.QuestionType),
							attribute.String("answer.preview", func() string {
								if len(answer) > 80 { return answer[:80] }
								return answer
							}()),
						))

					if r.QuestionType == "permission" {
						// Claude Code native file-create/overwrite confirmation TUI.
						// Options are ["Allow", "Deny"] (or similar). The TUI is a BubbleTea
						// selectlist: "1. Yes  2. Yes, and allow settings  3. No".
						// Allow → press "1" + Enter; Deny → press "3" + Enter.
						key := "1"
						if strings.EqualFold(answer, "deny") || strings.EqualFold(answer, "no") {
							key = "3"
						}
						injectSpan.SetAttributes(attribute.String("inject.method", "permission"), attribute.String("inject.key", key))
						tmuxCmd("send-keys", "-t", injectionTarget, key).Run()
						time.Sleep(100 * time.Millisecond)
						tmuxCmd("send-keys", "-t", injectionTarget, "Enter").Run()
						logDebug("[answer] permission injected key=%s for answer=%s\n", key, answer)
					} else if r.QuestionType == "alp_pane" {
						// Numbered menu: map answer label to 1-based digit.
						digit := "1"
						for i, opt := range r.Options {
							if strings.EqualFold(opt, answer) {
								digit = fmt.Sprintf("%d", i+1)
								break
							}
						}
						tmuxCmd("send-keys", "-t", injectionTarget, digit).Run()
						time.Sleep(100 * time.Millisecond)
						tmuxCmd("send-keys", "-t", injectionTarget, "Enter").Run()
						injectSpan.SetAttributes(attribute.String("inject.method", "alp_pane"), attribute.String("inject.digit", digit))
						logDebug("[answer] alp_pane injected digit=%s\n", digit)
					} else {
						// Tool question (AskUserQuestion / AskFollowupQuestion).
						// Answer may be a single option label or a multi-step Q&A block
						// (paragraphs separated by \n\n, each "question\nanswer").
						paragraphs := strings.Split(answer, "\n\n")
						isMultiStep := len(paragraphs) > 1 && len(r.Options) == 0
						if isMultiStep {
							injectSpan.SetAttributes(attribute.String("inject.method", "multi_step"), attribute.Int("inject.steps", len(paragraphs)))
							for _, para := range paragraphs {
								lines := strings.SplitN(strings.TrimSpace(para), "\n", 2)
								if len(lines) < 2 {
									continue
								}
								stepAnswer := strings.TrimSpace(lines[1])
								tmuxCmd("send-keys", "-t", injectionTarget, "-l", "--", stepAnswer).Run()
								time.Sleep(200 * time.Millisecond)
								tmuxCmd("send-keys", "-t", injectionTarget, "Enter").Run()
								time.Sleep(300 * time.Millisecond)
							}
							// Tab to Submit button then confirm
							tmuxCmd("send-keys", "-t", injectionTarget, "Tab").Run()
							time.Sleep(100 * time.Millisecond)
							tmuxCmd("send-keys", "-t", injectionTarget, "Enter").Run()
						} else if len(r.Options) > 0 {
							// Single-step with known options: navigate BubbleTea selectlist.
							targetIdx := 1
							for i, opt := range r.Options {
								if strings.EqualFold(opt, answer) {
									targetIdx = i + 1
									break
								}
							}
							injectSpan.SetAttributes(attribute.String("inject.method", "select_list"), attribute.Int("inject.target_idx", targetIdx))
							for i := 1; i < targetIdx; i++ {
								tmuxCmd("send-keys", "-t", injectionTarget, "Down").Run()
								time.Sleep(100 * time.Millisecond)
							}
							tmuxCmd("send-keys", "-t", injectionTarget, "Enter").Run()
						} else {
							// Free text: type directly.
							injectSpan.SetAttributes(attribute.String("inject.method", "free_text"))
							tmuxCmd("send-keys", "-t", injectionTarget, "-l", "--", answer).Run()
							time.Sleep(100 * time.Millisecond)
							tmuxCmd("send-keys", "-t", injectionTarget, "Enter").Run()
						}
						logDebug("[answer] tool question injected for questionId=%s\n", qID)
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
