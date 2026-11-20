package broadcast

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The ask_question metadata event has exactly the shape the PreToolUse hook posts for a
// Claude AskUserQuestion tool_use, so servers parse one shape.
func TestBuildAskQuestionMetadataShape(t *testing.T) {
	m := BuildAskQuestionMetadata("sess", "vibecast-ask-1", "claude-1", AskQuestionRequest{
		Question: "Which colour?",
		Options:  []string{"Red", "Blue"},
	})
	raw, _ := json.Marshal(m)
	var got map[string]interface{}
	json.Unmarshal(raw, &got)

	for k, want := range map[string]interface{}{
		"sessionId": "sess", "type": "metadata", "subtype": "tool_use",
		"toolName": "AskUserQuestion", "toolUseId": "vibecast-ask-1", "claudeSessionId": "claude-1",
	} {
		if got[k] != want {
			t.Errorf("%s = %v, want %v", k, got[k], want)
		}
	}
	if _, ok := got["timestamp"].(float64); !ok {
		t.Errorf("timestamp missing: %v", got["timestamp"])
	}
	qs := got["toolInput"].(map[string]interface{})["questions"].([]interface{})
	if len(qs) != 1 {
		t.Fatalf("questions = %v", qs)
	}
	q := qs[0].(map[string]interface{})
	if q["question"] != "Which colour?" || q["multiSelect"] != false {
		t.Errorf("question = %v", q)
	}
	if h, _ := q["header"].(string); h == "" {
		t.Errorf("header should be set (AskUserQuestion requires one): %v", q)
	}
	opts := q["options"].([]interface{})
	if len(opts) != 2 || opts[0].(map[string]interface{})["label"] != "Red" || opts[1].(map[string]interface{})["label"] != "Blue" {
		t.Errorf("options = %v", opts)
	}

	// No options => an empty options list (free-text answer), never null.
	raw, _ = json.Marshal(BuildAskQuestionMetadata("s", "id", "", AskQuestionRequest{Question: "Why?"}))
	if !strings.Contains(string(raw), `"options":[]`) {
		t.Errorf("free-text question must carry options:[]: %s", raw)
	}
}

// AskQuestion posts the question, then returns the answer once pending-answer reports it
// for this question id — ignoring answers for other questions.
func TestAskQuestionReturnsAnswerFromPendingAnswer(t *testing.T) {
	f := newFakeLives(t)
	t.Setenv("AGENTICS_TOKEN", "rnt_ask")
	old := pendingAnswerPollInterval
	pendingAnswerPollInterval = 50 * time.Millisecond
	defer func() { pendingAnswerPollInterval = old }()

	// Another question's answer is pending first; it must not be taken.
	f.setPending(map[string]interface{}{"questionId": "toolu_other", "answer": "Wrong"})

	type result struct {
		res AskQuestionResponse
		err error
	}
	out := make(chan result, 1)
	go func() {
		res, err := AskQuestion(context.Background(), "sess-ask", f.srv.URL, "", AskQuestionRequest{Question: "Which colour?", Options: []string{"Red", "Blue"}})
		out <- result{res, err}
	}()

	var qid string
	waitUntil(t, 3*time.Second, "question posted", func() bool {
		for _, p := range f.reqs("/api/lives/metadata") {
			if p.Body["toolName"] == "AskUserQuestion" {
				qid, _ = p.Body["toolUseId"].(string)
				if p.Auth != "Bearer rnt_ask" {
					t.Errorf("metadata auth = %q", p.Auth)
				}
				return true
			}
		}
		return false
	})
	if !IsAskQuestionID(qid) {
		t.Fatalf("toolUseId %q lacks the ask prefix", qid)
	}
	time.Sleep(200 * time.Millisecond)
	select {
	case r := <-out:
		t.Fatalf("returned before its own answer: %+v", r)
	default:
	}
	f.setPending(map[string]interface{}{"questionId": qid, "answer": "Blue"})

	select {
	case r := <-out:
		if r.err != nil || r.res.Answer != "Blue" || r.res.QuestionID != qid {
			t.Fatalf("AskQuestion = %+v, %v", r.res, r.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("AskQuestion did not return after the answer was available")
	}
	for _, p := range f.reqs("/pending-answer") {
		if p.Auth != "Bearer rnt_ask" {
			t.Errorf("pending-answer auth = %q", p.Auth)
		}
	}
}

func TestAskQuestionTimesOut(t *testing.T) {
	f := newFakeLives(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	res, err := AskQuestion(ctx, "s", f.srv.URL, "", AskQuestionRequest{Question: "Anyone?"})
	if err == nil || !IsAskQuestionID(res.QuestionID) {
		t.Fatalf("expected timeout with a question id, got %+v %v", res, err)
	}
	if _, err := AskQuestion(context.Background(), "s", f.srv.URL, "", AskQuestionRequest{}); err == nil {
		t.Fatal("empty question must be rejected")
	}
}

// The job-mode answer loop hands an ask_question answer to the waiting call and never
// types it into the pane (the server may clear the question on that first read, so the
// loop's delivery is the one that counts).
func TestAnswerLoopDeliversAskAnswerWithoutTmux(t *testing.T) {
	f := newFakeLives(t)
	id := AskQuestionIDPrefix + "loop-test"
	ch := registerAsk(id)
	defer unregisterAsk(id)
	f.setPending(map[string]interface{}{"questionId": id, "questionType": "tool", "answer": "Green", "options": []string{"Green"}})

	tm := &recordingTmux{}
	done := make(chan struct{})
	defer close(done)
	startAnswerLoop(done, "sess-loop", f.srv.URL, "main", tm.cmd)

	select {
	case a := <-ch:
		if a != "Green" {
			t.Fatalf("answer = %q", a)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("answer loop did not deliver the ask_question answer")
	}
	if sends := tm.callsWithPrefix("send-keys"); len(sends) != 0 {
		t.Fatalf("ask_question answer was typed into the pane: %v", sends)
	}
}

// The pending-answer endpoint clears a question on the first read that returns its
// answer. While an ask_question call is waiting, answers to other questions — a native
// AskUserQuestion (toolu_…) and a second, concurrent ask_question — must still reach their
// own consumer rather than being read and dropped by the first ask.
func TestAskQuestionDoesNotSwallowForeignAnswers(t *testing.T) {
	f := newFakeLives(t)
	old := pendingAnswerPollInterval
	pendingAnswerPollInterval = 50 * time.Millisecond
	defer func() { pendingAnswerPollInterval = old }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		res AskQuestionResponse
		err error
	}
	outA := make(chan result, 1)
	outB := make(chan result, 1)
	go func() {
		r, err := AskQuestion(ctx, "sess-foreign", f.srv.URL, "", AskQuestionRequest{Question: "A?"})
		outA <- result{r, err}
	}()
	go func() {
		r, err := AskQuestion(ctx, "sess-foreign", f.srv.URL, "", AskQuestionRequest{Question: "B?"})
		outB <- result{r, err}
	}()
	ids := map[string]string{}
	waitUntil(t, 3*time.Second, "both questions posted", func() bool {
		for _, p := range f.reqs("/api/lives/metadata") {
			q := p.Body["toolInput"].(map[string]interface{})["questions"].([]interface{})[0].(map[string]interface{})["question"].(string)
			ids[q], _ = p.Body["toolUseId"].(string)
		}
		return len(ids) == 2
	})

	// A job-mode answer loop subscribes as the injector (as startAnswerLoop does).
	reader := acquireAnswerReader("sess-foreign", f.srv.URL+"/api/lives/sessions/sess-foreign/pending-answer")
	injected := reader.subscribeInjector()
	defer reader.release()
	defer reader.unsubscribeInjector(injected)

	// Served once each, in this order: the native answer, then B's, then A's.
	f.enqueuePending(
		map[string]interface{}{"questionId": "toolu_native", "questionType": "tool", "answer": "Native"},
		map[string]interface{}{"questionId": ids["B?"], "answer": "Bee"},
		map[string]interface{}{"questionId": ids["A?"], "answer": "Ay"},
	)

	for name, c := range map[string]struct {
		out  chan result
		want string
	}{"A": {outA, "Ay"}, "B": {outB, "Bee"}} {
		select {
		case r := <-c.out:
			if r.err != nil || r.res.Answer != c.want {
				t.Errorf("ask %s = %+v, %v; want %q", name, r.res, r.err, c.want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("ask %s never got its answer", name)
		}
	}
	select {
	case a := <-injected:
		if a.QuestionID != "toolu_native" || a.Answer != "Native" {
			t.Fatalf("injector got %+v", a)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the native AskUserQuestion answer was swallowed by a waiting ask_question")
	}
}

// A non-ask answer read while no answer loop is subscribed (e.g. a broadcast reconnect
// gap while an ask_question holds the reader) is kept for the next answer loop.
func TestAnswerReaderKeepsBacklogForLaterInjector(t *testing.T) {
	f := newFakeLives(t)
	old := pendingAnswerPollInterval
	pendingAnswerPollInterval = 50 * time.Millisecond
	defer func() { pendingAnswerPollInterval = old }()
	url := f.srv.URL + "/api/lives/sessions/sess-backlog/pending-answer"

	r := acquireAnswerReader("sess-backlog", url)
	f.enqueuePending(map[string]interface{}{"questionId": "toolu_gap", "questionType": "permission", "answer": "1"})
	waitUntil(t, 3*time.Second, "answer read", func() bool { return len(f.reqs("/pending-answer")) >= 2 })
	r.release()

	r2 := acquireAnswerReader("sess-backlog", url)
	defer r2.release()
	ch := r2.subscribeInjector()
	defer r2.unsubscribeInjector(ch)
	select {
	case a := <-ch:
		if a.QuestionID != "toolu_gap" {
			t.Fatalf("got %+v", a)
		}
	case <-time.After(time.Second):
		t.Fatal("backlogged answer lost across reader restart")
	}
}

// The job-mode answer loop, now fed by the shared reader, still types a non-ask answer into
// its pane exactly once — even when the server serves the same resolved answer again.
func TestAnswerLoopInjectsToolAnswerOnce(t *testing.T) {
	f := newFakeLives(t)
	old := pendingAnswerPollInterval
	pendingAnswerPollInterval = 50 * time.Millisecond
	defer func() { pendingAnswerPollInterval = old }()
	ans := map[string]interface{}{"questionId": "toolu_free", "questionType": "tool", "answer": "hello there"}
	f.enqueuePending(ans, ans)

	tm := &recordingTmux{}
	done := make(chan struct{})
	defer close(done)
	startAnswerLoop(done, "sess-inject", f.srv.URL, "main", tm.cmd)

	target := "vibecast-sess-inject:main.0"
	waitUntil(t, 3*time.Second, "free-text answer typed", func() bool {
		return len(tm.callsWithPrefix("send-keys -t "+target+" Enter")) >= 1
	})
	waitUntil(t, 3*time.Second, "both copies served", func() bool { return len(f.reqs("/pending-answer")) >= 3 })
	time.Sleep(300 * time.Millisecond)
	want := []string{"send-keys -t " + target + " -l -- hello there", "send-keys -t " + target + " Enter"}
	got := tm.callsWithPrefix("send-keys")
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("send-keys = %q, want %q", got, want)
	}
}

// Several answer loops on one session (one per pane) each receive every non-ask answer, as
// when each loop polled pending-answer on its own.
func TestAnswerReaderFansOutToEveryAnswerLoop(t *testing.T) {
	f := newFakeLives(t)
	old := pendingAnswerPollInterval
	pendingAnswerPollInterval = 50 * time.Millisecond
	defer func() { pendingAnswerPollInterval = old }()
	url := f.srv.URL + "/api/lives/sessions/sess-fan/pending-answer"

	r1 := acquireAnswerReader("sess-fan", url)
	defer r1.release()
	c1 := r1.subscribeInjector()
	defer r1.unsubscribeInjector(c1)
	r2 := acquireAnswerReader("sess-fan", url)
	defer r2.release()
	c2 := r2.subscribeInjector()
	defer r2.unsubscribeInjector(c2)

	f.enqueuePending(map[string]interface{}{"questionId": "toolu_fan", "answer": "x"})
	for i, c := range []<-chan pendingAnswer{c1, c2} {
		select {
		case a := <-c:
			if a.QuestionID != "toolu_fan" {
				t.Errorf("loop %d got %+v", i, a)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("loop %d never got the answer", i)
		}
	}
}
