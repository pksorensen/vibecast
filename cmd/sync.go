package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pksorensen/vibecast/internal/auth"
	"github.com/pksorensen/vibecast/internal/session"
	"github.com/pksorensen/vibecast/internal/util"
)

const chunkSize = 200

// RunSync handles the "vibecast sync" subcommand.
func RunSync() {
	var sessionID string
	var syncAll bool
	var filePath string

	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--session-id":
			if i+1 < len(args) {
				sessionID = args[i+1]
				i++
			}
		case "--all":
			syncAll = true
		default:
			if !strings.HasPrefix(args[i], "-") {
				filePath = args[i]
			}
		}
	}

	if sessionID == "" {
		fmt.Fprintf(os.Stderr, "Error: --session-id is required\n")
		fmt.Fprintf(os.Stderr, "Usage: vibecast sync --session-id <id> [session.jsonl]\n")
		fmt.Fprintf(os.Stderr, "       vibecast sync --session-id <id> --all\n")
		os.Exit(1)
	}

	// Get auth token
	token, _, err := auth.GetValidToken()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: not logged in. Run 'vibecast login' first.\n")
		fmt.Fprintf(os.Stderr, "  (%v)\n", err)
		os.Exit(1)
	}

	serverHost := util.GetServerHost()

	if filePath != "" {
		// Sync a specific file
		syncFile(filePath, sessionID, serverHost, token)
	} else if syncAll {
		// Sync all sessions for current project
		sessions := session.ScanClaudeSessions()
		if len(sessions) == 0 {
			fmt.Println("No Claude sessions found for the current project.")
			return
		}
		fmt.Printf("Found %d sessions to sync.\n", len(sessions))
		for i, s := range sessions {
			path := resolveSessionPath(s.SessionID)
			if path == "" {
				fmt.Printf("[%d/%d] Session %s: file not found, skipping\n", i+1, len(sessions), s.SessionID[:8])
				continue
			}
			fmt.Printf("[%d/%d] Syncing session %s (%s)\n", i+1, len(sessions), s.SessionID[:8], s.FirstPrompt)
			syncFile(path, sessionID, serverHost, token)
		}
	} else {
		// Auto-discover most recent session
		sessions := session.ScanClaudeSessions()
		if len(sessions) == 0 {
			fmt.Println("No Claude sessions found for the current project.")
			fmt.Println("Provide a file path or use --all.")
			os.Exit(1)
		}
		s := sessions[0] // most recent
		path := resolveSessionPath(s.SessionID)
		if path == "" {
			fmt.Fprintf(os.Stderr, "Error: could not find session file for %s\n", s.SessionID)
			os.Exit(1)
		}
		fmt.Printf("Syncing most recent session: %s\n", s.SessionID[:8])
		if s.FirstPrompt != "" {
			fmt.Printf("  First prompt: %s\n", s.FirstPrompt)
		}
		fmt.Printf("  Messages: %d\n", s.MessageCount)
		syncFile(path, sessionID, serverHost, token)
	}
}

func resolveSessionPath(sessionID string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}

	// Try git root first
	if out, err := execGitCommand("rev-parse", "--show-toplevel"); err == nil {
		cwd = strings.TrimSpace(out)
	}

	encoded := strings.ReplaceAll(cwd, string(os.PathSeparator), "-")
	path := filepath.Join(home, ".claude", "projects", encoded, sessionID+".jsonl")
	if _, err := os.Stat(path); err == nil {
		return path
	}
	return ""
}

func execGitCommand(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	return string(out), err
}

func syncFile(filePath, sessionID, serverHost, token string) {
	f, err := os.Open(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: could not open %s: %v\n", filePath, err)
		return
	}
	defer f.Close()

	// Collect all lines
	var allLines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer for long lines
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		allLines = append(allLines, line)
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Error reading file: %v\n", err)
		return
	}

	if len(allLines) == 0 {
		fmt.Println("  File is empty, nothing to sync.")
		return
	}

	fmt.Printf("  Total lines: %d\n", len(allLines))

	scheme := "https"
	if util.IsLocalHost(serverHost) {
		scheme = "http"
	}
	syncURL := fmt.Sprintf("%s://%s/api/lives/sync", scheme, serverHost)

	totalCounts := map[string]int{}

	// Chunk and POST
	for i := 0; i < len(allLines); i += chunkSize {
		end := i + chunkSize
		if end > len(allLines) {
			end = len(allLines)
		}
		chunk := allLines[i:end]

		payload := map[string]interface{}{
			"sessionId": sessionID,
			"lines":     chunk,
		}
		body, err := json.Marshal(payload)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Error marshaling chunk: %v\n", err)
			return
		}

		req, err := http.NewRequest("POST", syncURL, bytes.NewReader(body))
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Error creating request: %v\n", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Error sending chunk %d-%d: %v\n", i+1, end, err)
			return
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != 200 {
			fmt.Fprintf(os.Stderr, "  Server error (status %d): %s\n", resp.StatusCode, string(respBody))
			return
		}

		var result struct {
			OK     bool           `json:"ok"`
			Counts map[string]int `json:"counts"`
		}
		json.Unmarshal(respBody, &result)

		for k, v := range result.Counts {
			totalCounts[k] += v
		}

		fmt.Printf("  Chunk %d-%d/%d sent\n", i+1, end, len(allLines))
	}

	fmt.Printf("  Sync complete. Records written:\n")
	for k, v := range totalCounts {
		if v > 0 {
			fmt.Printf("    %s: %d\n", k, v)
		}
	}
}
