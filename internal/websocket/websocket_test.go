package websocket

import (
	"crypto/sha1"
	"encoding/base64"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// upgradeServer is a minimal WebSocket upgrader that records request paths.
func upgradeServer(t *testing.T, addr string) (string, func() []string) {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("cannot listen on %s: %v", addr, err)
	}
	var mu sync.Mutex
	var paths []string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.RequestURI())
		mu.Unlock()
		h := sha1.New()
		h.Write([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		conn, bufrw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		bufrw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " +
			base64.StdEncoding.EncodeToString(h.Sum(nil)) + "\r\n\r\n")
		bufrw.Flush()
		time.AfterFunc(2*time.Second, func() { conn.Close() })
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String(), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), paths...)
	}
}

// A non-local host with an explicit http:// scheme must be dialled as plain ws. Under the
// bare-host rule (only localhost/127.0.0.1 are "local") 127.0.0.2 would go over TLS.
func TestConnectWithProtocolHonoursExplicitHTTPScheme(t *testing.T) {
	addr, paths := upgradeServer(t, "127.0.0.2:0")

	conn, _, err := ConnectWithProtocol("http://"+addr+"/edge", "/api/lives/broadcast/ws?sessionId=s1", "")
	if err != nil {
		t.Fatalf("explicit http:// dial failed: %v", err)
	}
	conn.Close()
	got := paths()
	if len(got) != 1 || got[0] != "/edge/api/lives/broadcast/ws?sessionId=s1" {
		t.Fatalf("paths = %v", got)
	}
}

// A bare non-local host keeps today's rule: TLS. Against a plain-HTTP server that handshake
// fails, and the server never sees a WebSocket request.
func TestConnectWithProtocolBareNonLocalStillUsesTLS(t *testing.T) {
	addr, paths := upgradeServer(t, "127.0.0.2:0")

	conn, _, err := ConnectWithProtocol(addr, "/api/lives/broadcast/ws", "")
	if err == nil {
		conn.Close()
		t.Fatalf("bare non-local host should have attempted TLS and failed against a plain server")
	}
	if got := paths(); len(got) != 0 {
		t.Fatalf("plain request reached server: %v", got)
	}
}

// Bare localhost keeps plain ws (the conformance harness path).
func TestConnectWithProtocolBareLocalhost(t *testing.T) {
	addr, paths := upgradeServer(t, "127.0.0.1:0")
	conn, _, err := ConnectWithProtocol(addr, "/ws", "tty")
	if err != nil {
		t.Fatalf("bare local dial: %v", err)
	}
	conn.Close()
	if got := paths(); len(got) != 1 || !strings.HasPrefix(got[0], "/ws") {
		t.Fatalf("paths = %v", got)
	}
}
