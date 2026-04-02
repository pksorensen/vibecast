package broadcast

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/pksorensen/vibecast/internal/auth"
	"github.com/pksorensen/vibecast/internal/types"
	"github.com/pksorensen/vibecast/internal/util"
	ws "github.com/pksorensen/vibecast/internal/websocket"

	tea "github.com/charmbracelet/bubbletea"
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

func postURLDetected(serverHost, streamID, u, context string) {
	scheme := "https"
	if util.IsLocalHost(serverHost) {
		scheme = "http"
	}
	apiURL := fmt.Sprintf("%s://%s/api/lives/metadata", scheme, serverHost)
	body, _ := json.Marshal(map[string]interface{}{
		"type":      "metadata",
		"subtype":   "url_detected",
		"streamId":  streamID,
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
func ConnectBroadcast(streamID string, status *types.SharedStatus, metaCh chan []byte, ttydPort int, paneId string) {
	for attempt := 0; attempt < 120; attempt++ {
		if attempt > 0 {
			time.Sleep(2 * time.Second)
		}
		status.Mu.Lock()
		host := status.ServerHost
		status.Mu.Unlock()
		connectBroadcastOnce(streamID, host, status, metaCh, ttydPort, attempt, paneId)
	}
}

func connectBroadcastOnce(streamID string, serverHost string, status *types.SharedStatus, metaCh chan []byte, ttydPort int, attempt int, paneId string) {
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
	broadcastPath := "/api/lives/broadcast/ws?streamId=" + streamID + "&paneId=" + paneId
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

	logDebug("[broadcast] relay active for stream %s\n", streamID)

	done := make(chan struct{})

	// Goroutine: poll broadcaster's terminal size and propagate to tmux -> viewers
	go func() {
		tmuxSess := "vibecast-" + streamID
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

				out, err := exec.Command("tmux", "display-message", "-t", tmuxTarget, "-p", "#{pane_width} #{pane_height}").Output()
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
	go func() {
		snapTmuxTarget := "vibecast-" + streamID + ":" + paneId
		snapScheme := "https"
		if util.IsLocalHost(serverHost) {
			snapScheme = "http"
		}
		snapshotURL := fmt.Sprintf("%s://%s/_relay/snapshot", snapScheme, serverHost)

		postSnapshot := func() {
			out, err := exec.Command("tmux", "capture-pane", "-p", "-e", "-t", snapTmuxTarget).Output()
			if err != nil {
				logDebug("[broadcast] capture-pane error: %v\n", err)
				return
			}
			body, _ := json.Marshal(map[string]string{
				"streamId": streamID,
				"snapshot": string(out),
			})
			resp, err := http.Post(snapshotURL, "application/json", bytes.NewReader(body))
			if err != nil {
				logDebug("[broadcast] snapshot post error: %v\n", err)
				return
			}
			resp.Body.Close()
			logDebug("[broadcast] snapshot posted (%d bytes)\n", len(out))
		}

		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				postSnapshot() // final snapshot on disconnect
				return
			case <-ticker.C:
				postSnapshot()
			}
		}
	}()

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
	// Also scans stdout (0x30 frames) for URLs and reports them via metadata API.
	seenURLs := map[string]bool{}
	var urlBuf strings.Builder
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
							go postURLDetected(serverHost, streamID, u, ctx)
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
	tmuxSessName := "vibecast-" + streamID

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
	logDebug("[broadcast] relay disconnected for stream %s\n", streamID)
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
func ConnectChat(streamID string, program *tea.Program) {
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
		Type:     "join",
		StreamID: streamID,
		Username: "Broadcaster",
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
