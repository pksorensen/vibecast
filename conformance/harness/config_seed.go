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
	case "pi":
		return preparePiConfig(baseDir, workspace, vibecastBin)
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

	// Register the vibecast MCP server so codex can call stop_broadcast (the job-completion
	// conclusion path — C07). Unlike claude (which inherits the pane env for its plugin MCP),
	// codex sanitizes the MCP subprocess env, so VIBECAST_HOME must be forwarded explicitly.
	// This mirrors what vibecast's launch path will write into codex's config in production;
	// VIBECAST_HOME is the same baseDir/home Launch exports to the pane.
	if vibecastBin != "" {
		vibecastHome := filepath.Join(baseDir, "home")
		mcpEnv := map[string]string{"VIBECAST_HOME": vibecastHome}
		toml.WriteString(agent.CodexMCPServersTOML(vibecastBin, mcpEnv))
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

// preparePiConfig builds an isolated PI_CODING_AGENT_DIR under baseDir and writes vibecast's pi
// hook-bridge extension into its extensions/ dir, where pi auto-discovers it. pi 0.73.1 has no
// first-run trust/onboarding dialog (goes straight to the editor), so — unlike claude/codex —
// there is nothing to pre-trust; the isolation is purely so the run never touches the real
// ~/.pi/agent (auth, sessions, settings) and startup is deterministic.
//
// The returned env reaches the pi process (and thus the extension): PI_CODING_AGENT_DIR relocates
// pi's whole config home; VIBECAST_BIN tells the extension which binary to exec for `vibecast hook
// <sub>`; PI_SKIP_VERSION_CHECK + PI_OFFLINE keep startup free of network (version check + fd/ripgrep
// helper downloads) without affecting the model call (verified: `pi --offline` still reached the
// Foundry model). Real-mode model wiring (models.json → pks-foundry proxy) lands with the scenarios
// that need a model response; C01/C02 registration fire at launch and need none.
func preparePiConfig(baseDir, workspace, vibecastBin string) (map[string]string, error) {
	cfgDir := filepath.Join(baseDir, "pi-home")
	extDir := filepath.Join(cfgDir, "extensions")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		return nil, err
	}

	// Auto-discovered hook bridge: $PI_CODING_AGENT_DIR/extensions/vibecast.ts (embedded in the
	// vibecast binary). Requires the binary path so the extension can exec `vibecast hook`.
	if vibecastBin != "" {
		if err := os.WriteFile(filepath.Join(extDir, agent.PiExtensionFileName), agent.PiExtensionTS(), 0o644); err != nil {
			return nil, err
		}
	}

	env := map[string]string{
		"PI_CODING_AGENT_DIR":   cfgDir,
		"PI_SKIP_VERSION_CHECK": "1",
		"PI_OFFLINE":            "1",
	}
	if vibecastBin != "" {
		env["VIBECAST_BIN"] = vibecastBin
	}

	// Real-mode model: when a Foundry proxy is running (StartFoundryProxy), wire pi's pks-foundry
	// provider at its endpoint and make it the default so pi uses Foundry gpt-5.5 with no --model
	// flag. models.json declares the provider (OpenAI Responses API + Bearer proxy-token via
	// authHeader); settings.json defaults to it. FOUNDRY_PROXY_TOKEN is resolved by pi from the
	// env (apiKey field names the var). Proven standalone: `pi --model pks-foundry/gpt-5.5` returns
	// a Foundry response through this proxy. With no proxy, pi launches keyless (C01/C02 only).
	if piFoundryURL != "" {
		// api=openai-completions (Chat Completions), NOT openai-responses. gpt-5.5 is a reasoning
		// model and Azure Foundry's Responses API only persists reasoning items when store=true —
		// but pi's Responses client hardcodes store=false (its supportsStore compat flag, confusingly,
		// only ever sets store=false). So any follow-up turn that references a prior reasoning item
		// 400s: "Items are not persisted when store is set to false." C08's blocked pkill forces
		// exactly that second turn. Chat Completions is stateless by design (no server-side
		// reasoning-item persistence to reference), so it avoids the issue entirely — verified it
		// handles the multi-turn tool path against Foundry gpt-5.5.
		modelsJSON := fmt.Sprintf(`{
  "providers": {
    "pks-foundry": {
      "baseUrl": %q,
      "api": "openai-completions",
      "apiKey": "FOUNDRY_PROXY_TOKEN",
      "authHeader": true,
      "models": [ { "id": "gpt-5.5" } ]
    }
  }
}
`, piFoundryURL+"/openai/v1")
		if err := os.WriteFile(filepath.Join(cfgDir, "models.json"), []byte(modelsJSON), 0o600); err != nil {
			return nil, err
		}
		settingsJSON := `{
  "defaultProvider": "pks-foundry",
  "defaultModel": "gpt-5.5"
}
`
		if err := os.WriteFile(filepath.Join(cfgDir, "settings.json"), []byte(settingsJSON), 0o600); err != nil {
			return nil, err
		}
		env["FOUNDRY_PROXY_TOKEN"] = piFoundryToken
	}

	return env, nil
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
