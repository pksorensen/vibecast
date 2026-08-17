package mcp

import "testing"

func TestPlanAutoGitCredentialsPreservesRunnerCredentialsForGitHub(t *testing.T) {
	origin := "https://github.com/KjeldagerIO/arvo-museliving.git"
	plan := planAutoGitCredentials(origin, origin, "runner", "", "internal-token")

	if plan.remoteURL != origin {
		t.Fatalf("remote URL changed: %q", plan.remoteURL)
	}
	if plan.isolateCredentialHelpers {
		t.Fatal("runner credential helpers must be preserved")
	}
}

func TestPlanAutoGitCredentialsUsesExplicitAgenticsDestination(t *testing.T) {
	plan := planAutoGitCredentials(
		"https://old.example/repo.git",
		"https://git.agentics.dk/p/museliving/repo.git",
		"agentics-repo",
		"repo-token",
		"",
	)

	want := "https://x-access-token:repo-token@git.agentics.dk/p/museliving/repo.git"
	if plan.remoteURL != want {
		t.Fatalf("remote URL = %q, want %q", plan.remoteURL, want)
	}
	if !plan.isolateCredentialHelpers {
		t.Fatal("embedded Agentics credentials should be isolated from askpass helpers")
	}
}

func TestPlanAutoGitCredentialsDoesNotApplyLegacyTokenToGitHub(t *testing.T) {
	origin := "https://github.com/example/repo.git"
	plan := planAutoGitCredentials(origin, "", "", "", "legacy-agentics-token")

	if plan.remoteURL != origin || plan.isolateCredentialHelpers {
		t.Fatalf("legacy token affected external origin: %+v", plan)
	}
}
