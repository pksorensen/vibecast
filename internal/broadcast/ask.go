package broadcast

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/pksorensen/vibecast/internal/util"
)

// AskQuestionIDPrefix marks the toolUseId of a question raised through the ask_question
// MCP tool. The server echoes it back as the pending-answer questionId; the job-mode
// answer loop hands such answers to the waiting tool call and never types them into the
// pane.
const AskQuestionIDPrefix = "vibecast-ask-"

// IsAskQuestionID reports whether a pending-answer questionId belongs to ask_question.
func IsAskQuestionID(id string) bool {
	return strings.HasPrefix(id, AskQuestionIDPrefix)
}

var (
	askWaitersMu sync.Mutex
	askWaiters   = map[string]chan string{}
)

func registerAsk(id string) chan string {
	ch := make(chan string, 1)
	askWaitersMu.Lock()
	askWaiters[id] = ch
	askWaitersMu.Unlock()
	return ch
}

func unregisterAsk(id string) {
	askWaitersMu.Lock()
	delete(askWaiters, id)
	askWaitersMu.Unlock()
}

// deliverAskAnswer hands answer to the ask_question call waiting on id. Returns false if
// nobody is waiting (already answered, timed out, or a question from an earlier process).
// Safe to call more than once: only the first delivery is kept.
func deliverAskAnswer(id, answer string) bool {
	askWaitersMu.Lock()
	ch, ok := askWaiters[id]
	askWaitersMu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- answer:
	default:
	}
	return true
}

// AskQuestionRequest is the body of the control socket's POST /ask-question.
type AskQuestionRequest struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

// AskQuestionResponse is the reply of POST /ask-question.
type AskQuestionResponse struct {
	QuestionID string `json:"questionId"`
	Answer     string `json:"answer"`
}

// BuildAskQuestionMetadata returns the metadata event ask_question posts. It is exactly the
// shape vibecast's PreToolUse hook posts for a Claude AskUserQuestion tool_use
// (hooks.handleHookTool), so a server parses one shape for both: toolInput follows
// Claude's AskUserQuestion schema {questions:[{question, header?, options:[{label,
// description?}], multiSelect}]}.
func BuildAskQuestionMetadata(sessionID, toolUseID, claudeSessionID string, req AskQuestionRequest) map[string]interface{} {
	opts := make([]interface{}, 0, len(req.Options))
	for _, o := range req.Options {
		opts = append(opts, map[string]interface{}{"label": o})
	}
	return map[string]interface{}{
		"sessionId": sessionID,
		"type":      "metadata",
		"subtype":   "tool_use",
		"toolName":  "AskUserQuestion",
		"toolInput": map[string]interface{}{
			"questions": []interface{}{
				map[string]interface{}{
					"question":    req.Question,
					"header":      "Question",
					"options":     opts,
					"multiSelect": false,
				},
			},
		},
		"toolUseId":       toolUseID,
		"claudeSessionId": claudeSessionID,
		"transcriptPath":  "",
		"timestamp":       time.Now().Unix(),
	}
}

// AskQuestion raises a question for the human (or the controlling server) and blocks
// until it is answered or ctx ends. It posts the question as an AskUserQuestion tool_use
// metadata event and then waits for its answer from the session's shared pending-answer
// reader (answer_reader.go), which the job-mode answer loop also subscribes to.
func AskQuestion(ctx context.Context, sessionID, serverHost, claudeSessionID string, req AskQuestionRequest) (AskQuestionResponse, error) {
	if strings.TrimSpace(req.Question) == "" {
		return AskQuestionResponse{}, errors.New("question is required")
	}
	if sessionID == "" {
		return AskQuestionResponse{}, errors.New("no live session")
	}
	id := AskQuestionIDPrefix + util.GenerateUUIDv4()
	ch := registerAsk(id)
	defer unregisterAsk(id)

	payload, _ := json.Marshal(BuildAskQuestionMetadata(sessionID, id, claudeSessionID, req))
	resp, err := util.PostLivesJSON(util.ServerURL(serverHost, "/api/lives/metadata"), payload)
	if err != nil {
		return AskQuestionResponse{QuestionID: id}, fmt.Errorf("post question: %w", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return AskQuestionResponse{QuestionID: id}, fmt.Errorf("post question: HTTP %d", resp.StatusCode)
	}
	logDebug("[ask] posted questionId=%s\n", id)

	// Wait through the shared per-session pending-answer reader: it is the only reader of
	// the (clear-on-first-read) endpoint, so answers to other questions — a native
	// AskUserQuestion, a permission prompt, a concurrent ask_question — still reach their
	// own consumer instead of being dropped here.
	reader := acquireAnswerReader(sessionID, util.ServerURL(serverHost, fmt.Sprintf("/api/lives/sessions/%s/pending-answer", sessionID)))
	defer reader.release()

	select {
	case answer := <-ch:
		logDebug("[ask] answered questionId=%s\n", id)
		return AskQuestionResponse{QuestionID: id, Answer: answer}, nil
	case <-ctx.Done():
		return AskQuestionResponse{QuestionID: id}, fmt.Errorf("no answer before timeout: %w", ctx.Err())
	}
}
