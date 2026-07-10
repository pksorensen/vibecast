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
	"os/exec"
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
			t.Run("C03_initial_prompt", func(t *testing.T) { scenarioC03(t, agent) })
			t.Run("C04_system_prompt_honored", func(t *testing.T) { scenarioC04(t, agent) })
			t.Run("C05_tool_events", func(t *testing.T) { scenarioC05(t, agent) })
			t.Run("C06_turn_complete", func(t *testing.T) { scenarioC06(t, agent) })
			t.Run("C07_completion_conclusion", func(t *testing.T) { scenarioC07(t, agent) })
			t.Run("C08_guard_denies", func(t *testing.T) { scenarioC08(t, agent) })
			t.Run("C09_resume_relaunch", func(t *testing.T) { scenarioC09(t, agent) })
			t.Run("C10_session_end_reported", func(t *testing.T) { scenarioC10(t, agent) })
			t.Run("C11_prompt_injection_tui", func(t *testing.T) { scenarioC11(t, agent) })
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

// scenarioC03 (initial-prompt-published): the initial job prompt vibecast is handed via
// VIBECAST_INITIAL_PROMPT_FILE must reach the agent and surface back to the platform. Seed
// a nonce prompt file, launch, and assert a `prompt` metadata event for this session whose
// text contains the nonce arrives within the deadline (ordering vs tool_use is advisory —
// hooks POST asynchronously and can race).
func scenarioC03(t *testing.T, agent string) {
	nonce := newNonce(t)
	promptFile := filepath.Join(t.TempDir(), "initial-prompt.txt")
	body := "Conformance check " + nonce + ". Reply with one short line acknowledging; do not run any tools."
	if err := os.WriteFile(promptFile, []byte(body), 0o644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}

	sess, mock := bringLive(t, agent, harness.LaunchConfig{
		PromptShare: true,
		ShareInfo:   true,
		ExtraEnv:    map[string]string{"VIBECAST_INITIAL_PROMPT_FILE": promptFile},
	})

	waitFor(t, 90*time.Second, "prompt metadata whose text contains the nonce", func() bool {
		for _, e := range mock.MetadataPostsOfSubtype("prompt") {
			if e.Decoded["sessionId"] != sess.SessionID {
				continue
			}
			if txt, ok := e.Decoded["prompt"].(string); ok && strings.Contains(txt, nonce) {
				return true
			}
		}
		return false
	})
	t.Logf("initial prompt surfaced as a prompt event carrying nonce %s", nonce)
}

// scenarioC04 (system-prompt-honored): the station's system prompt vibecast appends must
// actually reach the model and shape its behaviour. The nonce lives ONLY in the appended
// system prompt (as a secret "station code"); the initial user prompt merely *asks* for the
// code without ever stating it. So a reply containing the nonce can only come from the model
// having read and honoured the appended system prompt — an echo of the user prompt cannot
// produce it. Real-mode only: a canned/mock provider cannot "honor" an instruction, so this
// scenario is meaningless there and the pi mockmodel run skips it.
//
// vibecast injects the station prompt via VIBECAST_APPEND_SYSTEM_PROMPT_FILE, which the
// adapter translates to the agent's own mechanism (claude → --append-system-prompt). Keeping
// it a vibecast-level env keeps this scenario agent-agnostic.
//
// Real-mode only (spec: "R only"). Today the suite runs claude in real mode only, so there is
// nothing to skip. When pi's mockmodel run lands (a canned provider can't honor an instruction)
// it must skip this scenario — add that guard alongside the mode plumbing, not before it.
func scenarioC04(t *testing.T, agent string) {
	nonce := newNonce(t)

	sysPromptFile := filepath.Join(t.TempDir(), "system-prompt.txt")
	sysBody := "You are operating a station in an assembly line. The station code is " + nonce +
		". When the user asks for the station code, reply with exactly that code on a line by " +
		"itself and nothing else."
	if err := os.WriteFile(sysPromptFile, []byte(sysBody), 0o644); err != nil {
		t.Fatalf("write system-prompt file: %v", err)
	}

	promptFile := filepath.Join(t.TempDir(), "initial-prompt.txt")
	// Note: the user prompt never contains the nonce — it only asks for the station code.
	body := "What is the station code? Answer using your operating instructions. " +
		"Do not use any tools and do not say anything else."
	if err := os.WriteFile(promptFile, []byte(body), 0o644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}

	sess, mock := bringLive(t, agent, harness.LaunchConfig{
		PromptShare: true,
		ShareInfo:   true,
		ExtraEnv: map[string]string{
			"VIBECAST_INITIAL_PROMPT_FILE":       promptFile,
			"VIBECAST_APPEND_SYSTEM_PROMPT_FILE": sysPromptFile,
		},
	})

	waitFor(t, 120*time.Second, "assistant_response echoing the station code from the appended system prompt", func() bool {
		for _, e := range mock.MetadataPostsOfSubtype("assistant_response") {
			if e.Decoded["sessionId"] != sess.SessionID {
				continue
			}
			if txt, ok := e.Decoded["text"].(string); ok && strings.Contains(txt, nonce) {
				return true
			}
		}
		return false
	})
	t.Logf("station code from the appended system prompt was honored in the reply (nonce %s)", nonce)
}

// scenarioC05 (tool-events): the agent must run a write tool under vibecast and have both
// ends of that tool call surface over the metadata channel. Prompt it to write a nonce file,
// then assert a `tool_use` (write-class toolName) and a matching `tool_use_end` with the same
// toolUseId, and — the ground truth — the file exists on disk with the nonce content.
func scenarioC05(t *testing.T, agent string) {
	nonce := newNonce(t)
	fname := nonce + ".txt"
	promptFile := filepath.Join(t.TempDir(), "initial-prompt.txt")
	body := "Use the Write tool to create a file named exactly " + fname +
		" in the current working directory, containing exactly the text " + nonce +
		" and nothing else. Do not run any shell commands, and do nothing else afterward."
	if err := os.WriteFile(promptFile, []byte(body), 0o644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}

	sess, mock := bringLive(t, agent, harness.LaunchConfig{
		PromptShare: true,
		ShareInfo:   true,
		ExtraEnv:    map[string]string{"VIBECAST_INITIAL_PROMPT_FILE": promptFile},
	})

	var useID string
	waitFor(t, 120*time.Second, "write-class tool_use event", func() bool {
		for _, e := range mock.MetadataPostsOfSubtype("tool_use") {
			if e.Decoded["sessionId"] != sess.SessionID {
				continue
			}
			if name, _ := e.Decoded["toolName"].(string); isWriteToolName(name) {
				if id, ok := e.Decoded["toolUseId"].(string); ok && id != "" {
					useID = id
					return true
				}
			}
		}
		return false
	})

	waitFor(t, 60*time.Second, "matching tool_use_end for toolUseId "+useID, func() bool {
		for _, e := range mock.MetadataPostsOfSubtype("tool_use_end") {
			if e.Decoded["sessionId"] == sess.SessionID && e.Decoded["toolUseId"] == useID {
				return true
			}
		}
		return false
	})

	target := filepath.Join(sess.Workspace, fname)
	waitFor(t, 10*time.Second, "written file exists with nonce content", func() bool {
		b, err := os.ReadFile(target)
		return err == nil && strings.Contains(string(b), nonce)
	})
	t.Logf("write tool pair observed (toolUseId=%s) and %s written to workspace", useID, fname)
}

// scenarioC06 (turn-complete): after the agent replies, vibecast must observe the turn
// ending. Prompt for a no-tool reply carrying a nonce, then assert (a) the reply surfaces
// as `assistant_response` text containing the nonce and (b) a Stop-derived turn-end event
// arrives — distinguishable because the Stop hook attaches `transcriptLines` while streamed
// mid-turn assistant_response events do not. (For a no-tool turn both are the same event.)
func scenarioC06(t *testing.T, agent string) {
	nonce := newNonce(t)
	promptFile := filepath.Join(t.TempDir(), "initial-prompt.txt")
	body := "Reply with exactly this token on a line by itself: " + nonce +
		". Do not use any tools and do not say anything else."
	if err := os.WriteFile(promptFile, []byte(body), 0o644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}

	sess, mock := bringLive(t, agent, harness.LaunchConfig{
		PromptShare: true,
		ShareInfo:   true,
		ExtraEnv:    map[string]string{"VIBECAST_INITIAL_PROMPT_FILE": promptFile},
	})

	waitFor(t, 120*time.Second, "assistant_response text containing the nonce", func() bool {
		for _, e := range mock.MetadataPostsOfSubtype("assistant_response") {
			if e.Decoded["sessionId"] != sess.SessionID {
				continue
			}
			if txt, ok := e.Decoded["text"].(string); ok && strings.Contains(txt, nonce) {
				return true
			}
		}
		return false
	})

	waitFor(t, 30*time.Second, "Stop-derived turn-end assistant_response", func() bool {
		for _, e := range mock.MetadataPostsOfSubtype("assistant_response") {
			if e.Decoded["sessionId"] != sess.SessionID {
				continue
			}
			if _, ok := e.Decoded["transcriptLines"]; ok {
				return true
			}
		}
		return false
	})
	t.Logf("reply surfaced and turn completed for nonce %s", nonce)
}

// scenarioC07 (completion-conclusion): the ALP Runner contract. In job mode the agent itself
// must finish a job by calling vibecast's stop tool with a conclusion — this is how a station
// reports success/failure back to the assembly line. It exercises a DIFFERENT path than C10:
// C10 is the operator's out-of-band /stop-broadcast on the control socket; C07 is the agent
// invoking the `stop_broadcast` MCP tool (registered via the vibecast claude-plugin's .mcp.json
// → `vibecast mcp serve`), which resolves its own gates, then posts the same session-event
// `end`. The nonce rides in the completion message so the assertion is unambiguous.
//
// Job mode (AGENTICS_JOB_MODE=1 + AGENTICS_JOB_ID) is what makes the stop tool the contract:
// the job-mode Stop hook blocks the agent from exiting until stop_broadcast has been called, so
// even if the model forgot the explicit instruction the conclusion still gets reported. The
// harness workspace is not a git repo and AGENTICS_AUTO_GIT is unset, so the tool's auto-git /
// uncommitted-changes gates are inert and gitCommit/gitBranch degrade to empty.
func scenarioC07(t *testing.T, agent string) {
	nonce := newNonce(t)
	promptFile := filepath.Join(t.TempDir(), "initial-prompt.txt")
	body := "The job is complete. Call the stop_broadcast tool now with conclusion set to " +
		"\"success\" and message set to a one-line summary that includes the token " + nonce +
		". Do not use any other tools and do not do anything else."
	if err := os.WriteFile(promptFile, []byte(body), 0o644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}

	sess, mock := bringLive(t, agent, harness.LaunchConfig{
		JobMode:     true,
		JobID:       "test-job-" + nonce,
		PromptShare: true,
		ShareInfo:   true,
		ExtraEnv:    map[string]string{"VIBECAST_INITIAL_PROMPT_FILE": promptFile},
	})

	waitFor(t, 150*time.Second, "session-event end with conclusion=success carrying our message + jobId", func() bool {
		for _, e := range mock.SessionEvents() {
			if e.Decoded["event"] != "end" || e.Decoded["sessionId"] != sess.SessionID {
				continue
			}
			if e.Decoded["conclusion"] != "success" {
				continue
			}
			msgOK := false
			if m, ok := e.Decoded["message"].(string); ok && strings.Contains(m, nonce) {
				msgOK = true
			}
			if msgOK && e.Decoded["jobId"] == sess.JobID {
				return true
			}
		}
		return false
	})
	t.Logf("agent called stop_broadcast; station reported conclusion=success jobId=%s (nonce %s)",
		sess.JobID, nonce)
}

// scenarioC08 (guard-denies): a broad process-kill must be blocked by the PreToolUse
// guard AND recorded in the per-stream deny ledger. A denied PreToolUse produces no
// PostToolUse and only stdout+exit-code on the wire, so the ledger under
// $VIBECAST_HOME/.vibecast/guard-denials/<streamId>.jsonl is the only durable evidence
// the guard fired — that is exactly what the platform (and this suite) can observe.
//
// The scenario prompts a `pkill -f <sentinel>` (which in a shared-PID broadcast
// container would also kill the agent + broadcaster) and asserts the deny is recorded
// and the turn still completes. The sentinel makes the prompt realistic; the guard
// should keep it alive.
func scenarioC08(t *testing.T, agent string) {
	nonce := newNonce(t)
	token := "conformance-sentinel-" + nonce

	// Long-lived sentinel whose argv[0] embeds the match token so `pkill -f <token>`
	// has a real target. `exec -a` sets argv[0]; the guard should prevent its death.
	sentinel := exec.Command("bash", "-c", "exec -a "+token+" sleep 3600")
	if err := sentinel.Start(); err != nil {
		t.Fatalf("start sentinel: %v", err)
	}
	t.Cleanup(func() {
		if sentinel.Process != nil {
			_ = sentinel.Process.Kill()
			_, _ = sentinel.Process.Wait()
		}
	})

	promptFile := filepath.Join(t.TempDir(), "initial-prompt.txt")
	body := "Use the Bash tool to run this exact command: pkill -f " + token +
		". If the command fails or is blocked, do NOT try any other way to stop that " +
		"process — just report the error text you received, then stop."
	if err := os.WriteFile(promptFile, []byte(body), 0o644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}

	sess, mock := bringLive(t, agent, harness.LaunchConfig{
		PromptShare: true,
		ShareInfo:   true,
		ExtraEnv:    map[string]string{"VIBECAST_INITIAL_PROMPT_FILE": promptFile},
	})

	ledger := filepath.Join(sess.VibecastHome, ".vibecast", "guard-denials", sess.SessionID+".jsonl")

	waitFor(t, 120*time.Second, "guard deny record for the process-kill", func() bool {
		data, err := os.ReadFile(ledger)
		if err != nil {
			return false
		}
		return strings.Contains(string(data), token) && strings.Contains(string(data), "process-kill")
	})

	// The turn must still complete — a blocked tool call should surface as the agent
	// reporting the error, not wedge the session.
	waitFor(t, 60*time.Second, "turn completes after the deny", func() bool {
		for _, e := range mock.MetadataPostsOfSubtype("assistant_response") {
			if e.Decoded["sessionId"] == sess.SessionID {
				return true
			}
		}
		return false
	})
	t.Logf("guard blocked and recorded the process-kill; turn completed (nonce %s)", nonce)
}

// scenarioC09 (resume-relaunch): the ALP Runner's crash/timeout recovery contract. When a job
// is recycled the Runner relaunches vibecast fresh against the SAME stream id and exports
// VIBECAST_RESUME_SESSION_ID = the agent session id harvested from the prior run, so the agent
// continues its session instead of starting cold (cmd/root.go → StartStream → SpawnPane →
// claude --resume <id>). This asserts the full round-trip: run 1 mints an agent session id that
// vibecast records + publishes; the harness harvests it exactly as the platform would; a fresh
// relaunch (new tmux server, same VIBECAST_HOME/workspace/config seed, SESSION_ID pinned) reaches
// live and respawns the agent with a resume command naming that harvested id, and a fresh
// session_start proves the agent process actually came back up and re-registered.
//
// Per-adapter identity: the resume-command shape differs by agent (claude/pi resume by session
// id, codex by thread) — resumeCommandFragment encodes each. Only the Runner relaunch (agent
// respawn) is exercised headlessly; vibecast's tmux-alive reattach (stream.ResumeStream) is
// reachable only via an interactive splash keypress with no control-socket trigger, so it is out
// of CI scope (see docs/agents/conformance-suite.md roadmap).
func scenarioC09(t *testing.T, agent string) {
	// Phase 1: bring the agent live WITH one real turn so a resumable conversation transcript
	// exists on disk — `claude --resume <id>` errors out on an empty/absent session, and a
	// recycled job that had done nothing wouldn't be worth resuming anyway. The nonce reply is
	// just to force a turn; C09 does not assert conversation continuity (that needs asserting on
	// live model output — out of scope here).
	nonce := newNonce(t)
	promptFile := filepath.Join(t.TempDir(), "initial-prompt.txt")
	body := "Reply with exactly this token on a line by itself: " + nonce +
		". Do not use any tools and do not say anything else."
	if err := os.WriteFile(promptFile, []byte(body), 0o644); err != nil {
		t.Fatalf("write prompt file: %v", err)
	}

	sess, mock := bringLive(t, agent, harness.LaunchConfig{
		PromptShare: true,
		ShareInfo:   true,
		ExtraEnv:    map[string]string{"VIBECAST_INITIAL_PROMPT_FILE": promptFile},
	})

	// Harvest the session id vibecast reports + records — the same id the platform reads back to
	// hand a resuming Runner.
	var priorID string
	waitFor(t, 90*time.Second, "phase-1 session_start with a non-empty agent session id", func() bool {
		for _, e := range mock.MetadataPostsOfSubtype("session_start") {
			if e.Decoded["sessionId"] != sess.SessionID {
				continue
			}
			if id, ok := e.Decoded["claudeSessionId"].(string); ok && id != "" {
				priorID = id
				return true
			}
		}
		return false
	})
	waitFor(t, 10*time.Second, "phase-1 agent session id recorded in the session file", func() bool {
		for _, id := range sess.PaneClaudeSessionIDs() {
			if id == priorID {
				return true
			}
		}
		return false
	})

	// Wait for the phase-1 turn to complete so the transcript is durably written before the
	// recycle — otherwise there is nothing for --resume to load.
	waitFor(t, 120*time.Second, "phase-1 turn completes (transcript written) before recycle", func() bool {
		for _, e := range mock.MetadataPostsOfSubtype("assistant_response") {
			if e.Decoded["sessionId"] == sess.SessionID {
				return true
			}
		}
		return false
	})
	t.Logf("phase 1: agent session id %s (stream %s), turn complete", priorID, sess.SessionID)

	// Snapshot phase-1 session_start count for this stream so phase 2's relaunch is provable as a
	// genuinely new agent registration (count strictly increases) — agent-agnostic.
	phase1Starts := 0
	for _, e := range mock.MetadataPostsOfSubtype("session_start") {
		if e.Decoded["sessionId"] == sess.SessionID {
			phase1Starts++
		}
	}

	// Recycle: tear the whole run down (agent + tmux + vibecast), keeping VIBECAST_HOME +
	// workspace + agent config seed — what survives a Runner job recycle on the same volume.
	sess.Teardown()

	// Phase 2: the Runner relaunches vibecast against the same stream, resuming priorID.
	rs, err := sess.RelaunchForResume(priorID)
	if err != nil {
		t.Fatalf("relaunch for resume: %v", err)
	}
	t.Cleanup(func() { rs.Teardown() })
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("RESUME DIAGNOSTICS\n%s\nvibecast resume stderr:\n%s", rs.Diagnostics(), rs.Stderr())
		}
	})

	if err := rs.WaitControlSocket(30 * time.Second); err != nil {
		t.Fatalf("resume control socket: %v", err)
	}
	if err := rs.StartStream(); err != nil {
		t.Fatalf("resume start-stream: %v", err)
	}
	if _, err := rs.WaitPhase(30*time.Second, "live"); err != nil {
		t.Fatalf("resume wait phase live: %v", err)
	}

	// The agent must be relaunched against the harvested id — the launch command names it.
	frag := resumeCommandFragment(agent, priorID)
	waitFor(t, 20*time.Second, "agent pane relaunched with resume command containing "+frag, func() bool {
		return strings.Contains(rs.AgentPaneStartCommand(), frag)
	})

	// And the agent process must actually have come back up under resume: a fresh session_start
	// beyond phase 1's registrations for this stream.
	waitFor(t, 90*time.Second, "a fresh session_start after resume (agent relaunched + re-registered)", func() bool {
		n := 0
		for _, e := range mock.MetadataPostsOfSubtype("session_start") {
			if e.Decoded["sessionId"] == sess.SessionID {
				n++
			}
		}
		return n > phase1Starts
	})
	t.Logf("resume relaunched the agent with %q and it re-registered (stream %s)", frag, sess.SessionID)
}

// scenarioC10 (session-end-reported): the operator's clean stop must be reported to the
// platform. POST /stop-broadcast on the control socket with a completion message and
// conclusion; after vibecast's ~10s flush grace a session-event `end` must arrive carrying
// that message + conclusion + the job id. The grace is load-bearing (it lets trailing
// tool_use_end/assistant_response metadata land first), so the scenario also asserts the end
// did NOT arrive instantly. A pane-kill would NOT produce `end` — that is ws-relay's job in
// prod and out of the mock's scope — so this exercises the control-socket path specifically.
//
// A job id is set (that is what puts `jobId` on the end event) without AGENTICS_JOB_MODE:
// full job-mode boot drives an interactive onboarding auto-answer handshake that needs a
// cooperating server, which the minimal mock doesn't play — a separate scenario's concern.
func scenarioC10(t *testing.T, agent string) {
	nonce := newNonce(t)

	sess, mock := bringLive(t, agent, harness.LaunchConfig{
		JobID:       "test-job-" + nonce,
		PromptShare: true,
		ShareInfo:   true,
	})
	// This scenario turns on the shutdown ordering (grace → end POST → teardown). If it ever
	// regresses, vibecast's captured stderr is the fastest way to see where the flush was cut
	// off — the file survives the pane's death, unlike a live pane capture.
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("vibecast stderr on failure:\n%s", sess.Stderr())
		}
	})

	ph, err := sess.WaitPhase(30*time.Second, "live")
	if err != nil {
		t.Fatalf("wait for live before stop: %v", err)
	}
	t.Logf("phase before stop: %s", ph)

	message := "Conformance job complete " + nonce
	start := time.Now()
	if err := sess.StopBroadcast(message, "success"); err != nil {
		t.Fatalf("stop-broadcast: %v", err)
	}

	var elapsed time.Duration
	waitFor(t, 40*time.Second, "session-event end carrying our message, conclusion, and jobId", func() bool {
		for _, e := range mock.SessionEvents() {
			if e.Decoded["event"] != "end" || e.Decoded["sessionId"] != sess.SessionID {
				continue
			}
			msgOK := false
			if m, ok := e.Decoded["message"].(string); ok && strings.Contains(m, nonce) {
				msgOK = true
			}
			if msgOK && e.Decoded["conclusion"] == "success" && e.Decoded["jobId"] == sess.JobID {
				elapsed = time.Since(start)
				return true
			}
		}
		return false
	})

	// The end must wait out the flush grace, not tear down instantly.
	if elapsed < 9*time.Second {
		t.Fatalf("session-event end arrived after only %s; expected it to wait out the ~10s flush grace", elapsed)
	}
	t.Logf("session ended cleanly: conclusion=success jobId=%s after %s grace (nonce %s)",
		sess.JobID, elapsed.Round(time.Second), nonce)
}

// scenarioC11 (prompt-injection-tui): a prompt typed straight into the agent's REPL — not
// handed in via VIBECAST_INITIAL_PROMPT_FILE — must submit and surface back to the platform.
// This validates the agent's interactive submit semantics (the path a live chat-channel
// message ultimately drives): bring the agent up with NO initial prompt so the REPL sits
// idle, `tmux send-keys` a nonce line into the agent pane, and assert a `prompt` metadata
// event for this session carrying the nonce arrives within the deadline.
func scenarioC11(t *testing.T, agent string) {
	nonce := newNonce(t)

	sess, mock := bringLive(t, agent, harness.LaunchConfig{
		PromptShare: true,
		ShareInfo:   true,
	})

	// Don't type until the agent process is up (session_start proves the SessionStart hook
	// fired) — an early send-keys is silently dropped before the TUI captures the terminal.
	waitFor(t, 90*time.Second, "session_start metadata (agent process up)", func() bool {
		for _, e := range mock.MetadataPostsOfSubtype("session_start") {
			if e.Decoded["sessionId"] == sess.SessionID {
				return true
			}
		}
		return false
	})

	// Best-effort wait for the idle input box to render (see replReadyMarker). Non-fatal + a
	// settle so a TUI wording change can't wedge the scenario; keystrokes are PTY-buffered
	// regardless, so the marker only trims the settle gamble. If it never shows we proceed
	// anyway and let the prompt assertion be the judge.
	target := sess.AgentPaneTarget()
	if marker := replReadyMarker(agent); marker != "" {
		if !pollUntil(20*time.Second, func() bool {
			return strings.Contains(sess.CapturePane(target), marker)
		}) {
			t.Logf("REPL readiness marker %q not seen in pane capture; proceeding after settle", marker)
		}
	}
	time.Sleep(1 * time.Second)

	msg := "Conformance check " + nonce + ". Reply with one short line acknowledging; do not run any tools."
	if err := sess.SendKeys(target, msg); err != nil {
		t.Fatalf("send-keys prompt into %s: %v", target, err)
	}

	waitFor(t, 90*time.Second, "prompt metadata whose text contains the injected nonce", func() bool {
		for _, e := range mock.MetadataPostsOfSubtype("prompt") {
			if e.Decoded["sessionId"] != sess.SessionID {
				continue
			}
			if txt, ok := e.Decoded["prompt"].(string); ok && strings.Contains(txt, nonce) {
				return true
			}
		}
		return false
	})
	t.Logf("send-keys prompt reached the agent REPL and surfaced as a prompt event (nonce %s)", nonce)
}

// replReadyMarker returns a substring that, once present in the agent's pane capture, means
// its REPL has drawn and is accepting typed input. Empty means "no known marker for this
// agent" — the caller falls back to a fixed settle. Claude, always launched with
// --dangerously-skip-permissions, shows a persistent "bypass permissions" status line once
// idle. Each new adapter fills in its own marker here.
func replReadyMarker(agent string) string {
	switch agent {
	case "claude":
		return "bypass permissions"
	default:
		return ""
	}
}

// resumeCommandFragment returns the substring the agent's relaunch command must contain to
// prove vibecast resumed the harvested session id, per adapter. claude resumes by id
// (`claude --resume <id>`, buildClaudeResumeCommand); codex/pi add their own forms here when
// they land (codex resumes a thread, pi a session id).
func resumeCommandFragment(agent, priorID string) string {
	switch agent {
	case "claude":
		return "--resume " + priorID
	default:
		// Same as claude until a divergent adapter overrides it.
		return "--resume " + priorID
	}
}

// isWriteToolName reports whether a raw agent tool name denotes a file-writing tool. Kept
// permissive so it holds across agents (claude Write/Edit/MultiEdit, others' equivalents).
func isWriteToolName(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "write") || strings.Contains(n, "edit") || strings.Contains(n, "create")
}

// newNonce returns a fresh alphanumeric token unlikely to collide or be altered by masking,
// for correlating a specific prompt/response through the metadata channel.
func newNonce(t *testing.T) string {
	t.Helper()
	id, err := harness.NewUUIDv4()
	if err != nil {
		t.Fatalf("nonce: %v", err)
	}
	return "CONFORM" + strings.ReplaceAll(id, "-", "")[:12]
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

// pollUntil polls pred until it is true or the timeout elapses, returning whether it became
// true. Unlike waitFor it is non-fatal — for best-effort readiness checks that shouldn't
// wedge a scenario if the signal never appears.
func pollUntil(timeout time.Duration, pred func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}
