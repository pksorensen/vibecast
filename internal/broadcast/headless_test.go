package broadcast

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pksorensen/vibecast/internal/types"
)

// fakeLives is a minimal /api/lives server that records every request.
type fakeLives struct {
	srv *httptest.Server

	mu       sync.Mutex
	requests []recordedReq
	// pending is the pending-answer body served (nil => {"questionId":null,"answer":null}).
	pending map[string]interface{}
	// queue is served before pending, one entry per pending-answer read and then dropped —
	// the clear-on-first-resolved-read behaviour of the real endpoint.
	queue []map[string]interface{}
}

type recordedReq struct {
	Method, Path, Auth string
	Body               map[string]interface{}
}

func newFakeLives(t *testing.T) *fakeLives {
	t.Helper()
	f := &fakeLives{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]interface{}
		_ = json.Unmarshal(b, &body)
		f.mu.Lock()
		f.requests = append(f.requests, recordedReq{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Body: body})
		pending := f.pending
		if strings.HasSuffix(r.URL.Path, "/pending-answer") && len(f.queue) > 0 {
			pending = f.queue[0]
			f.queue = f.queue[1:]
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/pending-answer") {
			if pending == nil {
				pending = map[string]interface{}{"questionId": nil, "answer": nil}
			}
			json.NewEncoder(w).Encode(pending)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLives) setPending(p map[string]interface{}) {
	f.mu.Lock()
	f.pending = p
	f.mu.Unlock()
}

func (f *fakeLives) enqueuePending(ps ...map[string]interface{}) {
	f.mu.Lock()
	f.queue = append(f.queue, ps...)
	f.mu.Unlock()
}

func (f *fakeLives) reqs(pathSuffix string) []recordedReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recordedReq
	for _, r := range f.requests {
		if strings.HasSuffix(r.Path, pathSuffix) {
			out = append(out, r)
		}
	}
	return out
}

func waitUntil(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", d, what)
}

// recordingTmux is a fake tmux builder: capture-pane prints screen, has-session fails
// (no session) and every call is recorded.
type recordingTmux struct {
	mu     sync.Mutex
	calls  []string
	screen string
}

func (r *recordingTmux) cmd(args ...string) *exec.Cmd {
	r.mu.Lock()
	r.calls = append(r.calls, strings.Join(args, " "))
	screen := r.screen
	r.mu.Unlock()
	if len(args) > 0 && args[0] == "capture-pane" {
		return exec.Command("printf", "%s", screen)
	}
	if len(args) > 0 && args[0] == "display-message" {
		return exec.Command("printf", "0")
	}
	return exec.Command("false")
}

func (r *recordingTmux) setScreen(s string) {
	r.mu.Lock()
	r.screen = s
	r.mu.Unlock()
}

func (r *recordingTmux) callsWithPrefix(p string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.calls {
		if strings.HasPrefix(c, p) {
			out = append(out, c)
		}
	}
	return out
}

// With the broadcast off, metaCh frames (written for the WebSocket, without a sessionId)
// are POSTed to /api/lives/metadata with the sessionId added and the runner bearer set.
func TestDrainMetaHTTPPostsFramesWithSessionIDAndBearer(t *testing.T) {
	f := newFakeLives(t)
	t.Setenv("AGENTICS_TOKEN", "rnt_drain")
	status := &types.SharedStatus{ServerHost: f.srv.URL}
	metaCh := make(chan []byte, 4)
	done := make(chan struct{})
	defer close(done)
	go drainMetaHTTP("sess-1", status, metaCh, done)

	metaCh <- []byte(`{"type":"metadata","subtype":"capabilities","keyboard":false}`)
	metaCh <- []byte(`not json`)
	metaCh <- []byte(`{"type":"metadata","subtype":"stream_info","sessionId":"explicit"}`)

	waitUntil(t, 3*time.Second, "two metadata posts", func() bool { return len(f.reqs("/api/lives/metadata")) == 2 })
	posts := f.reqs("/api/lives/metadata")
	if posts[0].Body["subtype"] != "capabilities" || posts[0].Body["sessionId"] != "sess-1" {
		t.Errorf("first post = %v", posts[0].Body)
	}
	if posts[1].Body["subtype"] != "stream_info" || posts[1].Body["sessionId"] != "explicit" {
		t.Errorf("second post must keep its own sessionId: %v", posts[1].Body)
	}
	for _, p := range posts {
		if p.Auth != "Bearer rnt_drain" {
			t.Errorf("metadata post auth = %q", p.Auth)
		}
	}
}

// The pane watch runs the same detection with or without the broadcast; only the
// /_relay/snapshot post depends on it.
func TestRunPaneWatchSnapshotOnlyWhenBroadcasting(t *testing.T) {
	screen := "This session is 3 days old and 180k tokens.\n❯ Resume from summary\n  Resume full session as-is\n"
	for _, broadcasting := range []bool{false, true} {
		f := newFakeLives(t)
		t.Setenv("AGENTICS_TOKEN", "rnt_pane")
		t.Setenv("AGENTICS_JOB_MODE", "")
		t.Setenv("VIBECAST_HOME", t.TempDir())
		tm := &recordingTmux{screen: screen}
		done := make(chan struct{})
		go runPaneWatch(done, "sess-2", f.srv.URL, &types.SharedStatus{}, "main", tm.cmd, &paneAutomation{}, broadcasting)

		waitUntil(t, 5*time.Second, "alp_pane question posted", func() bool {
			for _, p := range f.reqs("/api/lives/metadata") {
				if p.Body["subtype"] == "alp_pane" {
					return true
				}
			}
			return false
		})
		// Give the tick that posted the question time to reach its snapshot step.
		time.Sleep(300 * time.Millisecond)
		close(done)
		time.Sleep(200 * time.Millisecond)

		for _, p := range f.reqs("/api/lives/metadata") {
			if p.Auth != "Bearer rnt_pane" {
				t.Errorf("broadcasting=%v: metadata auth = %q", broadcasting, p.Auth)
			}
		}
		snaps := f.reqs("/_relay/snapshot")
		if broadcasting && len(snaps) == 0 {
			t.Errorf("broadcasting: expected /_relay/snapshot posts")
		}
		for _, sn := range snaps {
			if sn.Auth != "" {
				t.Errorf("/_relay/snapshot is a relay path and must not carry the runner bearer, got %q", sn.Auth)
			}
		}
		if !broadcasting && len(snaps) != 0 {
			t.Errorf("broadcast off: /_relay/snapshot must not be posted, got %d", len(snaps))
		}
	}
}

// With the broadcast off (postSnapshots=false) the job-mode first-run auto-answers still
// fire: the workspace-trust dialog is answered by navigating to "Yes, I trust this folder"
// and the post-OAuth "Login successful / Press Enter" screen is dismissed with Enter —
// without a WebSocket and without any /_relay/snapshot post.
func TestRunPaneWatchOnboardingAutoAnswersWithBroadcastOff(t *testing.T) {
	f := newFakeLives(t)
	t.Setenv("AGENTICS_JOB_MODE", "1")
	t.Setenv("VIBECAST_ALLOWED_DIRECTORIES", "")
	t.Setenv("VIBECAST_HOME", t.TempDir())
	target := "vibecast-sess-onb:main.0"
	tm := &recordingTmux{screen: "Quick safety check: Is this a project you created or one you trust?\n" +
		"  No, exit\n" +
		"❯ Yes, I trust this folder\n"}
	done := make(chan struct{})
	defer close(done)
	go runPaneWatch(done, "sess-onb", f.srv.URL, &types.SharedStatus{}, "main", tm.cmd, &paneAutomation{}, false)

	enters := func() int { return len(tm.callsWithPrefix("send-keys -t " + target + " Enter")) }
	waitUntil(t, 6*time.Second, "trust dialog answered with Enter", func() bool { return enters() >= 1 })
	if n := len(tm.callsWithPrefix("send-keys")); n != 1 {
		t.Fatalf("trust dialog: expected exactly one send-keys (Enter on the highlighted trust option), got %v", tm.callsWithPrefix("send-keys"))
	}

	tm.setScreen("Login successful. Press Enter to continue\n")
	waitUntil(t, 6*time.Second, "login-successful screen dismissed with Enter", func() bool { return enters() >= 2 })

	if snaps := f.reqs("/_relay/snapshot"); len(snaps) != 0 {
		t.Errorf("broadcast off: /_relay/snapshot must not be posted, got %d", len(snaps))
	}
}

// ConnectBroadcast with VIBECAST_BROADCAST=off never touches the WebSocket or the ttyd
// port, still drains metadata over HTTP, and returns once the tmux session is gone.
func TestConnectBroadcastOffRunsHeadless(t *testing.T) {
	f := newFakeLives(t)
	t.Setenv("VIBECAST_BROADCAST", "off")
	t.Setenv("AGENTICS_TOKEN", "rnt_off")
	t.Setenv("AGENTICS_JOB_MODE", "1")
	t.Setenv("VIBECAST_HOME", t.TempDir())
	t.Setenv("TMUX", "") // the session never exists, so has-session fails and the run ends
	status := &types.SharedStatus{ServerHost: f.srv.URL}
	metaCh := make(chan []byte, 4)
	metaCh <- []byte(`{"type":"metadata","subtype":"capabilities"}`)

	returned := make(chan struct{})
	go func() {
		// ttydPort 1 is never dialled in off mode.
		ConnectBroadcast("sess-off-"+time.Now().Format("150405.000"), status, metaCh, 1, "main")
		close(returned)
	}()

	waitUntil(t, 3*time.Second, "metadata drained over HTTP", func() bool { return len(f.reqs("/api/lives/metadata")) > 0 })
	select {
	case <-returned:
	case <-time.After(15 * time.Second):
		t.Fatal("headless run did not end after the tmux session was gone")
	}
	// The job-mode answer loop polled pending-answer with the bearer.
	polls := f.reqs("/pending-answer")
	if len(polls) == 0 {
		t.Fatal("job-mode pending-answer loop did not poll with the broadcast off")
	}
	if polls[0].Auth != "Bearer rnt_off" {
		t.Errorf("pending-answer auth = %q", polls[0].Auth)
	}
	for _, r := range f.reqs("") {
		if strings.Contains(r.Path, "/broadcast/ws") || strings.Contains(r.Path, "/_relay/snapshot") || strings.Contains(r.Path, "/chat/ws") {
			t.Errorf("broadcast off but server saw %s %s", r.Method, r.Path)
		}
	}
}

func TestWithSessionID(t *testing.T) {
	out, err := withSessionID([]byte(`{"type":"metadata","sessionId":""}`), "s9")
	if err != nil || !strings.Contains(string(out), `"sessionId":"s9"`) {
		t.Fatalf("empty sessionId should be filled: %s %v", out, err)
	}
	if _, err := withSessionID([]byte(`[1,2]`), "s9"); err == nil {
		t.Fatal("non-object must be rejected")
	}
}
