package hooks

import "testing"

func TestAutoGitBlockCounter_RoundTripAndReset(t *testing.T) {
	t.Setenv("VIBECAST_HOME", t.TempDir())
	const id = "sess-autogit"
	if n := readAutoGitBlockCount(id); n != 0 {
		t.Fatalf("fresh count = %d, want 0", n)
	}
	for i := 1; i <= maxAutoGitStopBlocks; i++ {
		writeAutoGitBlockCount(id, i)
		if n := readAutoGitBlockCount(id); n != i {
			t.Fatalf("count = %d, want %d", n, i)
		}
	}
	// The stop-enforcement counter is a separate budget.
	if n := readStopBlockCount(id); n != 0 {
		t.Fatalf("stop_blocks leaked from autogit counter: %d", n)
	}
	resetAutoGitBlockCount(id)
	if n := readAutoGitBlockCount(id); n != 0 {
		t.Fatalf("after reset = %d, want 0", n)
	}
}
