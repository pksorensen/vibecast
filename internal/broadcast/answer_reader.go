package broadcast

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/pksorensen/vibecast/internal/telemetry"
	"github.com/pksorensen/vibecast/internal/util"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// pendingAnswerPollInterval is how often the shared pending-answer reader polls. A var so
// tests can shorten it.
var pendingAnswerPollInterval = 3 * time.Second

// subQuestion is one entry of a multi-question pending answer.
type subQuestion struct {
	ToolUseID      string   `json:"toolUseId"`
	Question       string   `json:"question"`
	Options        []string `json:"options"`
	MultiSelect    bool     `json:"multiSelect"`
	ResolvedAnswer *string  `json:"resolvedAnswer"`
}

// pendingAnswer is one resolved answer read from GET /api/lives/sessions/{id}/pending-answer.
type pendingAnswer struct {
	QuestionID   string
	QuestionType string
	Options      []string
	Answer       string
	MultiSelect  bool
	SubQuestions []subQuestion
}

// maxAnswerBacklog bounds the answers kept for an injector that is not (yet) subscribed.
const maxAnswerBacklog = 16

// answerReader is the single pending-answer reader for one session URL. The server may
// clear a question on the first read that returns its resolved answer, so only one
// goroutine per session may read the endpoint; it dispatches what it reads:
//   - ask_question ids go to the waiting ask_question call (deliverAskAnswer);
//   - every other answer goes to every subscribed job-mode answer loop (one per pane, as
//     when each loop polled on its own), or is kept in a small backlog until one
//     subscribes (e.g. between broadcast reconnects).
//
// The reader polls while it has at least one user (an injector or a waiting ask) and stops
// when the last one releases it. Lock order: answerReadersMu before mu.
type answerReader struct {
	url       string
	sessionID string

	mu        sync.Mutex
	refs      int
	injectors map[chan pendingAnswer]struct{}
	backlog   []pendingAnswer
	stop      chan struct{}
}

var (
	answerReadersMu sync.Mutex
	answerReaders   = map[string]*answerReader{}
)

// acquireAnswerReader returns the reader for pollURL, starting it if needed. Every call
// must be paired with release.
func acquireAnswerReader(sessionID, pollURL string) *answerReader {
	answerReadersMu.Lock()
	defer answerReadersMu.Unlock()
	r, ok := answerReaders[pollURL]
	if !ok {
		r = &answerReader{url: pollURL, sessionID: sessionID, injectors: map[chan pendingAnswer]struct{}{}}
		answerReaders[pollURL] = r
	}
	r.mu.Lock()
	r.refs++
	if r.stop == nil {
		r.stop = make(chan struct{})
		go r.run(r.stop, pendingAnswerPollInterval)
	}
	r.mu.Unlock()
	return r
}

func (r *answerReader) release() {
	answerReadersMu.Lock()
	defer answerReadersMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refs--
	if r.refs > 0 || r.stop == nil {
		return
	}
	close(r.stop)
	r.stop = nil
	// Keep the entry while it holds answers read for an injector that has not subscribed
	// yet (e.g. a broadcast reconnect gap), so the next answer loop still gets them.
	if len(r.backlog) == 0 && answerReaders[r.url] == r {
		delete(answerReaders, r.url)
	}
}

// subscribeInjector registers a job-mode answer loop as a receiver of non-ask answers and
// returns its channel. The first subscriber also receives the backlog.
func (r *answerReader) subscribeInjector() <-chan pendingAnswer {
	ch := make(chan pendingAnswer, maxAnswerBacklog)
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.injectors) == 0 {
		for _, a := range r.backlog {
			ch <- a
		}
		r.backlog = nil
	}
	r.injectors[ch] = struct{}{}
	return ch
}

// unsubscribeInjector detaches ch. When it was the last subscriber, the answers it had not
// consumed go back to the backlog so a later answer loop (after a reconnect) still gets
// them.
func (r *answerReader) unsubscribeInjector(sub <-chan pendingAnswer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for ch := range r.injectors {
		if (<-chan pendingAnswer)(ch) != sub {
			continue
		}
		delete(r.injectors, ch)
		if len(r.injectors) > 0 {
			return
		}
		for {
			select {
			case a := <-ch:
				r.pushBacklogLocked(a)
				continue
			default:
			}
			return
		}
	}
}

func (r *answerReader) pushBacklogLocked(a pendingAnswer) {
	r.backlog = append(r.backlog, a)
	if len(r.backlog) > maxAnswerBacklog {
		r.backlog = r.backlog[len(r.backlog)-maxAnswerBacklog:]
	}
}

func (r *answerReader) dispatch(a pendingAnswer) {
	if IsAskQuestionID(a.QuestionID) {
		// Answered as the tool result, never typed into the pane. No waiter means the
		// call already returned (or belongs to an earlier process): nothing to do.
		deliverAskAnswer(a.QuestionID, a.Answer)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.injectors) == 0 {
		r.pushBacklogLocked(a)
		return
	}
	for ch := range r.injectors {
		select {
		case ch <- a:
		default:
			logDebug("[answer] answer loop busy, dropped questionId=%s for one subscriber\n", a.QuestionID)
		}
	}
}

func (r *answerReader) run(stop <-chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if a, ok := r.poll(); ok {
				r.dispatch(a)
			}
		}
	}
}

// poll does one GET of pending-answer and returns the resolved answer, if any.
func (r *answerReader) poll() (pendingAnswer, bool) {
	_, span := telemetry.Tracer().Start(context.Background(), "vibecast.answer.poll",
		trace.WithAttributes(
			attribute.String("session.id", r.sessionID),
			attribute.String("poll.url", r.url),
		))
	defer span.End()
	resp, err := util.GetLives(r.url)
	if err != nil {
		span.RecordError(err)
		logDebug("[answer] poll error: %v\n", err)
		return pendingAnswer{}, false
	}
	var body struct {
		QuestionID   *string       `json:"questionId"`
		QuestionType string        `json:"questionType"`
		Options      []string      `json:"options"`
		Answer       *string       `json:"answer"`
		MultiSelect  bool          `json:"multiSelect"`
		SubQuestions []subQuestion `json:"subQuestions"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()

	hasQuestion := body.QuestionID != nil && *body.QuestionID != ""
	hasAnswer := body.Answer != nil
	span.SetAttributes(
		attribute.Bool("poll.has_question", hasQuestion),
		attribute.Bool("poll.has_answer", hasAnswer),
		attribute.String("poll.question_type", body.QuestionType),
	)
	if hasQuestion {
		span.SetAttributes(attribute.String("poll.question_id", *body.QuestionID))
	}
	if !hasQuestion || !hasAnswer {
		return pendingAnswer{}, false
	}
	return pendingAnswer{
		QuestionID:   *body.QuestionID,
		QuestionType: body.QuestionType,
		Options:      body.Options,
		Answer:       *body.Answer,
		MultiSelect:  body.MultiSelect,
		SubQuestions: body.SubQuestions,
	}, true
}
