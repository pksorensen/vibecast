package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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

// RestartFunc is a callback for restarting Claude. Parameters: tmuxSession, resume, claudeSessionID, paneId (optional).
type RestartFunc func(sessionName string, resume bool, claudeSessionID string, paneId ...string) error

// ControlSocketPath returns the path to the control socket.
func ControlSocketPath() string {
	return filepath.Join(session.VibecastDir(), "control.sock")
}

// StartControlServer starts the Unix socket control server.
func StartControlServer(status *types.SharedStatus, program *tea.Program, restartClaude RestartFunc) (net.Listener, error) {
	sockPath := ControlSocketPath()
	os.MkdirAll(filepath.Dir(sockPath), 0755)
	os.Remove(sockPath)

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/restart-claude", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		clearContext := r.URL.Query().Get("clearContext") == "true"
		resume := !clearContext
		status.Mu.Lock()
		csID := status.ClaudeSessionID
		tmuxSess := status.TmuxSession
		status.Mu.Unlock()
		logDebug("[control] restart-claude request received (resume=%v, claudeSessionID=%s)\n", resume, csID)
		go func() {
			logDebug("[control] executing restart for session=%s resume=%v\n", tmuxSess, resume)
			restartClaude(tmuxSess, resume, csID)
			program.Send(types.ControlRestartMsg{})
		}()
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"ok":true,"message":"restart signal sent"}`)
	})

	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		status.Mu.Lock()
		data, _ := json.Marshal(map[string]interface{}{
			"streamId":      status.StreamID,
			"url":           status.URL,
			"pinCode":       status.PinCode,
			"viewers":       status.Viewers,
			"uptime":        status.Uptime,
			"phase":         status.Phase,
			"pendingImages": status.PendingImages,
		})
		status.Mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	})

	mux.HandleFunc("/stop-broadcast", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		msg := types.ControlStopMsg{
			Message:    r.URL.Query().Get("message"),
			Conclusion: r.URL.Query().Get("conclusion"),
		}
		// Also accept JSON body
		if r.Body != nil {
			var body struct {
				Message    string `json:"message"`
				Conclusion string `json:"conclusion"`
				GitCommit  string `json:"gitCommit"`
				GitBranch  string `json:"gitBranch"`
			}
			if json.NewDecoder(r.Body).Decode(&body) == nil {
				if body.Message != "" {
					msg.Message = body.Message
				}
				if body.Conclusion != "" {
					msg.Conclusion = body.Conclusion
				}
				msg.GitCommit = body.GitCommit
				msg.GitBranch = body.GitBranch
			}
		}
		go program.Send(msg)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"ok":true,"message":"Session will end in 10 seconds. Final metadata and transcript will be flushed before disconnecting."}`)
	})

	mux.HandleFunc("/change-server", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		newHost := r.URL.Query().Get("host")
		if newHost == "" {
			http.Error(w, `{"error":"host parameter required"}`, http.StatusBadRequest)
			return
		}

		status.Mu.Lock()
		status.ServerHost = newHost
		newURL := util.BuildViewerURL(newHost, status.StreamID)
		status.URL = newURL
		connToClose := status.ServerConn
		streamID := status.StreamID
		status.Mu.Unlock()

		sfPath := filepath.Join(session.SessionsDir(), streamID+".json")
		if sf, err := session.ReadSessionFile(sfPath); err == nil {
			sf.ServerHost = newHost
			session.WriteSessionFile(*sf)
		}

		program.Send(types.ControlServerChangedMsg{URL: newURL})

		if connToClose != nil {
			connToClose.Close()
		}

		w.Header().Set("Content-Type", "application/json")
		data, _ := json.Marshal(map[string]interface{}{
			"ok":         true,
			"serverHost": newHost,
			"url":        newURL,
		})
		w.Write(data)
	})

	mux.HandleFunc("/image-queued", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var img types.PendingImage
		if err := json.NewDecoder(r.Body).Decode(&img); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		program.Send(types.ImageQueuedMsg{Img: img})
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"ok":true}`)
	})

	mux.HandleFunc("/configure-otel", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var cfg struct {
			Endpoint    string `json:"endpoint"`
			Insecure    bool   `json:"insecure"`
			ServiceName string `json:"service_name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
			return
		}
		if cfg.Endpoint == "" {
			http.Error(w, `{"error":"endpoint is required"}`, http.StatusBadRequest)
			return
		}
		status.Mu.Lock()
		oldShutdown := status.OtelShutdown
		status.Mu.Unlock()

		newShutdown, err := telemetry.ConfigureOTEL(oldShutdown, cfg.Endpoint, cfg.Insecure, cfg.ServiceName)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			data, _ := json.Marshal(map[string]interface{}{"ok": false, "error": err.Error()})
			w.Write(data)
			return
		}

		status.Mu.Lock()
		status.OtelShutdown = newShutdown
		status.Mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		data, _ := json.Marshal(map[string]interface{}{
			"ok":           true,
			"endpoint":     cfg.Endpoint,
			"insecure":     cfg.Insecure,
			"service_name": cfg.ServiceName,
		})
		w.Write(data)
	})

	mux.HandleFunc("/fkey", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		key := r.URL.Query().Get("key")
		if key == "" {
			http.Error(w, `{"error":"key parameter required"}`, http.StatusBadRequest)
			return
		}

		// F1 (info toggle), F3 (prev), F4 (next) are handled by tmux bindings directly.
		// Only F2, F5, F6, F10 come through the control socket.
		var action string
		switch key {
		case "f2": // New pane
			program.Send(types.FKeyActionMsg{Key: key})
			action = "spawn_pane"
		case "f5": // Close pane
			program.Send(types.FKeyActionMsg{Key: key})
			action = "close_pane"
		case "f6": // Restart Claude
			go func() {
				status.Mu.Lock()
				csID := status.ClaudeSessionID
				tmuxSess := status.TmuxSession
				status.Mu.Unlock()
				restartClaude(tmuxSess, true, csID)
			}()
			action = "restart_claude"
		case "f10": // Stop
			go program.Send(types.ControlStopMsg{})
			action = "stop"
		default:
			program.Send(types.FKeyActionMsg{Key: key})
			action = key
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "action": action})
	})

	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher.Flush()

		// Send initial state
		status.Mu.Lock()
		initData, _ := json.Marshal(map[string]interface{}{
			"type":     "init",
			"streamId": status.StreamID,
			"phase":    status.Phase,
			"viewers":  status.Viewers,
			"uptime":   status.Uptime,
			"panes":    status.Panes,
		})
		status.Mu.Unlock()
		fmt.Fprintf(w, "data: %s\n\n", initData)
		flusher.Flush()

		ch := status.Subscribe()
		defer status.Unsubscribe(ch)

		ctx := r.Context()
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fmt.Fprintf(w, ": keepalive\n\n")
				flusher.Flush()
			case event, ok := <-ch:
				if !ok {
					return
				}
				fmt.Fprintf(w, "data: %s\n\n", event)
				flusher.Flush()
			}
		}
	})

	mux.HandleFunc("/panes", func(w http.ResponseWriter, r *http.Request) {
		status.Mu.Lock()
		data, _ := json.Marshal(status.Panes)
		status.Mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	})

	mux.HandleFunc("/start-stream", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			PromptSharing    bool `json:"promptSharing"`
			ShareProjectInfo bool `json:"shareProjectInfo"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		program.Send(types.StartStreamRequestMsg{
			PromptSharing:    req.PromptSharing,
			ShareProjectInfo: req.ShareProjectInfo,
		})
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true}`)
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(listener)

	return listener, nil
}

// CleanupControlSocket closes the listener and removes the socket file.
func CleanupControlSocket(listener net.Listener) {
	if listener != nil {
		listener.Close()
	}
	os.Remove(ControlSocketPath())
}

// ControlHTTPRequest sends an HTTP request to the control socket.
func ControlHTTPRequest(sockPath, method, path string) (string, error) {
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sockPath)
			},
		},
		Timeout: 5 * time.Second,
	}

	req, err := http.NewRequest(method, "http://localhost"+path, nil)
	if err != nil {
		return "", err
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("control server unavailable: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// ControlHTTPRequestWithBody sends an HTTP request with a body to the control socket.
func ControlHTTPRequestWithBody(sockPath, method, path string, body []byte) (string, error) {
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sockPath)
			},
		},
		Timeout: 5 * time.Second,
	}

	req, err := http.NewRequest(method, "http://localhost"+path, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("control server unavailable: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(respBody), nil
}
