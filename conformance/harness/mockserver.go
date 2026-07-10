// Package harness provides the conformance test harness for vibecast's multi-agent
// support: a mock agentics.dk server plus a launch ceremony that mimics the ALP Runner.
//
// This package is deliberately buildable without the `conformance` build tag so it can
// be vetted with a plain `go build ./conformance/harness`. Only the scenario test entry
// (agents_test.go) carries `//go:build conformance`.
package harness

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	ws "github.com/pksorensen/vibecast/internal/websocket"
)

// wsAcceptGUID is the RFC 6455 magic value for computing Sec-WebSocket-Accept.
const wsAcceptGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// MetaFrame is one text frame observed on a broadcaster WebSocket connection. These
// carry either terminal-dimension updates ({"columns","rows"}) or metadata envelopes
// ({"type":"metadata","subtype":...}). vibecast sends both as WS text frames on the
// broadcaster connection (broadcast.go: ws.SendText(serverConn, msg)).
type MetaFrame struct {
	Raw     json.RawMessage
	Decoded map[string]any
	At      time.Time
}

// SessionEvent is one decoded POST /api/lives/session-event body.
type SessionEvent struct {
	Raw     json.RawMessage
	Decoded map[string]any
	At      time.Time
}

// BroadcastConn records one broadcaster WebSocket upgrade, keyed by query params.
type BroadcastConn struct {
	SessionID   string
	BroadcastID string
	PaneID      string
	At          time.Time
}

// MockServer is a stand-in for the agentics.dk edge (ws-relay + Next.js) that records
// everything vibecast sends so scenarios can assert on it. It binds 127.0.0.1 so
// vibecast's IsLocalHost() check selects the http/ws (not https/wss) scheme.
type MockServer struct {
	Addr string // host:port, e.g. 127.0.0.1:41xxx

	listener net.Listener
	srv      *http.Server

	mu             sync.Mutex
	sessionEvents  []SessionEvent
	metaFrames     []MetaFrame
	broadcastConns []BroadcastConn
	metadataPosts  []SessionEvent // POST /api/lives/metadata bodies (URL detection etc.)
	binaryFrames   int            // count of terminal-data binary frames drained

	// resumeState, when set, is merged into the session-event response for resume
	// (event=="start" with an existing session). Left nil for the base scenarios.
	resumeState map[string]any
}

// NewMockServer starts a mock server on a loopback port and returns it running.
func NewMockServer() (*MockServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	m := &MockServer{Addr: ln.Addr().String(), listener: ln}
	mux := http.NewServeMux()
	m.routes(mux)
	m.srv = &http.Server{Handler: mux}
	go m.srv.Serve(ln)
	return m, nil
}

// Close shuts the server down.
func (m *MockServer) Close() {
	if m.srv != nil {
		_ = m.srv.Close()
	}
}

func (m *MockServer) routes(mux *http.ServeMux) {
	// Auth gate: report auth NOT required so vibecast streams without a token.
	mux.HandleFunc("/api/lives/auth-config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"authRequired": false})
	})

	// Session lifecycle: start/resume/end. Records the body and returns a pin (+ any
	// configured resume state so the ResumeStream path can rehydrate).
	mux.HandleFunc("/api/lives/session-event", func(w http.ResponseWriter, r *http.Request) {
		body := readJSONBody(r)
		m.mu.Lock()
		m.sessionEvents = append(m.sessionEvents, SessionEvent{Raw: rawOf(body), Decoded: body, At: now()})
		resume := m.resumeState
		m.mu.Unlock()
		resp := map[string]any{"ok": true, "pin": "MOCK", "env": map[string]any{}}
		if resume != nil {
			for k, v := range resume {
				resp[k] = v
			}
		}
		writeJSON(w, resp)
	})

	// Generic metadata POSTs (url_detected and friends). Recorded separately.
	mux.HandleFunc("/api/lives/metadata", func(w http.ResponseWriter, r *http.Request) {
		body := readJSONBody(r)
		m.mu.Lock()
		m.metadataPosts = append(m.metadataPosts, SessionEvent{Raw: rawOf(body), Decoded: body, At: now()})
		m.mu.Unlock()
		writeJSON(w, map[string]any{"ok": true})
	})

	// Broadcaster + chat WebSocket endpoints.
	mux.HandleFunc("/api/lives/broadcast/ws", m.handleBroadcastWS)
	mux.HandleFunc("/api/lives/chat/ws", m.handleChatWS)

	// Relay snapshot sink.
	mux.HandleFunc("/_relay/snapshot", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true})
	})

	// Question-vote / pending-answer polls — answer empty so nothing is pending.
	mux.HandleFunc("/api/lives/question-vote", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{})
	})

	// Catch-all: 200 {} so no vibecast path errors out unexpectedly. Any endpoint we
	// haven't special-cased (pending-answer, media upload, workspace-archive, ...) lands
	// here; scenarios that care about those add explicit handlers.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{})
	})
}

// handleBroadcastWS accepts the broadcaster WS handshake and drains frames, recording
// text frames (dims + metadata) and counting binary (terminal) frames.
func (m *MockServer) handleBroadcastWS(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	m.mu.Lock()
	m.broadcastConns = append(m.broadcastConns, BroadcastConn{
		SessionID:   q.Get("sessionId"),
		BroadcastID: q.Get("broadcastId"),
		PaneID:      q.Get("paneId"),
		At:          now(),
	})
	m.mu.Unlock()

	conn, br, err := acceptWS(w, r)
	if err != nil {
		return
	}
	defer conn.Close()
	for {
		opcode, payload, err := ws.ReadFrame(br)
		if err != nil {
			return
		}
		switch opcode {
		case 0x1: // text — dims or metadata
			var decoded map[string]any
			_ = json.Unmarshal(payload, &decoded)
			m.mu.Lock()
			m.metaFrames = append(m.metaFrames, MetaFrame{
				Raw:     append(json.RawMessage(nil), payload...),
				Decoded: decoded,
				At:      now(),
			})
			m.mu.Unlock()
		case 0x2: // binary — terminal bytes
			m.mu.Lock()
			m.binaryFrames++
			m.mu.Unlock()
		case 0x8: // close
			return
		case 0x9: // ping — reply pong
			_ = writeServerFrame(conn, 0xA, payload)
		}
	}
}

// handleChatWS accepts and drains the chat WS so vibecast's chat connect succeeds.
func (m *MockServer) handleChatWS(w http.ResponseWriter, r *http.Request) {
	conn, br, err := acceptWS(w, r)
	if err != nil {
		return
	}
	defer conn.Close()
	for {
		opcode, _, err := ws.ReadFrame(br)
		if err != nil || opcode == 0x8 {
			return
		}
	}
}

// --- accessors used by scenarios (all snapshot under the mutex) ---

// SessionEvents returns a copy of the recorded session-event bodies.
func (m *MockServer) SessionEvents() []SessionEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]SessionEvent(nil), m.sessionEvents...)
}

// MetaFrames returns a copy of the recorded broadcaster WS text frames.
func (m *MockServer) MetaFrames() []MetaFrame {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]MetaFrame(nil), m.metaFrames...)
}

// BroadcastConns returns a copy of the recorded broadcaster WS connections.
func (m *MockServer) BroadcastConns() []BroadcastConn {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]BroadcastConn(nil), m.broadcastConns...)
}

// MetadataPosts returns a copy of the recorded POST /api/lives/metadata bodies.
func (m *MockServer) MetadataPosts() []SessionEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]SessionEvent(nil), m.metadataPosts...)
}

// SetResumeState configures extra fields merged into the session-event response so the
// ResumeStream path can rehydrate (used by the resume scenario).
func (m *MockServer) SetResumeState(state map[string]any) {
	m.mu.Lock()
	m.resumeState = state
	m.mu.Unlock()
}

// MetaFramesOfSubtype returns recorded metadata frames whose subtype matches.
func (m *MockServer) MetaFramesOfSubtype(subtype string) []MetaFrame {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []MetaFrame
	for _, f := range m.metaFrames {
		if f.Decoded != nil && f.Decoded["subtype"] == subtype {
			out = append(out, f)
		}
	}
	return out
}

// MetadataPostsOfSubtype returns recorded POST /api/lives/metadata bodies (hook-derived
// events: session_start, prompt, tool_use, ...) whose subtype matches.
func (m *MockServer) MetadataPostsOfSubtype(subtype string) []SessionEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []SessionEvent
	for _, p := range m.metadataPosts {
		if p.Decoded != nil && p.Decoded["subtype"] == subtype {
			out = append(out, p)
		}
	}
	return out
}

// Dump returns a human-readable summary of everything the mock recorded, for failure
// diagnostics.
func (m *MockServer) Dump() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "── mock recorded: %d session-events, %d metadata POSTs, %d WS text frames, %d broadcast conns, %d binary frames ──\n",
		len(m.sessionEvents), len(m.metadataPosts), len(m.metaFrames), len(m.broadcastConns), m.binaryFrames)
	for _, e := range m.sessionEvents {
		fmt.Fprintf(&b, "  session-event: event=%v sessionId=%v\n", e.Decoded["event"], e.Decoded["sessionId"])
	}
	for _, p := range m.metadataPosts {
		fmt.Fprintf(&b, "  metadata POST: subtype=%v sessionId=%v\n", p.Decoded["subtype"], p.Decoded["sessionId"])
	}
	for _, f := range m.metaFrames {
		if f.Decoded["type"] == "metadata" {
			fmt.Fprintf(&b, "  WS meta frame: subtype=%v\n", f.Decoded["subtype"])
		}
	}
	for _, c := range m.broadcastConns {
		fmt.Fprintf(&b, "  broadcast conn: sessionId=%s paneId=%s\n", c.SessionID, c.PaneID)
	}
	return b.String()
}

// --- low-level WebSocket accept (hand-rolled; the vibecast client only checks for 101) ---

// acceptWS upgrades an HTTP request to a WebSocket connection, returning the hijacked
// conn and its buffered reader. Client→server frames are masked; ws.ReadFrame unmasks.
func acceptWS(w http.ResponseWriter, r *http.Request) (net.Conn, *bufio.Reader, error) {
	key := r.Header.Get("Sec-WebSocket-Key")
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "no hijack", http.StatusInternalServerError)
		return nil, nil, errNoHijack
	}
	conn, bufrw, err := hj.Hijack()
	if err != nil {
		return nil, nil, err
	}
	accept := computeAccept(key)
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := bufrw.WriteString(resp); err != nil {
		conn.Close()
		return nil, nil, err
	}
	if err := bufrw.Flush(); err != nil {
		conn.Close()
		return nil, nil, err
	}
	return conn, bufrw.Reader, nil
}

func computeAccept(key string) string {
	h := sha1.New()
	io.WriteString(h, key+wsAcceptGUID)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// writeServerFrame writes an unmasked server→client frame (used for pong replies).
func writeServerFrame(conn net.Conn, opcode byte, data []byte) error {
	frame := []byte{0x80 | opcode}
	n := len(data)
	switch {
	case n < 126:
		frame = append(frame, byte(n))
	case n < 65536:
		frame = append(frame, 126, byte(n>>8), byte(n&0xff))
	default:
		frame = append(frame, 127,
			byte(n>>56), byte(n>>48), byte(n>>40), byte(n>>32),
			byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
	frame = append(frame, data...)
	_, err := conn.Write(frame)
	return err
}

// --- small helpers ---

var errNoHijack = &hijackErr{}

type hijackErr struct{}

func (*hijackErr) Error() string { return "response writer does not support hijacking" }

func now() time.Time { return time.Now() }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func readJSONBody(r *http.Request) map[string]any {
	var m map[string]any
	b, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(b, &m)
	return m
}

func rawOf(m map[string]any) json.RawMessage {
	b, _ := json.Marshal(m)
	return b
}
