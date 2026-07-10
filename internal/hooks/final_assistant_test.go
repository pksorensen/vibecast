package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// appendLine appends one NDJSON line (with trailing newline) to path.
func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
}

const assistantLine = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"CONFORMdeadbeef"}],"usage":{"input_tokens":12,"output_tokens":7}}}`

// TestWaitForFinalAssistant_LateFlush reproduces the Claude Code Stop-hook race:
// at Stop time the transcript holds only the user prompt (plus state markers), and
// the final assistant message is flushed a moment later. waitForFinalAssistant must
// still return the late assistant text and usage.
func TestWaitForFinalAssistant_LateFlush(t *testing.T) {
	t.Setenv("VIBECAST_HOME", t.TempDir())

	tp := filepath.Join(t.TempDir(), "transcript.jsonl")
	seed := strings.Join([]string{
		`{"type":"mode"}`,
		`{"type":"permission-mode"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"say the nonce"}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(tp, []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}

	// Simulate Claude flushing the assistant line ~120ms after Stop fires.
	go func() {
		time.Sleep(120 * time.Millisecond)
		appendLine(t, tp, assistantLine)
	}()

	lines, text, usage := waitForFinalAssistant("stream-late", tp, 2*time.Second)

	if !strings.Contains(text, "CONFORMdeadbeef") {
		t.Fatalf("expected assistant nonce in text, got %q", text)
	}
	if usage == nil {
		t.Fatal("expected usage to be extracted from the late assistant line")
	}
	if usage["output_tokens"] != 7 {
		t.Fatalf("expected output_tokens=7, got %v", usage["output_tokens"])
	}
	// The accumulated lines must include the user prompt and the late assistant line.
	if len(lines) < 2 {
		t.Fatalf("expected >=2 accumulated lines (user + assistant), got %d", len(lines))
	}
}

// TestWaitForFinalAssistant_AlreadyPresent: when the assistant line is already in the
// transcript at Stop time, it must return immediately without waiting.
func TestWaitForFinalAssistant_AlreadyPresent(t *testing.T) {
	t.Setenv("VIBECAST_HOME", t.TempDir())

	tp := filepath.Join(t.TempDir(), "transcript.jsonl")
	seed := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}` + "\n" + assistantLine + "\n"
	if err := os.WriteFile(tp, []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, text, usage := waitForFinalAssistant("stream-present", tp, 2*time.Second)
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("returned in %v — should be near-immediate when assistant already present", elapsed)
	}
	if !strings.Contains(text, "CONFORMdeadbeef") {
		t.Fatalf("expected assistant nonce, got %q", text)
	}
	if usage == nil {
		t.Fatal("expected usage")
	}
}

// TestWaitForFinalAssistant_NoAssistant: a turn that never produces an assistant line
// must return empty and honor the (bounded) deadline rather than hanging.
func TestWaitForFinalAssistant_NoAssistant(t *testing.T) {
	t.Setenv("VIBECAST_HOME", t.TempDir())

	tp := filepath.Join(t.TempDir(), "transcript.jsonl")
	seed := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}` + "\n"
	if err := os.WriteFile(tp, []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, text, usage := waitForFinalAssistant("stream-none", tp, 300*time.Millisecond)
	elapsed := time.Since(start)
	if text != "" || usage != nil {
		t.Fatalf("expected empty result, got text=%q usage=%v", text, usage)
	}
	if elapsed < 250*time.Millisecond {
		t.Fatalf("returned too early (%v) — should wait for the deadline", elapsed)
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("exceeded bounded wait (%v)", elapsed)
	}
}
