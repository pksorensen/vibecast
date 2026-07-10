package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pksorensen/vibecast/internal/agent"
)

// envWithOverrides returns base ("KEY=VAL" slice) with every key in overrides replaced or
// appended. Used to inject the agent config-home env into the tmux server's environment.
func envWithOverrides(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(overrides))
	for _, kv := range base {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if _, replaced := overrides[key]; !replaced {
			out = append(out, kv)
		}
	}
	for k, v := range overrides {
		out = append(out, k+"="+v)
	}
	return out
}

// prepareAgentConfig seeds an isolated, pre-trusted config home for the agent and returns
// the env that points the agent at it. The goal is that launching the agent in a fresh
// throwaway workspace does NOT block on a first-run trust/onboarding dialog — the non-job
// conformance scenarios don't run vibecast's job-mode auto-answers, so the agent would sit
// forever at "Quick safety check: Is this a project you created or one you trust?".
//
// It copies the user's real credentials into the throwaway home so the agent stays logged
// in, and never modifies the real config. Agents without seeding support yet (codex/pi land
// with their adapters) return nil env — their scenarios will pre-trust another way.
func prepareAgentConfig(agent, baseDir, workspace, vibecastBin string) (map[string]string, error) {
	switch agent {
	case "claude":
		return prepareClaudeConfig(baseDir, workspace)
	case "codex":
		return prepareCodexConfig(baseDir, workspace, vibecastBin)
	default:
		return nil, nil
	}
}

// prepareCodexConfig builds an isolated CODEX_HOME under baseDir that is a copy of the real
// ~/.codex (so codex stays logged in and inherits the fully-onboarded config: model
// providers, completed first-run markers), with the throwaway workspace added as a trusted
// project so the hooks layer loads and no folder-trust prompt can block. Real user files are
// only read, never written.
//
// CODEX_HOME relocates the entire codex config dir (auth + config), so both auth.json and
// config.toml are copied verbatim. Absence of auth.json is tolerated (env-key/model-provider
// auth may cover it) — the trust seed is the load-bearing part.
//
// It also writes a hooks.json into the isolated CODEX_HOME (via agent.CodexHooksJSON, the
// same wiring vibecast will ship in production) so codex fires the vibecast lifecycle hooks —
// most importantly SessionStart, which drives the discover-identity flow. The codex adapter
// launches with --dangerously-bypass-hook-trust so these load without the interactive gate.
func prepareCodexConfig(baseDir, workspace, vibecastBin string) (map[string]string, error) {
	realDir := os.Getenv("CODEX_HOME")
	if realDir == "" {
		realDir = filepath.Join(os.Getenv("HOME"), ".codex")
	}
	cfgDir := filepath.Join(baseDir, "codex-home")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		return nil, err
	}

	// Copy auth.json (0600) so `codex login status` stays green against the same account.
	if auth, err := os.ReadFile(filepath.Join(realDir, "auth.json")); err == nil {
		if err := os.WriteFile(filepath.Join(cfgDir, "auth.json"), auth, 0o600); err != nil {
			return nil, err
		}
	}

	// Copy config.toml verbatim (inherits model_providers + onboarding state), then append a
	// trust entry for the throwaway workspace. A section header — not a bare dotted key at EOF
	// — is required: a trailing `projects."x".trust_level` would inherit the last table's
	// context (e.g. land under [model_providers.pks-foundry]). Distinct `[projects."path"]`
	// sub-tables don't collide with any existing project trust entry.
	var toml strings.Builder
	if base, err := os.ReadFile(filepath.Join(realDir, "config.toml")); err == nil {
		toml.Write(base)
		if !strings.HasSuffix(toml.String(), "\n") {
			toml.WriteByte('\n')
		}
	}
	trustPaths := map[string]bool{workspace: true}
	if rp, err := filepath.EvalSymlinks(workspace); err == nil {
		trustPaths[rp] = true
	}
	for p := range trustPaths {
		// TOML basic-string escaping for the quoted key: backslash and double-quote.
		esc := strings.ReplaceAll(p, `\`, `\\`)
		esc = strings.ReplaceAll(esc, `"`, `\"`)
		fmt.Fprintf(&toml, "\n[projects.\"%s\"]\ntrust_level = \"trusted\"\n", esc)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(toml.String()), 0o600); err != nil {
		return nil, err
	}

	// Wire vibecast's lifecycle hooks. Written into CODEX_HOME/hooks.json — the config-layer
	// location codex loads regardless of workspace trust. The absolute vibecast path is baked
	// in (CodexHooksJSON), so the hook subprocess resolves without PATH assumptions.
	if vibecastBin != "" {
		if err := os.WriteFile(filepath.Join(cfgDir, "hooks.json"), agent.CodexHooksJSON(vibecastBin), 0o600); err != nil {
			return nil, err
		}
	}

	return map[string]string{"CODEX_HOME": cfgDir}, nil
}

// prepareClaudeConfig builds an isolated CLAUDE_CONFIG_DIR under baseDir that is a copy of
// the real one (so onboarding is already complete and the OAuth account matches the copied
// credentials), with the throwaway workspace added to the per-project `projects` map as
// trusted. Real user files are only read, never written.
func prepareClaudeConfig(baseDir, workspace string) (map[string]string, error) {
	realDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if realDir == "" {
		realDir = filepath.Join(os.Getenv("HOME"), ".claude")
	}
	cfgDir := filepath.Join(baseDir, "claude-config")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		return nil, err
	}

	// Locate the real .claude.json. With CLAUDE_CONFIG_DIR set it lives inside that dir;
	// otherwise Claude keeps it at ~/.claude.json.
	realJSON := filepath.Join(realDir, ".claude.json")
	if _, err := os.Stat(realJSON); err != nil {
		alt := filepath.Join(os.Getenv("HOME"), ".claude.json")
		if _, err2 := os.Stat(alt); err2 != nil {
			return nil, fmt.Errorf("real .claude.json not found (looked in %s and %s); is Claude logged in?", realJSON, alt)
		}
		realJSON = alt
	}
	raw, err := os.ReadFile(realJSON)
	if err != nil {
		return nil, err
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", realJSON, err)
	}

	// vibecast launches claude with --dangerously-skip-permissions, which triggers a one-time
	// "Bypass Permissions mode" acceptance dialog. It's a persistent global flag, but the real
	// config lacks it (this machine never ran bypass mode), so pre-accept it here — the config
	// analog of the job-mode auto-answer, so non-job scenarios don't stall at the dialog.
	cfg["bypassPermissionsModeAccepted"] = true

	// Trust the throwaway workspace so no "Quick safety check" dialog blocks the session.
	// Claude keys trust by the project's absolute cwd; add the symlink-resolved path too so
	// a /tmp → /private/tmp style indirection can't miss.
	projects, _ := cfg["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
	}
	trustKeys := map[string]bool{workspace: true}
	if rp, err := filepath.EvalSymlinks(workspace); err == nil {
		trustKeys[rp] = true
	}
	for k := range trustKeys {
		projects[k] = map[string]any{
			"hasTrustDialogAccepted":        true,
			"hasCompletedProjectOnboarding": true,
			"projectOnboardingSeenCount":    1,
		}
	}
	cfg["projects"] = projects

	out, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(cfgDir, ".claude.json"), out, 0o600); err != nil {
		return nil, err
	}

	// The --dangerously-skip-permissions disclaimer is gated by mG(), which returns true when
	// skipDangerousModePermissionPrompt is set in any settings layer (the sandbox/CI escape
	// hatch) — that short-circuits before the persisted-acceptance flag. Write it into the
	// isolated userSettings (<CLAUDE_CONFIG_DIR>/settings.json) so the disclaimer never shows.
	settings, err := json.Marshal(map[string]any{"skipDangerousModePermissionPrompt": true})
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "settings.json"), settings, 0o600); err != nil {
		return nil, err
	}

	// Copy credentials so the agent stays logged in against the same account. Absence is
	// tolerated (e.g. env-var-based auth) — the trust seed above is the load-bearing part.
	if creds, err := os.ReadFile(filepath.Join(realDir, ".credentials.json")); err == nil {
		if err := os.WriteFile(filepath.Join(cfgDir, ".credentials.json"), creds, 0o600); err != nil {
			return nil, err
		}
	}

	return map[string]string{"CLAUDE_CONFIG_DIR": cfgDir}, nil
}
