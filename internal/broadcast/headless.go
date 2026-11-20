package broadcast

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/pksorensen/vibecast/internal/types"
	"github.com/pksorensen/vibecast/internal/util"
)

// headlessSessionGoneChecks is how many consecutive failed `tmux has-session` probes
// (2s apart) end a headless run. The streaming session is only absent once vibecast
// has torn it down, so a short run of misses is enough to tell that from a blip.
const headlessSessionGoneChecks = 3

// runHeadless is ConnectBroadcast for VIBECAST_BROADCAST=off. It never dials the server
// (no broadcast WebSocket, no /_relay/snapshot) but keeps every control-plane duty that
// connectBroadcastOnce carries today:
//
//   - the pane watch: Claude Code first-run auto-answers and pane-detected questions
//     (alp_pane, onboarding_external), posted as metadata over HTTP;
//   - the job-mode pending-answer loop (GET /api/lives/sessions/{id}/pending-answer);
//   - draining metaCh to POST /api/lives/metadata.
//
// Unlike the broadcast path it is not torn down and rebuilt per WebSocket attempt: the
// state lives for the whole session, and the loops end once the tmux session is gone.
func runHeadless(sessionID string, status *types.SharedStatus, metaCh chan []byte, paneId string) {
	serverHost := currentServerHost(status)
	tmuxCmd := newTmuxCmd()
	done := make(chan struct{})

	logDebug("[headless] broadcast off — HTTP-only control plane for session %s pane %s (server %s)\n", sessionID, paneId, serverHost)

	go drainMetaHTTP(sessionID, status, metaCh, done)

	jobMode := os.Getenv("AGENTICS_JOB_MODE") == "1"
	auto := &paneAutomation{}
	go runPaneWatch(done, sessionID, serverHost, status, paneId, tmuxCmd, auto, false)
	if jobMode {
		startAnswerLoop(done, sessionID, serverHost, paneId, tmuxCmd)
	}

	sessName := "vibecast-" + sessionID
	misses := 0
	for {
		time.Sleep(2 * time.Second)
		if tmuxCmd("has-session", "-t", sessName).Run() != nil {
			misses++
			if misses >= headlessSessionGoneChecks {
				break
			}
			continue
		}
		misses = 0
	}
	close(done)
	logDebug("[headless] session %s gone — control-plane loops stopped\n", sessionID)
}

// currentServerHost returns the server the session is streaming to.
func currentServerHost(status *types.SharedStatus) string {
	host := ""
	if status != nil {
		status.Mu.Lock()
		host = status.ServerHost
		status.Mu.Unlock()
	}
	if host == "" {
		host = util.GetServerHost()
	}
	return host
}

// newTmuxCmd returns a tmux command builder bound to the socket in $TMUX (format
// "socket,pid,session"), so exec'd tmux subcommands reach the session's server when
// vibecast runs on a non-default socket.
func newTmuxCmd() func(args ...string) *exec.Cmd {
	tmuxSocket := ""
	if tmuxEnv := os.Getenv("TMUX"); tmuxEnv != "" {
		if idx := strings.Index(tmuxEnv, ","); idx > 0 {
			tmuxSocket = tmuxEnv[:idx]
		}
	}
	return func(args ...string) *exec.Cmd {
		if tmuxSocket != "" {
			return exec.Command("tmux", append([]string{"-S", tmuxSocket}, args...)...)
		}
		return exec.Command("tmux", args...)
	}
}

// drainMetaHTTP is the broadcast-off counterpart of the metaCh → WebSocket text-frame
// drain: each frame is POSTed to /api/lives/metadata instead. Frames written for the
// WebSocket carry no sessionId (the socket implied it), so it is added when missing.
// Frames are posted one at a time, in order. Returns when done is closed.
func drainMetaHTTP(sessionID string, status *types.SharedStatus, metaCh chan []byte, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case msg, ok := <-metaCh:
			if !ok {
				return
			}
			body, err := withSessionID(msg, sessionID)
			if err != nil {
				logDebug("[headless] metadata frame is not a JSON object, dropped: %v\n", err)
				continue
			}
			url := util.ServerURL(currentServerHost(status), "/api/lives/metadata")
			resp, err := util.PostLivesJSON(url, body)
			if err != nil {
				logDebug("[headless] metadata post error: %v\n", err)
				continue
			}
			resp.Body.Close()
		}
	}
}

// withSessionID returns msg with "sessionId" set when it is absent or empty.
func withSessionID(msg []byte, sessionID string) ([]byte, error) {
	var m map[string]interface{}
	if err := json.Unmarshal(msg, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]interface{}{}
	}
	if v, ok := m["sessionId"].(string); !ok || v == "" {
		m["sessionId"] = sessionID
	}
	return json.Marshal(m)
}
