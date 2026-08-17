package auth

import (
	"os"
	"testing"
)

func TestGetStreamingTokenUsesRunnerCredentialInJobMode(t *testing.T) {
	t.Setenv("VIBECAST_HOME", t.TempDir())
	t.Setenv("AGENTICS_JOB_MODE", "1")
	t.Setenv("AGENTICS_TOKEN", "rnt_project_scoped")

	token, claims, err := GetStreamingToken()
	if err != nil {
		t.Fatalf("GetStreamingToken returned error: %v", err)
	}
	if token != "rnt_project_scoped" {
		t.Fatalf("token = %q, want runner token", token)
	}
	if claims != nil {
		t.Fatalf("claims = %#v, want nil for runner token", claims)
	}
}

func TestGetStreamingTokenDoesNotUseRunnerCredentialOutsideJobMode(t *testing.T) {
	t.Setenv("VIBECAST_HOME", t.TempDir())
	t.Setenv("AGENTICS_JOB_MODE", "")
	t.Setenv("AGENTICS_TOKEN", "rnt_project_scoped")

	token, claims, err := GetStreamingToken()
	if err == nil {
		t.Fatal("GetStreamingToken returned no error without saved interactive auth")
	}
	if token != "" || claims != nil {
		t.Fatalf("got token=%q claims=%#v, want empty result", token, claims)
	}

	if _, statErr := os.Stat(authFilePath()); !os.IsNotExist(statErr) {
		t.Fatalf("unexpected auth file state: %v", statErr)
	}
}
