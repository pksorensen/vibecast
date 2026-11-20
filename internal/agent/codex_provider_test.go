package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func prepareCodexWith(t *testing.T, baseConfig string) string {
	t.Helper()
	real := t.TempDir()
	if baseConfig != "" {
		if err := os.WriteFile(filepath.Join(real, "config.toml"), []byte(baseConfig), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CODEX_HOME", real)
	env, err := codexAdapter{}.Prepare(PrepareInput{BaseDir: t.TempDir(), Workspace: "/workspaces/repo", VibecastBin: "/tmp/vibecast"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(filepath.Join(env["CODEX_HOME"], "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestCodexPrepare_BaseURLAddsEnvKeyProvider(t *testing.T) {
	t.Setenv("VIBECAST_CODEX_BASE_URL", "http://172.17.0.1:8822/openai/v1/")
	cfg := prepareCodexWith(t, "")
	if !strings.HasPrefix(cfg, `model_provider = "vibecast-env"`+"\n") {
		t.Fatalf("model_provider must be the first (top-level) line:\n%s", cfg)
	}
	for _, want := range []string{
		"[model_providers.vibecast-env]",
		`base_url = "http://172.17.0.1:8822/openai/v1"`,
		`env_key = "OPENAI_API_KEY"`,
		`wire_api = "responses"`,
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("missing %q in:\n%s", want, cfg)
		}
	}
	// The provider table must come before the trust/MCP tables it would otherwise swallow.
	if strings.Index(cfg, "[model_providers.vibecast-env]") > strings.Index(cfg, "[projects.") {
		t.Errorf("provider table after projects table:\n%s", cfg)
	}
}

func TestCodexPrepare_OperatorProviderWins(t *testing.T) {
	t.Setenv("VIBECAST_CODEX_BASE_URL", "http://example/openai/v1")
	cfg := prepareCodexWith(t, "model_provider = \"pks-foundry\"\n\n[model_providers.pks-foundry]\nbase_url = \"http://127.0.0.1:1/openai/v1\"\n")
	if strings.Contains(cfg, "vibecast-env") {
		t.Fatalf("operator's own provider must not be overridden:\n%s", cfg)
	}
}

func TestCodexPrepare_NoBaseURLNoProvider(t *testing.T) {
	t.Setenv("VIBECAST_CODEX_BASE_URL", "")
	if cfg := prepareCodexWith(t, ""); strings.Contains(cfg, "model_provider") {
		t.Fatalf("unexpected provider without VIBECAST_CODEX_BASE_URL:\n%s", cfg)
	}
}

func TestCodexMCP_ForwardsAutoGitEnvByName(t *testing.T) {
	t.Setenv("AGENTICS_TOKEN", "rnt_secret")
	t.Setenv("AGENTICS_AUTO_GIT", "1")
	t.Setenv("AGENTICS_COMMIT_BACK_TOKEN", "ghs_secret")
	t.Setenv("AGENTICS_COMMIT_BACK_URL", "")
	toml := CodexMCPServersTOML("/tmp/vibecast", nil)
	want := `env_vars = ["AGENTICS_TOKEN", "AGENTICS_AUTO_GIT", "AGENTICS_COMMIT_BACK_TOKEN"]`
	if !strings.Contains(toml, want) {
		t.Fatalf("want %s in:\n%s", want, toml)
	}
	if strings.Contains(toml, "ghs_secret") || strings.Contains(toml, "rnt_secret") {
		t.Fatalf("secret value leaked into config.toml:\n%s", toml)
	}
}
