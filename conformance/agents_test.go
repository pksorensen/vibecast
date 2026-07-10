//go:build conformance

// Package conformance is vibecast's multi-agent conformance suite: it launches the real
// vibecast binary under a mock agentics.dk server (see ./harness) and asserts that each
// coding agent integration satisfies the platform contract. Scenarios are gated behind
// the `conformance` build tag because they spawn tmux/ttyd and a real agent process.
//
// Run:  VIBECAST_CONFORMANCE_AGENTS=claude go test -tags conformance ./conformance -v
package conformance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pksorensen/vibecast/conformance/harness"
)

// vibecastBin is the freshly built vibecast binary shared by all scenarios.
var vibecastBin string

func TestMain(m *testing.M) {
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "getwd:", err)
		os.Exit(1)
	}
	moduleRoot := filepath.Dir(wd) // go test CWD is <root>/conformance
	tmp, err := os.MkdirTemp("", "vibecast-conf-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mkdtemp:", err)
		os.Exit(1)
	}
	bin := filepath.Join(tmp, "vibecast")
	if err := harness.BuildVibecast(moduleRoot, bin); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	vibecastBin = bin
	code := m.Run()
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}

// selectedAgents returns the agents to run, from VIBECAST_CONFORMANCE_AGENTS (comma-
// separated), defaulting to claude.
func selectedAgents() []string {
	v := os.Getenv("VIBECAST_CONFORMANCE_AGENTS")
	if strings.TrimSpace(v) == "" {
		return []string{"claude"}
	}
	var out []string
	for _, a := range strings.Split(v, ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

func TestConformance(t *testing.T) {
	for _, agent := range selectedAgents() {
		agent := agent
		t.Run(agent, func(t *testing.T) {
			t.Run("C01_launch_registers", func(t *testing.T) { scenarioC01(t, agent) })
			t.Run("C02_session_identity", func(t *testing.T) { scenarioC02(t, agent) })
		})
	}
}

// bringLive launches vibecast for the agent under a fresh mock server, starts streaming,
// and waits until it is live. It registers cleanup so that on failure the agent panes and
// everything the mock recorded are dumped BEFORE the private tmux server is torn down.
// Setup errors are fatal.
func bringLive(t *testing.T, agent string, cfg harness.LaunchConfig) (*harness.Session, *harness.MockServer) {
	t.Helper()
	mock, err := harness.NewMockServer()
	if err != nil {
		t.Fatalf("mock server: %v", err)
	}
	cfg.Agent = agent
	cfg.VibecastBin = vibecastBin
	cfg.ServerAddr = mock.Addr
	cfg.BaseDir = t.TempDir()
	sess, err := harness.Launch(cfg)
	if err != nil {
		mock.Close()
		t.Fatalf("launch: %v", err)
	}
	// Cleanup is LIFO: teardown (registered first) runs last; diagnostics (registered
	// second) runs first, while the tmux server is still alive to capture.
	t.Cleanup(func() { sess.Teardown(); mock.Close() })
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("DIAGNOSTICS\n%s\n%s", mock.Dump(), sess.Diagnostics())
		}
	})

	if err := sess.WaitControlSocket(30 * time.Second); err != nil {
		t.Fatalf("control socket: %v", err)
	}
	if err := sess.StartStream(); err != nil {
		t.Fatalf("start-stream: %v", err)
	}
	if _, err := sess.WaitPhase(30*time.Second, "starting", "live"); err != nil {
		t.Fatalf("wait phase: %v", err)
	}
	return sess, mock
}

// scenarioC01 (launch-registers): a Runner-style launch + /start-stream brings the agent
// live and registers with the platform — the session starts, the broadcaster connects,
// and the always-on capability + project-info metadata frames arrive. This is the
// baseline "the operator can host this agent at all" check.
func scenarioC01(t *testing.T, agent string) {
	sess, mock := bringLive(t, agent, harness.LaunchConfig{PromptShare: true, ShareInfo: true})

	waitFor(t, 15*time.Second, "session-event start for our sessionId", func() bool {
		for _, e := range mock.SessionEvents() {
			if e.Decoded["event"] == "start" && e.Decoded["sessionId"] == sess.SessionID {
				return true
			}
		}
		return false
	})

	waitFor(t, 20*time.Second, "broadcaster WS connect", func() bool {
		for _, c := range mock.BroadcastConns() {
			if c.SessionID == sess.SessionID {
				return true
			}
		}
		return false
	})

	waitFor(t, 20*time.Second, "capabilities metadata frame", func() bool {
		return len(mock.MetaFramesOfSubtype("capabilities")) > 0
	})

	waitFor(t, 20*time.Second, "stream_info metadata frame (shareProjectInfo=true)", func() bool {
		return len(mock.MetaFramesOfSubtype("stream_info")) > 0
	})
}

// scenarioC02 (session-identity): once the agent is live it must report a session identity
// to the platform. Assert a `session_start` metadata event with a non-empty claudeSessionId,
// and that the reported id agrees with what vibecast recorded in its session file (the
// preassign consistency check for claude/pi; discover agents just need any stable id).
func scenarioC02(t *testing.T, agent string) {
	sess, mock := bringLive(t, agent, harness.LaunchConfig{PromptShare: true, ShareInfo: true})

	var reportedID string
	waitFor(t, 90*time.Second, "session_start metadata with non-empty claudeSessionId", func() bool {
		for _, e := range mock.MetadataPostsOfSubtype("session_start") {
			if e.Decoded["sessionId"] != sess.SessionID {
				continue
			}
			if id, ok := e.Decoded["claudeSessionId"].(string); ok && id != "" {
				reportedID = id
				return true
			}
		}
		return false
	})
	t.Logf("reported session identity: claudeSessionId=%s (streamId=%s)", reportedID, sess.SessionID)

	// Consistency: the id vibecast published must match one it recorded in its session
	// file. Poll briefly — the file write and the hook POST can race.
	waitFor(t, 10*time.Second, "session identity recorded in session file", func() bool {
		for _, id := range sess.PaneClaudeSessionIDs() {
			if id == reportedID {
				return true
			}
		}
		return false
	})
}

// waitFor polls pred until it is true or the timeout elapses, failing the test with the
// given description on timeout.
func waitFor(t *testing.T, timeout time.Duration, what string, pred func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for: %s", timeout, what)
}
