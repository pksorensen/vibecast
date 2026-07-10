package hooks

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestRecordGuardDeny locks in the C08 deny-ledger contract: one JSON line per deny
// under $VIBECAST_HOME/guard-denials/<streamId>.jsonl, carrying enough to identify
// the blocked intent. A denied PreToolUse leaves no PostToolUse and nothing on the
// wire, so this ledger is the only durable evidence the guard fired.
func TestRecordGuardDeny(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIBECAST_HOME", home)

	const streamID = "stream-guard-1"
	recordGuardDeny(streamID, "process-kill", "Bash", "pkill -f conformance-sentinel", "pkill -f", "blocked: broad process kill")
	recordGuardDeny(streamID, "process-kill", "Bash", "killall node", "killall", "blocked: broad process kill")

	data, err := os.ReadFile(guardDenialsPath(streamID))
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 deny lines, got %d: %q", len(lines), string(data))
	}

	var rec map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("first line is not valid JSON: %v", err)
	}
	for _, k := range []string{"streamId", "timestamp", "rule", "tool", "command", "form", "reason"} {
		if _, ok := rec[k]; !ok {
			t.Errorf("deny record missing key %q: %v", k, rec)
		}
	}
	if rec["streamId"] != streamID {
		t.Errorf("streamId = %v, want %s", rec["streamId"], streamID)
	}
	if cmd, _ := rec["command"].(string); !strings.Contains(cmd, "conformance-sentinel") {
		t.Errorf("command not preserved: %v", rec["command"])
	}
}

// TestRecordGuardDeny_EmptyStreamNoop: a deny with no resolvable session must not
// create a file or panic — the block itself already happened; the ledger is optional.
func TestRecordGuardDeny_EmptyStreamNoop(t *testing.T) {
	t.Setenv("VIBECAST_HOME", t.TempDir())
	recordGuardDeny("", "process-kill", "Bash", "pkill -f x", "pkill -f", "blocked")
	if _, err := os.Stat(guardDenialsDir()); !os.IsNotExist(err) {
		t.Fatalf("expected no guard-denials dir for empty streamID, stat err=%v", err)
	}
}
