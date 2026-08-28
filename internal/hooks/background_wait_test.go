package hooks

import "testing"

// TestParseStopBackgroundWorkPresence locks the nil-vs-empty distinction, which is
// the whole safety property of the park: an absent background_tasks key means the
// agent cannot tell us (codex, pi, old Claude Code) and must keep the legacy
// pane-scrape path, while an explicit empty array means the turn is genuinely the
// last one and every stop gate should run.
func TestParseStopBackgroundWorkPresence(t *testing.T) {
	cases := []struct {
		name       string
		payload    string
		wantPresnt bool
		wantTasks  int
		wantCrons  int
	}{
		{"absent", `{"session_id":"s","stop_hook_active":false}`, false, 0, 0},
		{"empty", `{"background_tasks":[],"session_crons":[]}`, true, 0, 0},
		{
			"running shell",
			`{"background_tasks":[{"id":"bx1","type":"shell","status":"running","description":"Sleep 300","command":"sleep 300"}],"session_crons":[]}`,
			true, 1, 0,
		},
		{
			"completed only",
			`{"background_tasks":[{"id":"bx1","type":"shell","status":"completed","description":"done"}],"session_crons":[]}`,
			true, 0, 0,
		},
		{
			"mixed",
			`{"background_tasks":[{"id":"a","type":"monitor","status":"running"},{"id":"b","type":"shell","status":"failed"}]}`,
			true, 1, 0,
		},
		{
			"cron only",
			`{"background_tasks":[],"session_crons":[{"id":"c1","description":"loop tick"}]}`,
			true, 0, 1,
		},
		{"unparseable", `not json`, false, 0, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tasks, crons, present := parseStopBackgroundWork([]byte(tc.payload))
			if present != tc.wantPresnt {
				t.Errorf("present = %v, want %v", present, tc.wantPresnt)
			}
			if len(tasks) != tc.wantTasks {
				t.Errorf("tasks = %d, want %d", len(tasks), tc.wantTasks)
			}
			if len(crons) != tc.wantCrons {
				t.Errorf("crons = %d, want %d", len(crons), tc.wantCrons)
			}
		})
	}
}

// TestBackgroundTaskFinished pins the unknown-status default: anything we do not
// recognise counts as in flight, because a needless park costs one idle hold and a
// missed park costs the job.
func TestBackgroundTaskFinished(t *testing.T) {
	for _, s := range []string{"completed", "Complete", "FAILED", "error", "cancelled", " killed "} {
		if !backgroundTaskFinished(s) {
			t.Errorf("%q: want finished", s)
		}
	}
	for _, s := range []string{"running", "pending", "queued", "", "starting", "whatever"} {
		if backgroundTaskFinished(s) {
			t.Errorf("%q: want in flight", s)
		}
	}
}

func TestBackgroundWaitHoldMinutes(t *testing.T) {
	t.Setenv("AGENTICS_BACKGROUND_WAIT_HOLD_MINUTES", "")
	if got := backgroundWaitHoldMinutes(); got != 30 {
		t.Errorf("unset: got %d, want 30", got)
	}
	t.Setenv("AGENTICS_BACKGROUND_WAIT_HOLD_MINUTES", "45")
	if got := backgroundWaitHoldMinutes(); got != 45 {
		t.Errorf("45: got %d", got)
	}
	for _, bad := range []string{"0", "-5", "abc"} {
		t.Setenv("AGENTICS_BACKGROUND_WAIT_HOLD_MINUTES", bad)
		if got := backgroundWaitHoldMinutes(); got != 30 {
			t.Errorf("%q: got %d, want fallback 30", bad, got)
		}
	}
}
