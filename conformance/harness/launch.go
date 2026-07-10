package harness

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// LaunchConfig parameterizes a single vibecast launch under the conformance harness.
type LaunchConfig struct {
	Agent       string            // "claude" | "codex" | "pi"
	VibecastBin string            // absolute path to the built vibecast binary
	ServerAddr  string            // mock server host:port (must be 127.0.0.1:PORT)
	BaseDir     string            // caller-owned temp base (e.g. t.TempDir())
	ExtraEnv    map[string]string // extra/override env for vibecast
	JobMode     bool              // sets AGENTICS_JOB_MODE=1
	JobID       string            // AGENTICS_JOB_ID when JobMode (default "test-<sessionId>")
	PromptShare bool              // /start-stream promptSharing
	ShareInfo   bool              // /start-stream shareProjectInfo

	// NoConfigSeed skips seeding an isolated, pre-trusted agent config home. Off by
	// default so non-job scenarios don't stall at the agent's first-run trust dialog;
	// set it for scenarios that deliberately exercise logged-out/untrusted behavior
	// (e.g. C12 auth-gate).
	NoConfigSeed bool
}

// Session is a launched vibecast instance plus the control-socket client to drive it.
type Session struct {
	cfg          LaunchConfig
	TmuxSock     string
	VibecastHome string
	Workspace    string
	SessionID    string
	JobID        string // AGENTICS_JOB_ID, non-empty only in job mode
	ControlSock  string
	StderrLog    string // file capturing vibecast's stderr (survives the pane's death)

	seedEnv map[string]string // agent config-home env (CLAUDE_CONFIG_DIR, ...); reused on resume relaunch
	ctrl    *http.Client
}

// Stderr returns the full captured vibecast stderr log (empty string if unreadable).
func (s *Session) Stderr() string {
	if s.StderrLog == "" {
		return ""
	}
	b, err := os.ReadFile(s.StderrLog)
	if err != nil {
		return ""
	}
	return string(b)
}

// Launch starts vibecast inside a detached tmux session on a private -S socket, mimicking
// the ALP Runner. Because $TMUX is inherited (set by tmux for the pane), vibecast takes
// the switch-client + has-session polling path (cmd/root.go) and stays alive headless —
// no PTY and no attached client required. The control server comes up on a unix socket at
// $VIBECAST_HOME/.vibecast/control.sock.
func Launch(cfg LaunchConfig) (*Session, error) {
	sessionID, err := NewUUIDv4()
	if err != nil {
		return nil, err
	}
	vibecastHome := filepath.Join(cfg.BaseDir, "home")
	workspace := filepath.Join(cfg.BaseDir, "workspace")
	tmuxSock := filepath.Join(cfg.BaseDir, "tmux.sock")
	for _, d := range []string{vibecastHome, workspace} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	// A non-empty workspace so agents that inspect the tree see something real.
	_ = os.WriteFile(filepath.Join(workspace, "README.md"), []byte("# conformance workspace\n"), 0o644)

	env := map[string]string{
		"AGENTICS_SERVER":             cfg.ServerAddr,
		"AGENTIC_SERVER":              cfg.ServerAddr, // deprecated fallback, set for safety
		"VIBECAST_HOME":               vibecastHome,
		"VIBECAST_AGENT":              cfg.Agent,
		"SESSION_ID":                  sessionID,
		"CLAUDE_AUTO_UPDATE_DISABLED": "1",
		"PI_SKIP_VERSION_CHECK":       "1",
		"VIBECAST_DEBUG":              "1",
	}
	// AGENTICS_JOB_ID is what StopStream stamps onto the session-event `end` (it keys off the
	// id alone, independent of AGENTICS_JOB_MODE). JobMode additionally drives the interactive
	// onboarding auto-answer flow, which needs a cooperating server and is out of the minimal
	// mock's scope — so the two are set independently here.
	jobID := cfg.JobID
	if cfg.JobMode {
		env["AGENTICS_JOB_MODE"] = "1"
		if jobID == "" {
			jobID = "test-" + sessionID
		}
	}
	if jobID != "" {
		env["AGENTICS_JOB_ID"] = jobID
	}
	for k, v := range cfg.ExtraEnv {
		env[k] = v
	}

	// Agent config-home env (e.g. CLAUDE_CONFIG_DIR) must reach the AGENT process, which
	// vibecast spawns with `tmux new-window` — that pane inherits the tmux SERVER's global
	// environment, not vibecast's own. So this env belongs on the tmux server (the process
	// that starts it), exactly as the ALP Runner exports the credentials-volume config dir
	// before launching the detached tmux session. Putting it in the `env KEY=VAL` pane
	// wrapper below would only reach vibecast, and the agent window would inherit the real
	// user config instead. Non-job scenarios need this because they don't run vibecast's
	// job-mode onboarding auto-answers, so a fresh workspace would block at the trust dialog.
	var seedEnv map[string]string
	if !cfg.NoConfigSeed {
		seedEnv, err = prepareAgentConfig(cfg.Agent, cfg.BaseDir, workspace, cfg.VibecastBin)
		if err != nil {
			return nil, fmt.Errorf("prepare %s config home: %w", cfg.Agent, err)
		}
	}

	// tmux command: create a detached session whose only pane runs vibecast, with the
	// vibecast-specific env applied via `env KEY=VAL ... <bin>` (overlaying the tmux
	// server's inherited env, which already carries PATH/HOME and, per-pane, $TMUX).
	args := []string{
		"-S", tmuxSock, "new-session", "-d", "-s", "runner",
		"-x", "220", "-y", "50", "-c", workspace, "env",
	}
	// Deterministic env order keeps failures reproducible.
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, k+"="+env[k])
	}
	// Redirect vibecast's stderr to a file so its final shutdown logs survive the pane's
	// death (a 1s pane-capture poll misses the last ~1s before the runner session is torn
	// down). `exec` keeps the same PID; the `env` prefix already populated the environment.
	stderrLog := filepath.Join(cfg.BaseDir, "vibecast.stderr")
	args = append(args, "sh", "-c", `exec "$0" 2>> "$1"`, cfg.VibecastBin, stderrLog)

	cmd := exec.Command("tmux", args...)
	// The tmux server inherits the environment of the process that starts it; agent panes
	// (spawned later with `tmux new-window`) inherit that server env. Overlay the agent
	// config-home seed here so it reaches the agent, mirroring the Runner.
	cmd.Env = envWithOverrides(os.Environ(), seedEnv)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("tmux new-session failed: %w: %s", err, stderr.String())
	}

	s := &Session{
		cfg:          cfg,
		TmuxSock:     tmuxSock,
		VibecastHome: vibecastHome,
		Workspace:    workspace,
		SessionID:    sessionID,
		JobID:        jobID,
		ControlSock:  filepath.Join(vibecastHome, ".vibecast", "control.sock"),
		StderrLog:    stderrLog,
		seedEnv:      seedEnv,
	}
	s.ctrl = &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", s.ControlSock)
			},
		},
	}
	return s, nil
}

// WaitControlSocket blocks until the control socket exists (vibecast has booted) or the
// timeout elapses.
func (s *Session) WaitControlSocket(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(s.ControlSock); err == nil {
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("control socket %s did not appear within %s (vibecast log: %s)",
		s.ControlSock, timeout, s.tailPaneCapture())
}

// StartStream POSTs /start-stream to begin broadcasting.
func (s *Session) StartStream() error {
	body, _ := json.Marshal(map[string]any{
		"promptSharing":    s.cfg.PromptShare,
		"shareProjectInfo": s.cfg.ShareInfo,
	})
	status, respBody, err := s.control("POST", "/start-stream", body)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("start-stream: status %d: %s", status, string(respBody))
	}
	return nil
}

// Status GETs /status and returns the decoded body.
func (s *Session) Status() (map[string]any, error) {
	status, respBody, err := s.control("GET", "/status", nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("status: %d: %s", status, string(respBody))
	}
	var m map[string]any
	if err := json.Unmarshal(respBody, &m); err != nil {
		return nil, fmt.Errorf("status decode: %w (body=%s)", err, string(respBody))
	}
	return m, nil
}

// WaitPhase polls /status until the reported phase is one of the wanted values.
func (s *Session) WaitPhase(timeout time.Duration, wanted ...string) (string, error) {
	want := map[string]bool{}
	for _, w := range wanted {
		want[w] = true
	}
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		st, err := s.Status()
		if err == nil {
			if ph, ok := st["phase"].(string); ok {
				last = ph
				if want[ph] {
					return ph, nil
				}
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return last, fmt.Errorf("phase never reached %v (last=%q); pane:\n%s", wanted, last, s.tailPaneCapture())
}

// StopBroadcast POSTs /stop-broadcast to end the session cleanly, carrying the optional
// completion message + conclusion as a JSON body (the control handler accepts either query
// params or a body; a body avoids URL-encoding the free-text message). This is the operator
// path — after the ~10s flush grace vibecast POSTs a session-event `end` with these fields.
func (s *Session) StopBroadcast(message, conclusion string) error {
	body, err := json.Marshal(map[string]string{"message": message, "conclusion": conclusion})
	if err != nil {
		return err
	}
	code, resp, err := s.control("POST", "/stop-broadcast", body)
	if err != nil {
		return err
	}
	if code < 200 || code >= 300 {
		return fmt.Errorf("stop-broadcast: HTTP %d: %s", code, string(resp))
	}
	return nil
}

// Teardown kills the private tmux server (which terminates vibecast and its panes).
func (s *Session) Teardown() {
	_ = exec.Command("tmux", "-S", s.TmuxSock, "kill-server").Run()
}

// RelaunchForResume simulates the ALP Runner resuming a recycled job. It starts a FRESH
// vibecast process (new private tmux server + new stderr log) that reuses this session's
// VIBECAST_HOME, workspace, agent config seed, and stream id (SESSION_ID), and exports
// VIBECAST_RESUME_SESSION_ID=claudeResumeID so the agent is relaunched against its prior
// session (claude → `claude --resume <id>`). This mirrors the Runner handing the harvested
// agent session id to a fresh container on the same credentials volume. The caller must
// Teardown() the original process first; the returned *Session drives the resumed run and
// must itself be torn down.
func (s *Session) RelaunchForResume(claudeResumeID string) (*Session, error) {
	tmuxSock := filepath.Join(s.cfg.BaseDir, "tmux-resume.sock")
	stderrLog := filepath.Join(s.cfg.BaseDir, "vibecast-resume.stderr")
	controlSock := filepath.Join(s.VibecastHome, ".vibecast", "control.sock")
	// The prior process died via tmux kill-server (SIGHUP → no cleanup ran), leaving its
	// control socket file behind. Remove it so WaitControlSocket blocks for the NEW listener
	// rather than returning immediately on a stale path with no one listening. (StartControlServer
	// also os.Remove()s it before binding, so the relaunch rebinds cleanly regardless.)
	_ = os.Remove(controlSock)

	env := map[string]string{
		"AGENTICS_SERVER":             s.cfg.ServerAddr,
		"AGENTIC_SERVER":              s.cfg.ServerAddr,
		"VIBECAST_HOME":               s.VibecastHome,
		"VIBECAST_AGENT":              s.cfg.Agent,
		"SESSION_ID":                  s.SessionID, // resume the SAME stream (root.go reads SESSION_ID)
		"VIBECAST_RESUME_SESSION_ID":  claudeResumeID,
		"CLAUDE_AUTO_UPDATE_DISABLED": "1",
		"PI_SKIP_VERSION_CHECK":       "1",
		"VIBECAST_DEBUG":              "1",
	}
	if s.cfg.JobMode {
		env["AGENTICS_JOB_MODE"] = "1"
	}
	if s.JobID != "" {
		env["AGENTICS_JOB_ID"] = s.JobID
	}
	for k, v := range s.cfg.ExtraEnv {
		env[k] = v
	}

	args := []string{
		"-S", tmuxSock, "new-session", "-d", "-s", "runner",
		"-x", "220", "-y", "50", "-c", s.Workspace, "env",
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, k+"="+env[k])
	}
	args = append(args, "sh", "-c", `exec "$0" 2>> "$1"`, s.cfg.VibecastBin, stderrLog)

	cmd := exec.Command("tmux", args...)
	// Reuse the phase-1 agent config seed (CLAUDE_CONFIG_DIR) so the resumed agent stays
	// pre-trusted AND its prior-session transcript is present for `--resume` to load.
	cmd.Env = envWithOverrides(os.Environ(), s.seedEnv)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("tmux new-session (resume) failed: %w: %s", err, stderr.String())
	}

	rs := &Session{
		cfg:          s.cfg,
		TmuxSock:     tmuxSock,
		VibecastHome: s.VibecastHome,
		Workspace:    s.Workspace,
		SessionID:    s.SessionID,
		JobID:        s.JobID,
		ControlSock:  controlSock,
		StderrLog:    stderrLog,
		seedEnv:      s.seedEnv,
	}
	rs.ctrl = &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", rs.ControlSock)
			},
		},
	}
	return rs, nil
}

// AgentPaneStartCommand returns the command tmux used to launch the agent's REPL pane
// (`vibecast-<id>:main.0`) — i.e. the `sh -c '<cd && claude ...>'` string. It reflects the
// launch invocation regardless of whether the agent process then succeeded, so it is the
// robust way to assert a resume flag (claude `--resume <id>`) reached the relaunch.
func (s *Session) AgentPaneStartCommand() string {
	out, err := exec.Command("tmux", "-S", s.TmuxSock, "display-message", "-p", "-t",
		s.AgentPaneTarget()+".0", "#{pane_start_command}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// CapturePane returns the rendered contents of a pane in one of vibecast's tmux sessions,
// for debugging. session is e.g. "vibecast-lobby" or "vibecast-<id>".
func (s *Session) CapturePane(session string) string {
	out, err := exec.Command("tmux", "-S", s.TmuxSock, "capture-pane", "-p", "-t", session).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// AgentPaneTarget is the tmux target of the agent's REPL window (see stream.SpawnPane,
// which spawns the main pane as `vibecast-<id>:main`). It is where a chat-channel prompt
// is ultimately typed into the TUI.
func (s *Session) AgentPaneTarget() string {
	return "vibecast-" + s.SessionID + ":main"
}

// SendKeys types text into a tmux target and submits it with Enter, mimicking the
// prompt-delivery path into the agent REPL. The literal (`-l`) send avoids tmux
// interpreting the message as key names; the short settle lets the TUI register the
// text before the Enter so it isn't swallowed by paste-batching.
func (s *Session) SendKeys(target, text string) error {
	if err := exec.Command("tmux", "-S", s.TmuxSock, "send-keys", "-t", target, "-l", text).Run(); err != nil {
		return fmt.Errorf("send-keys text to %s: %w", target, err)
	}
	time.Sleep(250 * time.Millisecond)
	if err := exec.Command("tmux", "-S", s.TmuxSock, "send-keys", "-t", target, "Enter").Run(); err != nil {
		return fmt.Errorf("send-keys Enter to %s: %w", target, err)
	}
	return nil
}

// tailPaneCapture grabs whatever is on the lobby or streaming pane for error messages.
func (s *Session) tailPaneCapture() string {
	if out := s.CapturePane("vibecast-" + s.SessionID); out != "" {
		return out
	}
	return s.CapturePane("vibecast-lobby")
}

// Diagnostics returns a human-readable dump of the private tmux server's panes plus the
// session file, for embedding in a failure message.
func (s *Session) Diagnostics() string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "── vibecast session file (%s) ──\n", s.SessionID)
	if raw, err := os.ReadFile(filepath.Join(s.VibecastHome, ".vibecast", "sessions", s.SessionID+".json")); err == nil {
		b.Write(raw)
		b.WriteByte('\n')
	} else {
		fmt.Fprintf(&b, "(no session file: %v)\n", err)
	}
	fmt.Fprintf(&b, "── tmux panes on %s ──\n", s.TmuxSock)
	list, err := exec.Command("tmux", "-S", s.TmuxSock, "list-panes", "-a",
		"-F", "#{session_name}:#{window_index}.#{pane_index} [#{pane_current_command}] dead=#{pane_dead}").Output()
	if err != nil {
		fmt.Fprintf(&b, "(list-panes failed: %v)\n", err)
		return b.String()
	}
	for _, line := range splitNonEmpty(string(list)) {
		target := line
		if i := indexByte(line, ' '); i > 0 {
			target = line[:i]
		}
		fmt.Fprintf(&b, "\n### %s\n", line)
		out, _ := exec.Command("tmux", "-S", s.TmuxSock, "capture-pane", "-p", "-t", target).Output()
		b.Write(out)
	}
	return b.String()
}

func splitNonEmpty(s string) []string {
	var out []string
	for _, l := range bytes.Split([]byte(s), []byte("\n")) {
		if len(bytes.TrimSpace(l)) > 0 {
			out = append(out, string(l))
		}
	}
	return out
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// SessionFilePane mirrors the pane record in a vibecast session file. Keys match
// internal/types.SessionFilePaneEntry (camelCase on disk).
type SessionFilePane struct {
	PaneID          string `json:"paneId"`
	ClaudeSessionID string `json:"claudeSessionId"`
}

// SessionFile mirrors the subset of $VIBECAST_HOME/.vibecast/sessions/<streamId>.json the
// conformance suite asserts on. Keys match internal/types.SessionFile (camelCase on disk).
type SessionFile struct {
	SessionID       string            `json:"sessionId"`
	BroadcastID     string            `json:"broadcastId"`
	ServerHost      string            `json:"serverHost"`
	Workspace       string            `json:"workspace"`
	ClaudeSessionID string            `json:"claudeSessionId"`
	Panes           []SessionFilePane `json:"panes"`
}

// ReadSessionFile reads and decodes vibecast's session file for this stream.
func (s *Session) ReadSessionFile() (*SessionFile, error) {
	path := filepath.Join(s.VibecastHome, ".vibecast", "sessions", s.SessionID+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var sf SessionFile
	if err := json.Unmarshal(b, &sf); err != nil {
		return nil, fmt.Errorf("decode session file %s: %w", path, err)
	}
	return &sf, nil
}

// PaneClaudeSessionIDs returns every non-empty pane ClaudeSessionID recorded in the
// session file (plus the top-level one for older layouts).
func (s *Session) PaneClaudeSessionIDs() []string {
	sf, err := s.ReadSessionFile()
	if err != nil {
		return nil
	}
	var out []string
	if sf.ClaudeSessionID != "" {
		out = append(out, sf.ClaudeSessionID)
	}
	for _, p := range sf.Panes {
		if p.ClaudeSessionID != "" {
			out = append(out, p.ClaudeSessionID)
		}
	}
	return out
}

func (s *Session) control(method, path string, body []byte) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, "http://control"+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.ctrl.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, nil
}

// NewUUIDv4 returns a random RFC 4122 v4 UUID (vibecast validates --session-id / SESSION_ID
// with IsUUIDv4).
func NewUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// BuildVibecast compiles the vibecast binary from moduleRoot into outPath and places the
// claude-plugin directory next to it (via symlink) so telemetry.PluginDir() resolves and
// Claude's hooks load. Callers build once (e.g. in TestMain) so every scenario runs the
// current source — matching the production invariant that the running binary is always
// the latest.
func BuildVibecast(moduleRoot, outPath string) error {
	cmd := exec.Command("go", "build", "-o", outPath, "./main.go")
	cmd.Dir = moduleRoot
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go build vibecast: %w\n%s", err, out.String())
	}

	// vibecast resolves the Claude plugin as <dir-of-binary>/claude-plugin (PluginDir).
	// The bare binary in a temp dir has no plugin beside it, so replicate the shipped
	// layout by symlinking the repo's plugin next to the binary.
	src := filepath.Join(moduleRoot, "claude-plugin")
	if info, err := os.Stat(src); err != nil || !info.IsDir() {
		return fmt.Errorf("claude-plugin not found at %s (needed so Claude hooks load): %w", src, err)
	}
	dst := filepath.Join(filepath.Dir(outPath), "claude-plugin")
	_ = os.Remove(dst)
	if err := os.Symlink(src, dst); err != nil {
		return fmt.Errorf("symlink claude-plugin: %w", err)
	}
	return nil
}
