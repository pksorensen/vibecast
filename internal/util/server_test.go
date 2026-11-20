package util

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseServer(t *testing.T) {
	cases := []struct {
		raw                      string
		scheme, host, base, dial string
		explicit                 bool
	}{
		// Bare values keep the historical rule: localhost/127.0.0.1 => http, else https.
		{"localhost:3000", "http", "localhost:3000", "http://localhost:3000", "localhost:3000", false},
		{"127.0.0.1:41000", "http", "127.0.0.1:41000", "http://127.0.0.1:41000", "127.0.0.1:41000", false},
		{"agentics.dk", "https", "agentics.dk", "https://agentics.dk", "agentics.dk:443", false},
		{"staging.example.test:8443", "https", "staging.example.test:8443", "https://staging.example.test:8443", "staging.example.test:8443", false},
		// A bare private IP is not "local" under the old rule — it stays https.
		{"172.17.0.1:8080", "https", "172.17.0.1:8080", "https://172.17.0.1:8080", "172.17.0.1:8080", false},
		// Explicit schemes are honoured as given.
		{"http://172.17.0.1:8080", "http", "172.17.0.1:8080", "http://172.17.0.1:8080", "172.17.0.1:8080", true},
		{"http://controller.example.test", "http", "controller.example.test", "http://controller.example.test", "controller.example.test:80", true},
		{"https://agents.example.test", "https", "agents.example.test", "https://agents.example.test", "agents.example.test:443", true},
		{"https://agents.example.test/", "https", "agents.example.test", "https://agents.example.test", "agents.example.test:443", true},
		{"HTTPS://agents.example.test:9443/alp/?x=1", "https", "agents.example.test:9443", "https://agents.example.test:9443/alp", "agents.example.test:9443", true},
		{"http://localhost:3000", "http", "localhost:3000", "http://localhost:3000", "localhost:3000", true},
		{"  http://[::1]:8080  ", "http", "[::1]:8080", "http://[::1]:8080", "[::1]:8080", true},
	}
	for _, c := range cases {
		ep := ParseServer(c.raw)
		if ep.Scheme != c.scheme || ep.Host != c.host || ep.BaseURL() != c.base || ep.DialAddr() != c.dial || ep.Explicit != c.explicit {
			t.Errorf("ParseServer(%q) = {scheme=%q host=%q base=%q dial=%q explicit=%v}, want {%q %q %q %q %v}",
				c.raw, ep.Scheme, ep.Host, ep.BaseURL(), ep.DialAddr(), ep.Explicit,
				c.scheme, c.host, c.base, c.dial, c.explicit)
		}
		if ep.TLS() != (c.scheme == "https") {
			t.Errorf("ParseServer(%q).TLS() = %v", c.raw, ep.TLS())
		}
	}
}

func TestServerURLMatchesLegacyRuleForBareHosts(t *testing.T) {
	legacy := func(host, path string) string {
		scheme := "https"
		if IsLocalHost(host) {
			scheme = "http"
		}
		return scheme + "://" + host + path
	}
	for _, h := range []string{"localhost:3000", "127.0.0.1:1234", "agentics.dk", "example.test:8080", "localhost"} {
		if got, want := ServerURL(h, "/api/lives/session-event"), legacy(h, "/api/lives/session-event"); got != want {
			t.Errorf("ServerURL(%q) = %q, want legacy %q", h, got, want)
		}
	}
	if got := ServerURL("http://172.17.0.1:8080", "/api/lives/metadata"); got != "http://172.17.0.1:8080/api/lives/metadata" {
		t.Errorf("explicit http URL: %q", got)
	}
	if got := ServerHostPort("https://agents.example.test/base"); got != "agents.example.test" {
		t.Errorf("ServerHostPort: %q", got)
	}
}

func TestViewerAndJoinURLs(t *testing.T) {
	if got := BuildViewerURL("agentics.dk:443", "abc"); got != "https://agentics.dk/live/abc" {
		t.Errorf("bare viewer url changed: %q", got)
	}
	if got := BuildViewerURL("localhost:3000", "abc"); got != "http://localhost:3000/live/abc" {
		t.Errorf("local viewer url changed: %q", got)
	}
	if got := BuildViewerURL("http://172.17.0.1:8080", "abc"); got != "http://172.17.0.1:8080/live/abc" {
		t.Errorf("explicit viewer url: %q", got)
	}
	if got := BuildJoinURL("https://agents.example.test"); got != "https://agents.example.test/join" {
		t.Errorf("explicit join url: %q", got)
	}
}

func TestBroadcastEnabled(t *testing.T) {
	for v, want := range map[string]bool{"": true, "on": true, "ON": true, "1": true, "off": false, "OFF": false, " off ": false, "false": false, "0": false, "no": false} {
		t.Setenv("VIBECAST_BROADCAST", v)
		if got := BroadcastEnabled(); got != want {
			t.Errorf("VIBECAST_BROADCAST=%q: BroadcastEnabled()=%v want %v", v, got, want)
		}
	}
}

func TestLivesRequestsCarryBearerOnlyWhenTokenSet(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path+" auth="+r.Header.Get("Authorization")+" ct="+r.Header.Get("Content-Type"))
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	t.Setenv("AGENTICS_TOKEN", "")
	resp, err := PostLivesJSON(srv.URL+"/api/lives/metadata", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	t.Setenv("AGENTICS_TOKEN", " rnt_test123 ")
	resp, err = PostLivesJSON(srv.URL+"/api/lives/session-event", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	resp, err = GetLives(srv.URL + "/api/lives/sessions/s1/pending-answer")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	want := []string{
		"POST /api/lives/metadata auth= ct=application/json",
		"POST /api/lives/session-event auth=Bearer rnt_test123 ct=application/json",
		"GET /api/lives/sessions/s1/pending-answer auth=Bearer rnt_test123 ct=",
	}
	if len(got) != len(want) {
		t.Fatalf("requests = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAskQuestionTimeout(t *testing.T) {
	t.Setenv("VIBECAST_ASK_TIMEOUT", "")
	if d := AskQuestionTimeout(); d != 30*time.Minute {
		t.Errorf("default = %v", d)
	}
	t.Setenv("VIBECAST_ASK_TIMEOUT", "90s")
	if d := AskQuestionTimeout(); d != 90*time.Second {
		t.Errorf("override = %v", d)
	}
	t.Setenv("VIBECAST_ASK_TIMEOUT", "bogus")
	if d := AskQuestionTimeout(); d != 30*time.Minute {
		t.Errorf("bogus falls back = %v", d)
	}
}
