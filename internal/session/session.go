package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/pksorensen/vibecast/internal/types"
)

// VibecastDir returns the .vibecast directory path.
// Uses VIBECAST_HOME env var if set, otherwise falls back to ~/.vibecast.
func VibecastDir() string {
	if vh := os.Getenv("VIBECAST_HOME"); vh != "" {
		return filepath.Join(vh, ".vibecast")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".vibecast")
}

// SessionsDir returns the ~/.vibecast/sessions directory path.
func SessionsDir() string {
	return filepath.Join(VibecastDir(), "sessions")
}

// WriteSessionFile persists a session file to disk.
func WriteSessionFile(sf types.SessionFile) error {
	dir := SessionsDir()
	os.MkdirAll(dir, 0755)
	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, sf.SessionID+".json"), data, 0644)
}

// DeleteSessionFile removes a session file by session ID.
func DeleteSessionFile(sessionID string) {
	os.Remove(filepath.Join(SessionsDir(), sessionID+".json"))
}

// ReadSessionFile reads and parses a session file.
func ReadSessionFile(path string) (*types.SessionFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var sf types.SessionFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil, err
	}
	return &sf, nil
}

// FindSessionByWorkspace finds the most recent session for the given working directory.
func FindSessionByWorkspace(cwd string) *types.SessionFile {
	dir := SessionsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var best *types.SessionFile
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		sf, err := ReadSessionFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if cwd == sf.Workspace || strings.HasPrefix(cwd, sf.Workspace+string(os.PathSeparator)) {
			if best == nil || sf.StartedAt > best.StartedAt {
				best = sf
			}
		}
	}
	return best
}

// FindActiveSession finds any session with a running PID.
func FindActiveSession() *types.SessionFile {
	dir := SessionsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		sf, err := ReadSessionFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if sf.PID > 0 {
			proc, err := os.FindProcess(sf.PID)
			if err != nil {
				continue
			}
			if err := proc.Signal(syscall.Signal(0)); err == nil {
				return sf
			}
		}
	}
	return nil
}

// FindAllActiveSessions returns all sessions with running PIDs.
func FindAllActiveSessions() []*types.SessionFile {
	dir := SessionsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var result []*types.SessionFile
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		sf, err := ReadSessionFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		if sf.PID > 1 {
			proc, err := os.FindProcess(sf.PID)
			if err != nil {
				continue
			}
			if err := proc.Signal(syscall.Signal(0)); err == nil {
				result = append(result, sf)
			}
		}
	}
	return result
}

// CleanStaleSessions removes session files for processes that are no longer running.
func CleanStaleSessions() {
	dir := SessionsDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		sf, err := ReadSessionFile(filepath.Join(dir, e.Name()))
		if err != nil {
			os.Remove(filepath.Join(dir, e.Name()))
			continue
		}
		if sf.PID <= 1 {
			os.Remove(filepath.Join(dir, e.Name()))
			continue
		}
		if sf.PID > 1 {
			proc, err := os.FindProcess(sf.PID)
			if err != nil {
				os.Remove(filepath.Join(dir, e.Name()))
				continue
			}
			if err := proc.Signal(syscall.Signal(0)); err != nil {
				os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
}

// ResolveTargetSession returns the session to target for MCP tool calls.
func ResolveTargetSession(selectedSessionID string) (*types.SessionFile, error) {
	if selectedSessionID != "" {
		path := filepath.Join(SessionsDir(), selectedSessionID+".json")
		sf, err := ReadSessionFile(path)
		if err != nil {
			return nil, fmt.Errorf("session %q not found", selectedSessionID)
		}
		if sf.PID > 0 {
			proc, err := os.FindProcess(sf.PID)
			if err == nil {
				if err := proc.Signal(syscall.Signal(0)); err == nil {
					return sf, nil
				}
			}
		}
		return nil, fmt.Errorf("session %q is no longer running", selectedSessionID)
	}

	active := FindAllActiveSessions()
	if len(active) == 0 {
		return nil, fmt.Errorf("no active broadcast sessions found")
	}
	if len(active) == 1 {
		return active[0], nil
	}
	ids := make([]string, len(active))
	for i, s := range active {
		ids[i] = s.SessionID
	}
	return nil, fmt.Errorf("multiple active sessions found (%s). Use select_session to choose one", strings.Join(ids, ", "))
}

// ScanClaudeSessions discovers Claude Code sessions for the current project.
func ScanClaudeSessions() []types.ClaudeSessionInfo {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}

	if gitRoot, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
		cwd = strings.TrimSpace(string(gitRoot))
	}

	encoded := strings.ReplaceAll(cwd, string(os.PathSeparator), "-")
	projectDir := filepath.Join(home, ".claude", "projects", encoded)

	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return nil
	}

	var sessions []types.ClaudeSessionInfo
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") || strings.HasPrefix(name, "agent-") {
			continue
		}

		sessionID := strings.TrimSuffix(name, ".jsonl")
		filePath := filepath.Join(projectDir, name)

		info, err := e.Info()
		if err != nil {
			continue
		}

		firstPrompt := ""
		messageCount := 0
		f, err := os.Open(filePath)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 256*1024), 256*1024)
		for scanner.Scan() {
			var entry map[string]interface{}
			if json.Unmarshal(scanner.Bytes(), &entry) != nil {
				continue
			}
			messageCount++
			if t, ok := entry["type"].(string); ok && t == "user" && firstPrompt == "" {
				if msg, ok := entry["message"].(string); ok {
					firstPrompt = msg
				} else if content, ok := entry["content"].(string); ok {
					firstPrompt = content
				}
			}
		}
		f.Close()

		if messageCount == 0 {
			continue
		}

		if len(firstPrompt) > 50 {
			firstPrompt = firstPrompt[:50] + "..."
		}

		sessions = append(sessions, types.ClaudeSessionInfo{
			SessionID:    sessionID,
			LastUpdated:  info.ModTime(),
			FirstPrompt:  firstPrompt,
			MessageCount: messageCount,
		})
	}

	// Sort by last updated descending
	for i := 0; i < len(sessions); i++ {
		for j := i + 1; j < len(sessions); j++ {
			if sessions[j].LastUpdated.After(sessions[i].LastUpdated) {
				sessions[i], sessions[j] = sessions[j], sessions[i]
			}
		}
	}

	return sessions
}

// RelativeTime returns a human-readable relative time string.
func RelativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		if m == 1 {
			return "1 min ago"
		}
		return fmt.Sprintf("%d mins ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", h)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	}
}
