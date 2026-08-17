package mcp

import (
	"net/url"
	"strings"
)

type autoGitCredentialPlan struct {
	remoteURL                string
	isolateCredentialHelpers bool
}

func planAutoGitCredentials(rawOrigin, commitBackURL, credentialKind, commitBackToken, legacyToken string) autoGitCredentialPlan {
	plan := autoGitCredentialPlan{remoteURL: rawOrigin}

	switch credentialKind {
	case "runner":
		// The runner's credential socket/askpass owns external repository auth.
		return plan
	case "agentics-repo":
		if commitBackURL == "" || commitBackToken == "" {
			return plan
		}
		if authenticatedURL, ok := gitURLWithToken(commitBackURL, commitBackToken); ok {
			plan.remoteURL = authenticatedURL
			plan.isolateCredentialHelpers = true
		}
		return plan
	}

	// Backward compatibility for old jobs. Never put the legacy Agentics token
	// into an external origin; that was the bug that broke GitHub auto-push.
	if legacyToken != "" && isAgenticsGitOrigin(rawOrigin) {
		if authenticatedURL, ok := gitURLWithToken(rawOrigin, legacyToken); ok {
			plan.remoteURL = authenticatedURL
			plan.isolateCredentialHelpers = true
		}
	}
	return plan
}

func gitURLWithToken(rawURL, token string) (string, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return rawURL, false
	}
	parsed.User = url.UserPassword("x-access-token", token)
	return parsed.String(), true
}

func isAgenticsGitOrigin(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "git.agentics.dk" ||
		host == "agent-git" ||
		host == "localhost" ||
		host == "127.0.0.1"
}
