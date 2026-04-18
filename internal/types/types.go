package types

import (
	"encoding/json"
	"net"
	"sync"
	"time"
)

// PaneStatus is a simplified pane info for fkeybar display.
type PaneStatus struct {
	PaneId string `json:"paneId"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

// Phase represents the current UI phase.
type Phase int

const (
	PhaseSplash Phase = iota
	PhaseMenu
	PhaseStarting
	PhaseLive
	PhaseStopping
	PhaseSettings
	PhaseSessions
	PhaseWaiting // orchestrator waiting for fkeybar to trigger actions
)

// ChatMsg represents a chat message.
type ChatMsg struct {
	Type      string `json:"type"`
	Username  string `json:"username,omitempty"`
	Text      string `json:"text,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	Timestamp int64  `json:"timestamp,omitempty"`
	Count     int    `json:"count,omitempty"`
}

// PendingImage represents an image awaiting approval.
type PendingImage struct {
	ImageID   string `json:"imageId"`
	ImageData string `json:"imageData"`
	Caption   string `json:"caption"`
	Timestamp int64  `json:"timestamp"`
}

// ClaudeSessionInfo holds info about a discovered Claude session for the session picker.
type ClaudeSessionInfo struct {
	SessionID    string
	LastUpdated  time.Time
	FirstPrompt  string
	MessageCount int
}

// PaneInfo holds info about a single broadcast pane.
type PaneInfo struct {
	PaneId          string
	Name            string
	TmuxWindow      string
	TtydPort        int
	TtydPID         int
	ClaudeSessionID string
	MetaCh          chan []byte
	Active          bool
	Done            chan struct{}
}

// SharedStatus holds shared mutable state for the control server and TUI.
type SharedStatus struct {
	Mu              sync.Mutex
	SessionID       string
	BroadcastID     string
	URL             string
	PinCode         string
	Viewers         int
	Uptime          string
	Phase           string
	ClaudeSessionID string
	PendingImages   int
	TmuxSession     string
	ServerHost      string
	ServerConn      net.Conn
	OtelShutdown    func()
	// Pane tracking for fkeybar
	Panes         []PaneStatus
	ActivePaneIdx int
	MenuIndex   int
	// Fkeybar pane IDs (tmux pane identifiers for resize)
	FKeyBarPaneIDs []string
	// SSE subscriber management
	EventSubscribers []chan string
	EventSubMu       sync.Mutex
}

// Subscribe creates a new SSE event channel for a client.
func (s *SharedStatus) Subscribe() chan string {
	ch := make(chan string, 32)
	s.EventSubMu.Lock()
	s.EventSubscribers = append(s.EventSubscribers, ch)
	s.EventSubMu.Unlock()
	return ch
}

// Unsubscribe removes and closes an SSE event channel.
func (s *SharedStatus) Unsubscribe(ch chan string) {
	s.EventSubMu.Lock()
	for i, c := range s.EventSubscribers {
		if c == ch {
			s.EventSubscribers = append(s.EventSubscribers[:i], s.EventSubscribers[i+1:]...)
			break
		}
	}
	s.EventSubMu.Unlock()
	close(ch)
}

// BroadcastEvent sends an event string to all SSE subscribers.
func (s *SharedStatus) BroadcastEvent(event string) {
	s.EventSubMu.Lock()
	for _, ch := range s.EventSubscribers {
		select {
		case ch <- event:
		default: // drop if full
		}
	}
	s.EventSubMu.Unlock()
}

// SessionFile represents the on-disk session state file.
type SessionFile struct {
	SessionID       string                 `json:"sessionId"`
	BroadcastID     string                 `json:"broadcastId,omitempty"`
	ServerHost      string                 `json:"serverHost"`
	Workspace       string                 `json:"workspace"`
	Owner           string                 `json:"owner,omitempty"`
	Project         string                 `json:"project"`
	StartedAt       int64                  `json:"startedAt"`
	PID             int                    `json:"pid"`
	ClaudeSessionID string                 `json:"claudeSessionId,omitempty"`
	Panes           []SessionFilePaneEntry `json:"panes,omitempty"`
}

// SessionFilePaneEntry holds per-pane data in the session file.
type SessionFilePaneEntry struct {
	PaneID          string `json:"paneId"`
	ClaudeSessionID string `json:"claudeSessionId"`
}

// ── Bubble Tea Messages ─────────────────────────────────────────────────────

type TransTickMsg struct{}
type SplashTickMsg struct{}

type StreamStartedMsg struct {
	SessionID       string
	BroadcastID     string
	URL             string
	PID             int
	TtydPort        int
	MetaCh          chan []byte
	ClaudeSessionID string
	PinCode         string
	MainPane        *PaneInfo
}

type StreamErrorMsg struct{ Err error }
type StreamStoppedMsg struct{}
type UptimeTickMsg struct{}
type TmuxDetachedMsg struct{ Err error }
type ChatMsgReceived struct{ Msg ChatMsg }
type ClaudeRestartedMsg struct{ Err error }
type ControlRestartMsg struct{}
type ControlStopMsg struct {
	Message      string
	Conclusion   string
	GitCommit    string
	GitBranch    string
	GitPushError string
}
type ControlServerChangedMsg struct{ URL string }
type ImageQueuedMsg struct{ Img PendingImage }
type PaneSpawnedMsg struct{ Pane PaneInfo }
type PaneClosedMsg struct {
	PaneId string
	Err    error
}
type FKeyActionMsg struct{ Key string }
type StartStreamRequestMsg struct {
	PromptSharing    bool
	ShareProjectInfo bool
}

// ── Auth Types ──────────────────────────────────────────────────────────────

type AuthUserInfo struct {
	Sub              string `json:"sub"`
	PreferredUsername string `json:"preferred_username"`
	Email            string `json:"email,omitempty"`
	Picture          string `json:"picture,omitempty"`
}

type AuthData struct {
	AccessToken  string       `json:"access_token"`
	RefreshToken string       `json:"refresh_token"`
	ExpiresAt    int64        `json:"expires_at"`
	KeycloakURL  string       `json:"keycloak_url"`
	Realm        string       `json:"realm"`
	User         AuthUserInfo `json:"user"`
}

type AuthConfigResponse struct {
	KeycloakURL  string `json:"keycloakUrl"`
	Realm        string `json:"realm"`
	ClientID     string `json:"clientId"`
	AuthRequired bool   `json:"authRequired"`
}

// ── MCP Types ───────────────────────────────────────────────────────────────

type JsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type JsonrpcResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *RPCError   `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
