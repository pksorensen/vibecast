package util

import (
	"crypto/rand"
	"fmt"
	mathrand "math/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FindFreePort finds an available TCP port on localhost.
func FindFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port, nil
}

// GenerateSessionID generates a random 8-character session identifier.
func GenerateSessionID() string {
	chars := "abcdefghijklmnopqrstuvwxyz0123456789"
	result := make([]byte, 8)
	for i := range result {
		result[i] = chars[mathrand.Intn(len(chars))]
	}
	return string(result)
}

// FilterEnv returns a copy of env with entries matching the given key removed.
func FilterEnv(env []string, exclude string) []string {
	var result []string
	prefix := exclude + "="
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			result = append(result, e)
		}
	}
	return result
}

// GenerateUUIDv4 generates a random UUID v4 string.
func GenerateUUIDv4() string {
	var uuid [16]byte
	rand.Read(uuid[:])
	uuid[6] = (uuid[6] & 0x0f) | 0x40 // version 4
	uuid[8] = (uuid[8] & 0x3f) | 0x80 // variant 1
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])
}

// GetServerHost returns the AGENTICS_SERVER env var (or the deprecated AGENTIC_SERVER) or the default host.
func GetServerHost() string {
	if h := os.Getenv("AGENTICS_SERVER"); h != "" {
		return h
	}
	if h := os.Getenv("AGENTIC_SERVER"); h != "" {
		return h
	}
	return "agentics.dk"
}

// IsLocalHost returns true if the host is localhost or 127.0.0.1.
func IsLocalHost(host string) bool {
	return strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "127.0.0.1")
}

// BuildViewerURL constructs the viewer URL for a given server host and broadcast ID.
func BuildViewerURL(serverHost, broadcastID string) string {
	if IsLocalHost(serverHost) {
		return fmt.Sprintf("http://%s/live/%s", serverHost, broadcastID)
	}
	host := strings.Split(serverHost, ":")[0]
	return fmt.Sprintf("https://%s/live/%s", host, broadcastID)
}

// BuildJoinURL constructs the PIN-entry join URL for a given server host.
func BuildJoinURL(serverHost string) string {
	if IsLocalHost(serverHost) {
		return fmt.Sprintf("http://%s/join", serverHost)
	}
	host := strings.Split(serverHost, ":")[0]
	return fmt.Sprintf("https://%s/join", host)
}

// GetProjectName returns the project name (without owner prefix).
// Uses AGENTICS_PROJECT env var first, then custom name, then directory basename.
func GetProjectName(customName string) string {
	if envProject := os.Getenv("AGENTICS_PROJECT"); envProject != "" {
		// Strip owner prefix if present (e.g. "default/test-projekt" -> "test-projekt")
		if idx := strings.LastIndex(envProject, "/"); idx >= 0 {
			return envProject[idx+1:]
		}
		return envProject
	}
	if customName != "" {
		return customName
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "unknown"
	}
	return filepath.Base(cwd)
}

// GetProjectOwner returns the owner part of AGENTICS_PROJECT (e.g. "default"),
// or falls back to "default" if not set.
func GetProjectOwner() string {
	if envProject := os.Getenv("AGENTICS_PROJECT"); envProject != "" {
		if idx := strings.Index(envProject, "/"); idx >= 0 {
			return envProject[:idx]
		}
	}
	return "default"
}

// ExtractSessionID takes a URL or bare session ID and returns the session ID portion.
func ExtractSessionID(input string) string {
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://") {
		parts := strings.Split(strings.TrimRight(input, "/"), "/")
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}
	return input
}

// FormatInviteCode formats a stream ID as an invite code.
func FormatInviteCode(id string) string {
	up := strings.ToUpper(id)
	if len(up) > 4 {
		return up[:4] + "-" + up[4:]
	}
	return up
}

// DebugLog appends a timestamped line to /tmp/vibecast-hook-debug.log.
func DebugLog(format string, args ...interface{}) {
	f, err := os.OpenFile("/tmp/vibecast-hook-debug.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("15:04:05.000"), msg)
}

// FileExists returns true if the file at the given path exists.
func FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
