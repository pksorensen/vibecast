package hooks

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/pksorensen/vibecast/internal/types"
)

// Hook metadata posts honour a full-URL server host and carry the runner bearer when
// AGENTICS_TOKEN is set.
func TestHookPostMetadataSchemeAndBearer(t *testing.T) {
	var mu sync.Mutex
	var paths, auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	t.Setenv("VIBECAST_HOME", t.TempDir()) // no saved login
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTICS_TOKEN", "rnt_hook")
	sf := &types.SessionFile{SessionID: "s1", ServerHost: srv.URL} // "http://127.0.0.1:PORT"
	HookPostMetadata(sf, []byte(`{"sessionId":"s1","type":"metadata","subtype":"prompt"}`))

	t.Setenv("AGENTICS_TOKEN", "")
	HookPostMetadata(sf, []byte(`{"sessionId":"s1","type":"metadata","subtype":"prompt"}`))

	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 2 || paths[0] != "/api/lives/metadata" {
		t.Fatalf("paths = %v", paths)
	}
	if auths[0] != "Bearer rnt_hook" {
		t.Errorf("with AGENTICS_TOKEN: auth = %q", auths[0])
	}
	if auths[1] != "" {
		t.Errorf("without AGENTICS_TOKEN and no login: auth = %q", auths[1])
	}
}
