package util

import (
	"bytes"
	"net/http"
	"os"
	"strings"
	"time"
)

// ServerEndpoint is a parsed AGENTICS_SERVER value.
//
// AGENTICS_SERVER has always been a bare "host[:port]", with the scheme chosen by
// IsLocalHost: localhost/127.0.0.1 => http/ws, anything else => https/wss. An embedded
// ALP server (the sandbox controller) is reached on a private address such as
// "http://172.17.0.1:8080", which that rule would send over TLS, so the value may now
// also carry an explicit scheme ("http://host:port", "https://host"). A bare value keeps
// the old rule unchanged.
type ServerEndpoint struct {
	// Scheme is "http" or "https".
	Scheme string
	// Host is host[:port] exactly as given (no default port is added).
	Host string
	// BasePath is an optional path prefix from a full URL, without a trailing slash
	// (e.g. "/agentics" for "https://example.test/agentics/"). Empty for bare values.
	BasePath string
	// Explicit is true when the value carried its own scheme.
	Explicit bool
}

// ParseServer parses an AGENTICS_SERVER value (bare host[:port] or a full URL).
func ParseServer(raw string) ServerEndpoint {
	s := strings.TrimSpace(raw)
	lower := strings.ToLower(s)
	for _, scheme := range []string{"http", "https"} {
		prefix := scheme + "://"
		if strings.HasPrefix(lower, prefix) {
			rest := s[len(prefix):]
			host, path := rest, ""
			if i := strings.IndexAny(rest, "/?#"); i >= 0 {
				host, path = rest[:i], rest[i:]
				// Only a path prefix is meaningful for a base URL; drop query/fragment.
				if j := strings.IndexAny(path, "?#"); j >= 0 {
					path = path[:j]
				}
			}
			return ServerEndpoint{
				Scheme:   scheme,
				Host:     host,
				BasePath: strings.TrimRight(path, "/"),
				Explicit: true,
			}
		}
	}
	scheme := "https"
	if IsLocalHost(s) {
		scheme = "http"
	}
	return ServerEndpoint{Scheme: scheme, Host: s}
}

// BaseURL returns scheme://host[:port][/basepath].
func (e ServerEndpoint) BaseURL() string {
	return e.Scheme + "://" + e.Host + e.BasePath
}

// URL joins path (which must start with "/") onto the base URL.
func (e ServerEndpoint) URL(path string) string {
	return e.BaseURL() + path
}

// TLS reports whether the WebSocket dial must use TLS.
func (e ServerEndpoint) TLS() bool {
	return e.Scheme == "https"
}

// DialAddr returns the TCP address to dial for a WebSocket. A bare non-local host
// without a port gets :443 (today's behaviour); an explicit URL gets its scheme's
// default port when it names none.
func (e ServerEndpoint) DialAddr() string {
	if hasPort(e.Host) {
		return e.Host
	}
	if e.Scheme == "https" {
		return e.Host + ":443"
	}
	if e.Explicit {
		return e.Host + ":80"
	}
	// Bare local host without a port (e.g. "localhost") — today's code dials it as is.
	return e.Host
}

// Hostname returns the host without port (for TLS ServerName and display URLs).
func (e ServerEndpoint) Hostname() string {
	h := e.Host
	if strings.HasPrefix(h, "[") {
		if i := strings.Index(h, "]"); i >= 0 {
			return h[:i+1]
		}
	}
	return strings.Split(h, ":")[0]
}

func hasPort(host string) bool {
	if strings.HasPrefix(host, "[") {
		return strings.Contains(host, "]:")
	}
	return strings.Contains(host, ":")
}

// ServerURL builds an absolute URL for path on the server named by an AGENTICS_SERVER
// value (bare or full URL). Equivalent to the historical
// fmt.Sprintf("%s://%s%s", IsLocalHost ? "http" : "https", host, path) for bare values.
func ServerURL(server, path string) string {
	return ParseServer(server).URL(path)
}

// ServerHostPort returns just host[:port] for an AGENTICS_SERVER value, stripping any
// scheme and path. For a bare value it is the value itself.
func ServerHostPort(server string) string {
	return ParseServer(server).Host
}

// AgenticsToken returns the runner's bearer token (AGENTICS_TOKEN), trimmed, or "".
func AgenticsToken() string {
	return strings.TrimSpace(os.Getenv("AGENTICS_TOKEN"))
}

// ApplyAgenticsToken sets "Authorization: Bearer $AGENTICS_TOKEN" on req when the env
// var is set and reports whether it did. Every call vibecast makes to /api/lives/* goes
// through this so an embedded ALP server can bind the call to the runner's job token.
func ApplyAgenticsToken(req *http.Request) bool {
	if t := AgenticsToken(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
		return true
	}
	return false
}

// BroadcastEnabled reports whether the live broadcast (terminal-relay WebSocket, legacy
// chat WebSocket and /_relay/snapshot) is on. VIBECAST_BROADCAST=off disables it while
// keeping the HTTP control-plane traffic (session-event, metadata, pending-answer).
// Anything other than "off"/"false"/"0"/"no" (including unset) means on.
func BroadcastEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("VIBECAST_BROADCAST"))) {
	case "off", "false", "0", "no":
		return false
	}
	return true
}

// PostLivesJSON POSTs a JSON body to an /api/lives/* URL with the runner bearer
// (when AGENTICS_TOKEN is set). It is http.Post plus the Authorization header.
func PostLivesJSON(url string, body []byte) (*http.Response, error) {
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	ApplyAgenticsToken(req)
	return http.DefaultClient.Do(req)
}

// GetLives GETs an /api/lives/* URL with the runner bearer (when AGENTICS_TOKEN is set).
func GetLives(url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	ApplyAgenticsToken(req)
	return http.DefaultClient.Do(req)
}

// AskQuestionTimeout is how long one ask_question call waits for its answer:
// VIBECAST_ASK_TIMEOUT (a Go duration, e.g. "45m") or 30 minutes.
func AskQuestionTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("VIBECAST_ASK_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 30 * time.Minute
}
