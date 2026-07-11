package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCodexPrepareFreshSeed verifies the production seed builds an isolated CODEX_HOME that
// copies the operator's auth + config and appends vibecast's trust, MCP server, and hooks —
// the production analog of the harness's host-side prepareCodexConfig.
func TestCodexPrepareFreshSeed(t *testing.T) {
	realDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(realDir, "auth.json"), []byte(`{"OPENAI_API_KEY":"sk-test-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "config.toml"), []byte("[model_providers.pks-foundry]\nname = \"foundry\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", realDir)

	base := t.TempDir()
	ws := t.TempDir()
	env, err := codexAdapter{}.Prepare(PrepareInput{
		BaseDir:      base,
		Workspace:    ws,
		VibecastBin:  "/usr/local/bin/vibecast",
		VibecastHome: "/run/vibecast-home",
	})
	if err != nil {
		t.Fatal(err)
	}

	got := env["CODEX_HOME"]
	want := filepath.Join(base, "codex-home")
	if got != want {
		t.Fatalf("CODEX_HOME = %q, want %q", got, want)
	}

	// auth.json copied verbatim.
	if b, err := os.ReadFile(filepath.Join(got, "auth.json")); err != nil || !strings.Contains(string(b), "sk-test-token") {
		t.Errorf("auth.json not copied (err=%v)", err)
	}

	// config.toml = base content + trust entry + vibecast MCP with VIBECAST_HOME forwarding.
	cfg, err := os.ReadFile(filepath.Join(got, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfgStr := string(cfg)
	for _, sub := range []string{
		"[model_providers.pks-foundry]", // operator config inherited
		`trust_level = "trusted"`,        // workspace trusted
		"[mcp_servers.vibecast]",         // MCP server registered
		"VIBECAST_HOME",                  // env forwarding key
		"/run/vibecast-home",             // env forwarding value
		`args = ["mcp", "serve"]`,        // MCP command args
		"default_tools_approval_mode",    // vibecast control tools auto-approved
	} {
		if !strings.Contains(cfgStr, sub) {
			t.Errorf("config.toml missing %q\n---\n%s", sub, cfgStr)
		}
	}
	// Workspace trust entry present (raw or symlink-resolved path).
	wsResolved, _ := filepath.EvalSymlinks(ws)
	if !strings.Contains(cfgStr, ws) && (wsResolved == "" || !strings.Contains(cfgStr, wsResolved)) {
		t.Errorf("config.toml missing workspace trust path %q", ws)
	}
	// Exactly one MCP entry — no double-append.
	if n := strings.Count(cfgStr, "[mcp_servers.vibecast]"); n != 1 {
		t.Errorf("[mcp_servers.vibecast] appears %d times, want 1", n)
	}

	// hooks.json wires all five lifecycle events.
	h, err := os.ReadFile(filepath.Join(got, "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"} {
		if !strings.Contains(string(h), ev) {
			t.Errorf("hooks.json missing %s event", ev)
		}
	}
}

// TestCodexPrepareReusesSeededHome verifies the idempotency / conformance-safety guard: when
// CODEX_HOME already carries vibecast's MCP wiring (harness host-side seed, or a same-process
// restart), Prepare returns it unchanged and does NOT build a second home. This is what keeps
// the 33/33-green codex conformance harness unperturbed.
func TestCodexPrepareReusesSeededHome(t *testing.T) {
	seeded := t.TempDir()
	if err := os.WriteFile(filepath.Join(seeded, "config.toml"), []byte("[mcp_servers.vibecast]\ncommand = \"/x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", seeded)

	base := t.TempDir()
	env, err := codexAdapter{}.Prepare(PrepareInput{
		BaseDir:      base,
		Workspace:    t.TempDir(),
		VibecastBin:  "/usr/local/bin/vibecast",
		VibecastHome: "/run/vibecast-home",
	})
	if err != nil {
		t.Fatal(err)
	}
	if env["CODEX_HOME"] != seeded {
		t.Fatalf("expected reuse of %q, got %q", seeded, env["CODEX_HOME"])
	}
	if _, err := os.Stat(filepath.Join(base, "codex-home")); !os.IsNotExist(err) {
		t.Errorf("Prepare built a fresh seed under BaseDir despite an already-seeded CODEX_HOME")
	}
}

// TestCodexPrepareToleratesMissingAuth verifies a real codex dir with no auth.json still seeds
// (env-key auth may cover it) — the trust + MCP + hooks are the load-bearing parts.
func TestCodexPrepareToleratesMissingAuth(t *testing.T) {
	realDir := t.TempDir() // no auth.json, no config.toml
	t.Setenv("CODEX_HOME", realDir)

	base := t.TempDir()
	env, err := codexAdapter{}.Prepare(PrepareInput{
		BaseDir:      base,
		Workspace:    t.TempDir(),
		VibecastBin:  "/usr/local/bin/vibecast",
		VibecastHome: "/run/vibecast-home",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := env["CODEX_HOME"]
	if _, err := os.Stat(filepath.Join(got, "auth.json")); !os.IsNotExist(err) {
		t.Errorf("auth.json should be absent when the real dir has none")
	}
	cfg, err := os.ReadFile(filepath.Join(got, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "[mcp_servers.vibecast]") {
		t.Error("config.toml missing [mcp_servers.vibecast] even without base config")
	}
}

// TestClaudePiPrepareNoop verifies the non-seeding adapters return no env (claude's hooks ride
// in --plugin-dir; pi production seeding is deferred).
func TestClaudePiPrepareNoop(t *testing.T) {
	for _, ad := range []Adapter{claudeAdapter{}, piAdapter{}} {
		env, err := ad.Prepare(PrepareInput{BaseDir: t.TempDir()})
		if err != nil {
			t.Errorf("%s Prepare returned error: %v", ad.Kind(), err)
		}
		if env != nil {
			t.Errorf("%s Prepare env = %v, want nil", ad.Kind(), env)
		}
	}
}
