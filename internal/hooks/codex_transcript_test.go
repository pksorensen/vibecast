package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real codex session-rollout line shapes (captured from codex-cli 0.142.5). The assistant
// text lives in a response_item/message/role=assistant carrying output_text blocks; the
// developer message is the appended system prompt and the user lines are the prompt echoes
// — none of those may leak into the assistant_response.
const (
	codexSessionMeta   = `{"type":"session_meta","payload":{"id":"019f4e0f-a6b4-7bb2-bd82-56357fb7ef31","cwd":"/w"}}`
	codexTaskStarted   = `{"type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`
	codexDeveloperMsg  = `{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"STATION_SECRET_MUST_NOT_LEAK"}]}}`
	codexUserMsg       = `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"what is the code"}]}}`
	codexAgentEventMsg = `{"type":"event_msg","payload":{"type":"agent_message","message":"CONFORMcodex01","phase":"final_answer"}}`
	codexAssistantMsg  = `{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"CONFORMcodex01"}],"phase":"final_answer"}}`
	codexTokenCount    = `{"type":"event_msg","payload":{"type":"token_count","info":{}}}`
	codexTaskComplete  = `{"type":"event_msg","payload":{"type":"task_complete"}}`
)

// TestCodexRollout_AssistantExtracted: the shared ingestion path (waitForFinalAssistant →
// extractAssistantText) must surface a codex assistant message from its native rollout
// JSONL, exactly as it does for a claude transcript — this is what makes C04/C06 pass for
// codex without any change to the Stop-hook logic.
func TestCodexRollout_AssistantExtracted(t *testing.T) {
	t.Setenv("VIBECAST_HOME", t.TempDir())

	tp := filepath.Join(t.TempDir(), "rollout.jsonl")
	seed := strings.Join([]string{
		codexSessionMeta, codexTaskStarted, codexDeveloperMsg, codexUserMsg,
		codexAgentEventMsg, codexAssistantMsg, codexTokenCount, codexTaskComplete,
	}, "\n") + "\n"
	if err := os.WriteFile(tp, []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}

	lines, text, _ := waitForFinalAssistant("stream-codex", tp, 500*time.Millisecond)

	if !strings.Contains(text, "CONFORMcodex01") {
		t.Fatalf("expected codex assistant nonce in text, got %q", text)
	}
	// The appended system prompt (developer role) must never surface as assistant text.
	if strings.Contains(text, "STATION_SECRET_MUST_NOT_LEAK") {
		t.Fatalf("developer/system-prompt text leaked into assistant_response: %q", text)
	}
	// exactly one normalized assistant line (the agent_message event_msg is NOT a second
	// source — response_item is the single source, so no duplicate).
	nAssistant := 0
	for _, l := range lines {
		if l["type"] == "assistant" {
			nAssistant++
		}
	}
	if nAssistant != 1 {
		t.Fatalf("expected exactly 1 normalized assistant line, got %d (duplicate source?)", nAssistant)
	}
}

// TestCodexRollout_MultiBlockJoined: a codex assistant message with several output_text
// blocks is concatenated into one assistant text (mirrors claude's per-block model).
func TestCodexRollout_MultiBlockJoined(t *testing.T) {
	line := map[string]interface{}{
		"type": "response_item",
		"payload": map[string]interface{}{
			"type": "message",
			"role": "assistant",
			"content": []interface{}{
				map[string]interface{}{"type": "output_text", "text": "part-one "},
				map[string]interface{}{"type": "output_text", "text": "part-two"},
			},
		},
	}
	norm, ok := normalizeCodexRolloutLine(line)
	if !ok {
		t.Fatal("expected the assistant message to normalize")
	}
	got := extractAssistantText([]map[string]interface{}{norm})
	if got != "part-one part-two" {
		t.Fatalf("expected joined blocks, got %q", got)
	}
}

// TestCodexRollout_NonAssistantDropped: developer/user messages and non-message items are
// dropped by the normalizer (tool events come from hooks, not the transcript).
func TestCodexRollout_NonAssistantDropped(t *testing.T) {
	drop := []string{codexDeveloperMsg, codexUserMsg, codexTokenCount, codexTaskComplete, codexSessionMeta}
	for _, raw := range drop {
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &entry); err != nil {
			t.Fatalf("seed unmarshal: %v", err)
		}
		if _, ok := normalizeCodexRolloutLine(entry); ok {
			t.Errorf("line should have been dropped but normalized: %s", raw)
		}
	}
}
