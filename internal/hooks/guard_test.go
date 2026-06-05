package hooks

import "testing"

// TestDangerousProcessKill locks in the PreToolUse guard's behaviour: broad
// process kills (which in a shared broadcast container also match the agent's
// own Claude process and the broadcaster) are blocked, while PID-targeted kills
// and read-only pgrep are allowed.
func TestDangerousProcessKill(t *testing.T) {
	block := []string{
		`pkill -f "aspire run"`,
		`pkill -f "aspire run" 2>&1; sleep 1; pkill -f "dotnet run"; pkill -f "node.*next"; echo cleaned`,
		`pkill -f "DronePoul"`,
		`pkill node`,
		`killall node`,
		`cd /x && killall -9 dotnet`,
		`kill -- -12345`,
		`kill -9 -1`,
		`kill -1`,
	}
	allow := []string{
		`kill 12345`,
		`kill -9 12345`,
		`kill -TERM "$(cat /tmp/app.pid)"`,
		`pkill -F /tmp/app.pid`,
		`pgrep -af aspire`,
		`npm run build`,
		`aspire run --project src/apphost`,
		`git commit -m "stop the runaway loop"`,
	}
	for _, c := range block {
		if bad, _ := dangerousProcessKill(c); !bad {
			t.Errorf("SHOULD BLOCK but allowed: %q", c)
		}
	}
	for _, c := range allow {
		if bad, f := dangerousProcessKill(c); bad {
			t.Errorf("SHOULD ALLOW but blocked (%s): %q", f, c)
		}
	}
}
