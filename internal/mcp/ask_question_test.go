package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pksorensen/vibecast/internal/control"
	"github.com/pksorensen/vibecast/internal/types"
)

func TestAskQuestionToolRegistered(t *testing.T) {
	var tool map[string]interface{}
	for _, raw := range mcpToolsList() {
		m := raw.(map[string]interface{})
		if m["name"] == "ask_question" {
			tool = m
		}
	}
	if tool == nil {
		t.Fatal("ask_question is not in tools/list")
	}
	schema := tool["inputSchema"].(map[string]interface{})
	props := schema["properties"].(map[string]interface{})
	if props["question"].(map[string]interface{})["type"] != "string" {
		t.Errorf("question must be a string: %v", props["question"])
	}
	opts := props["options"].(map[string]interface{})
	if opts["type"] != "array" || opts["items"].(map[string]interface{})["type"] != "string" {
		t.Errorf("options must be string[]: %v", opts)
	}
	if req := schema["required"].([]string); len(req) != 1 || req[0] != "question" {
		t.Errorf("required = %v", req)
	}
}

// End to end through the stdio tool handler: the MCP server forwards ask_question over
// the control socket to the vibecast process, which posts the AskUserQuestion-shaped
// metadata, waits on pending-answer and returns the answer as the tool result.
func TestAskQuestionToolReturnsAnswerViaControlSocket(t *testing.T) {
	var mu sync.Mutex
	var qid string
	var metaAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/api/lives/metadata":
			var m map[string]interface{}
			json.Unmarshal(b, &m)
			if m["toolName"] == "AskUserQuestion" && m["subtype"] == "tool_use" && m["sessionId"] == "sess-mcp" {
				qid, _ = m["toolUseId"].(string)
				metaAuth = r.Header.Get("Authorization")
			}
			w.Write([]byte(`{"ok":true}`))
		case strings.HasSuffix(r.URL.Path, "/sessions/sess-mcp/pending-answer"):
			if qid == "" {
				w.Write([]byte(`{"questionId":null,"answer":null}`))
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"questionId": qid, "questionType": "tool", "answer": "Ship it"})
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	t.Setenv("VIBECAST_HOME", t.TempDir())
	t.Setenv("AGENTICS_TOKEN", "rnt_mcp")
	status := &types.SharedStatus{SessionID: "sess-mcp", ServerHost: srv.URL}
	ln, err := control.StartControlServer(status, nil, nil)
	if err != nil {
		t.Fatalf("control server: %v", err)
	}
	defer control.CleanupControlSocket(ln)

	params, _ := json.Marshal(map[string]interface{}{
		"name":      "ask_question",
		"arguments": map[string]interface{}{"question": "Ready to ship?", "options": []string{"Ship it", "Hold"}},
	})
	req := types.JsonrpcRequest{JSONRPC: "2.0", ID: json.RawMessage(`7`), Method: "tools/call", Params: params}

	done := make(chan *types.JsonrpcResponse, 1)
	go func() {
		sel := ""
		done <- handleMCPToolCall(req, control.ControlSocketPath(), &sel)
	}()
	var resp *types.JsonrpcResponse
	select {
	case resp = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("ask_question did not return")
	}
	result := resp.Result.(map[string]interface{})
	text := result["content"].([]interface{})[0].(map[string]interface{})["text"]
	if result["isError"] != false || text != "Ship it" {
		t.Fatalf("tool result = %v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if metaAuth != "Bearer rnt_mcp" {
		t.Errorf("question metadata auth = %q", metaAuth)
	}
}

func TestAskQuestionToolRequiresQuestion(t *testing.T) {
	text, isErr := askQuestionViaControl("/nonexistent.sock", struct {
		Question string   `json:"question"`
		Options  []string `json:"options,omitempty"`
	}{})
	if !isErr || !strings.Contains(text, "question is required") {
		t.Fatalf("got %q %v", text, isErr)
	}
}

func TestMCPToolName(t *testing.T) {
	if got := mcpToolName(json.RawMessage(`{"name":"ask_question","arguments":{}}`)); got != "ask_question" {
		t.Fatalf("got %q", got)
	}
	if got := mcpToolName(nil); got != "" {
		t.Fatalf("got %q", got)
	}
}
