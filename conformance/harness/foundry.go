package harness

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"
)

// piFoundryURL / piFoundryToken are the running `pks foundry proxy` endpoint + token, set by
// StartFoundryProxy and read by preparePiConfig to wire pi's pks-foundry provider (Foundry gpt-5.5
// via the token-swapping proxy — Poul's chosen pi model source). Empty when no proxy is running
// (pks unavailable / not started): preparePiConfig then writes no model config, so pi launches
// keyless — the launch/identity scenarios (C01/C02) still register, model-dependent scenarios need
// the proxy. The credential-less CI path is a separate mock-provider mode (future work).
var (
	piFoundryURL   string
	piFoundryToken string
)

// FoundryProxy is a `pks foundry proxy` process owned by the conformance run (started in TestMain
// when pi is selected, stopped after). One shared proxy serves every pi scenario.
type FoundryProxy struct {
	cmd *exec.Cmd
	log *os.File
}

// StartFoundryProxy starts `pks foundry proxy` on a free port with a fixed conformance token, waits
// for it to listen, and records the URL+token for preparePiConfig. Returns (nil, nil) if `pks` is
// not on PATH — the caller proceeds with keyless pi rather than failing the whole run.
func StartFoundryProxy() (*FoundryProxy, error) {
	if _, err := exec.LookPath("pks"); err != nil {
		return nil, nil // no pks → no real-mode model; C01/C02 still pass, model scenarios won't
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	const token = "vibecast-conformance"
	logf, _ := os.CreateTemp("", "foundry-proxy-*.log")
	cmd := exec.Command("pks", "foundry", "proxy", "--port", fmt.Sprint(port), "--token", token)
	if logf != nil {
		cmd.Stdout = logf
		cmd.Stderr = logf
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start pks foundry proxy: %w", err)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	if !waitListen(addr, 90*time.Second) {
		_ = cmd.Process.Kill()
		logPath := ""
		if logf != nil {
			logPath = logf.Name()
		}
		return nil, fmt.Errorf("pks foundry proxy did not listen on %s within 90s (log: %s)", addr, logPath)
	}
	piFoundryURL = "http://" + addr
	piFoundryToken = token
	return &FoundryProxy{cmd: cmd, log: logf}, nil
}

// Stop kills the proxy and clears the recorded URL/token. Nil-safe.
func (p *FoundryProxy) Stop() {
	piFoundryURL, piFoundryToken = "", ""
	if p == nil {
		return
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
	}
	if p.log != nil {
		_ = p.log.Close()
	}
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func waitListen(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 1*time.Second); err == nil {
			_ = c.Close()
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}
