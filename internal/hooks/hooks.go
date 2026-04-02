package hooks

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pksorensen/vibecast/internal/auth"
	"github.com/pksorensen/vibecast/internal/control"
	"github.com/pksorensen/vibecast/internal/session"
	"github.com/pksorensen/vibecast/internal/telemetry"
	"github.com/pksorensen/vibecast/internal/types"
	"github.com/pksorensen/vibecast/internal/util"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// HandleHookCommand dispatches to the appropriate hook handler.
func HandleHookCommand(args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "usage: vibecast hook <prompt|session|tool|post-tool|subagent-start|subagent-stop|stop>\n")
		os.Exit(1)
	}

	ctx, span := telemetry.Tracer().Start(context.Background(), "vibecast.hook",
		trace.WithAttributes(attribute.String("hook.type", args[0])))
	defer span.End()
	_ = ctx

	switch args[0] {
	case "prompt":
		handleHookPrompt()
	case "session":
		handleHookSession()
	case "tool":
		handleHookTool()
	case "post-tool":
		handleHookPostTool()
	case "subagent-start":
		handleHookSubagentStart()
	case "subagent-stop":
		handleHookSubagentStop()
	case "stop":
		handleHookStop()
	default:
		fmt.Fprintf(os.Stderr, "usage: vibecast hook <prompt|session|tool|post-tool|subagent-start|subagent-stop|stop>\n")
		os.Exit(1)
	}
}

func hookReadStdinAndFindSession() (json.RawMessage, *types.SessionFile, string, string, string) {
	stdinData, err := io.ReadAll(os.Stdin)
	if err != nil {
		util.DebugLog("hookReadStdin: stdin read error: %v", err)
		os.Exit(0)
	}

	var base struct {
		CWD            string `json:"cwd"`
		TranscriptPath string `json:"transcript_path"`
		SessionID      string `json:"session_id"`
	}
	if err := json.Unmarshal(stdinData, &base); err != nil {
		util.DebugLog("hookReadStdin: json unmarshal error: %v", err)
		os.Exit(0)
	}

	cwd := base.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	sf := session.FindSessionByWorkspace(cwd)
	if sf == nil {
		util.DebugLog("hookReadStdin: no session found for cwd=%s", cwd)
		os.Exit(0)
	}

	util.DebugLog("hookReadStdin: found session streamId=%s, claudeSessionId=%s", sf.StreamID, base.SessionID)
	return json.RawMessage(stdinData), sf, cwd, base.TranscriptPath, base.SessionID
}

// ── Transcript cursor infrastructure ────────────────────────────────────────

func transcriptCursorDir(streamID string) string {
	return filepath.Join(session.VibecastDir(), "transcripts", streamID)
}

// TranscriptCursorDir is exported for use in stream cleanup.
func TranscriptCursorDir(streamID string) string {
	return transcriptCursorDir(streamID)
}

func transcriptCursorPath(streamID, transcriptPath string) string {
	h := sha256.Sum256([]byte(transcriptPath))
	prefix := fmt.Sprintf("%x", h[:8])
	return filepath.Join(transcriptCursorDir(streamID), prefix+".offset")
}

func readTranscriptIncrement(streamID, transcriptPath string) []map[string]interface{} {
	if transcriptPath == "" {
		return nil
	}

	cursorPath := transcriptCursorPath(streamID, transcriptPath)
	os.MkdirAll(filepath.Dir(cursorPath), 0755)

	var offset int64
	if data, err := os.ReadFile(cursorPath); err == nil {
		fmt.Sscanf(string(data), "%d", &offset)
	}

	f, err := os.Open(transcriptPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	if offset > 0 {
		if _, err := f.Seek(offset, 0); err != nil {
			return nil
		}
	}

	var lines []map[string]interface{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	newOffset := offset
	meaningfulTypes := map[string]bool{
		"user": true, "assistant": true, "tool_use": true,
		"tool_result": true, "thinking": true, "result": true,
	}

	for scanner.Scan() {
		lineBytes := scanner.Bytes()
		newOffset += int64(len(lineBytes)) + 1

		var entry map[string]interface{}
		if err := json.Unmarshal(lineBytes, &entry); err != nil {
			continue
		}

		if t, ok := entry["type"].(string); ok && meaningfulTypes[t] {
			lines = append(lines, entry)
		}
	}

	os.WriteFile(cursorPath, []byte(fmt.Sprintf("%d", newOffset)), 0644)

	return lines
}

func readFirstUserPrompt(transcriptPath string) string {
	if transcriptPath == "" {
		return ""
	}
	f, err := os.Open(transcriptPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)
	for scanner.Scan() {
		var entry map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		t, _ := entry["type"].(string)
		if t != "user" {
			continue
		}
		msg, ok := entry["message"].(map[string]interface{})
		if !ok {
			continue
		}
		content, ok := msg["content"].([]interface{})
		if !ok {
			continue
		}
		for _, block := range content {
			b, ok := block.(map[string]interface{})
			if !ok {
				continue
			}
			if b["type"] == "text" {
				if text, ok := b["text"].(string); ok && text != "" {
					return text
				}
			}
		}
	}
	return ""
}

func extractUsageFromTranscript(lines []map[string]interface{}) map[string]interface{} {
	var inputTokens, outputTokens, cacheRead, cacheCreation float64
	found := false

	for _, line := range lines {
		t, _ := line["type"].(string)
		if t != "assistant" {
			continue
		}
		msg, ok := line["message"].(map[string]interface{})
		if !ok {
			continue
		}
		usage, ok := msg["usage"].(map[string]interface{})
		if !ok {
			continue
		}
		found = true
		if v, ok := usage["input_tokens"].(float64); ok {
			inputTokens += v
		}
		if v, ok := usage["output_tokens"].(float64); ok {
			outputTokens += v
		}
		if v, ok := usage["cache_read_input_tokens"].(float64); ok {
			cacheRead += v
		}
		if v, ok := usage["cache_creation_input_tokens"].(float64); ok {
			cacheCreation += v
		}
	}

	if !found {
		return nil
	}
	return map[string]interface{}{
		"input_tokens":                int(inputTokens),
		"output_tokens":               int(outputTokens),
		"cache_read_input_tokens":     int(cacheRead),
		"cache_creation_input_tokens": int(cacheCreation),
	}
}

// HookPostMetadata posts metadata to the server.
func HookPostMetadata(sf *types.SessionFile, payload []byte) {
	_, span := telemetry.Tracer().Start(context.Background(), "vibecast.hook.metadata_post",
		trace.WithAttributes(
			attribute.String("stream.id", sf.StreamID),
			attribute.String("server.host", sf.ServerHost),
		))
	defer span.End()

	scheme := "https"
	if util.IsLocalHost(sf.ServerHost) {
		scheme = "http"
	}

	url := fmt.Sprintf("%s://%s/api/lives/metadata", scheme, sf.ServerHost)
	span.SetAttributes(attribute.String("http.url", url))
	util.DebugLog("hookPostMetadata: POST %s (payload %d bytes)", url, len(payload))
	req, err := http.NewRequest("POST", url, bytes.NewReader(payload))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "request creation failed")
		util.DebugLog("hookPostMetadata: request creation error: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if token, _, authErr := auth.GetValidToken(); authErr == nil && token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "request failed")
		util.DebugLog("hookPostMetadata: error: %v", err)
		return
	}
	defer resp.Body.Close()
	span.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		span.SetStatus(codes.Error, fmt.Sprintf("HTTP %d", resp.StatusCode))
		util.DebugLog("hookPostMetadata: non-200 status=%d body=%s", resp.StatusCode, string(body))
	} else {
		util.DebugLog("hookPostMetadata: success (200)")
	}
}

func handleHookPrompt() {
	stdinData, sf, _, transcriptPath, claudeSessionId := hookReadStdinAndFindSession()
	_ = claudeSessionId

	var hookInput struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(stdinData, &hookInput); err != nil {
		os.Exit(0)
	}

	p := map[string]interface{}{
		"streamId":  sf.StreamID,
		"type":      "metadata",
		"subtype":   "prompt",
		"prompt":    hookInput.Prompt,
		"timestamp": time.Now().Unix(),
	}

	if tl := readTranscriptIncrement(sf.StreamID, transcriptPath); len(tl) > 0 {
		p["transcriptLines"] = tl
		if usage := extractUsageFromTranscript(tl); usage != nil {
			p["usage"] = usage
		}
	}

	payload, _ := json.Marshal(p)
	HookPostMetadata(sf, payload)
	os.Exit(0)
}

func handleHookSession() {
	stdinData, sf, _, transcriptPath, claudeSessionId := hookReadStdinAndFindSession()
	util.DebugLog("[session] checkpoint A: entry, claudeSessionId=%s", claudeSessionId)

	var hookInput struct {
		SessionID string `json:"session_id"`
		Source    string `json:"source"`
	}
	if err := json.Unmarshal(stdinData, &hookInput); err != nil {
		os.Exit(0)
	}

	p := map[string]interface{}{
		"streamId":        sf.StreamID,
		"type":            "metadata",
		"subtype":         "session_start",
		"source":          hookInput.Source,
		"claudeSessionId": claudeSessionId,
		"timestamp":       time.Now().Unix(),
	}

	if summary := readFirstUserPrompt(transcriptPath); summary != "" {
		p["sessionSummary"] = summary
	}

	if tl := readTranscriptIncrement(sf.StreamID, transcriptPath); len(tl) > 0 {
		p["transcriptLines"] = tl
	}

	payload, _ := json.Marshal(p)
	HookPostMetadata(sf, payload)

	viewerURL := util.BuildViewerURL(sf.ServerHost, sf.StreamID)
	output, _ := json.Marshal(map[string]interface{}{
		"additionalContext": fmt.Sprintf("This session is being broadcasted online at %s. Avoid showing sensitive secrets, API keys, or passwords in your output.", viewerURL),
	})
	os.Stdout.Write(output)
	os.Exit(0)
}

func handleHookTool() {
	stdinData, sf, _, transcriptPath, claudeSessionId := hookReadStdinAndFindSession()
	util.DebugLog("[tool] checkpoint A: entry, tool stdin=%d bytes", len(stdinData))

	var hookInput struct {
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
		ToolUseID string          `json:"tool_use_id"`
	}
	if err := json.Unmarshal(stdinData, &hookInput); err != nil {
		os.Exit(0)
	}

	util.DebugLog("[tool] checkpoint B: parsed tool_name=%s tool_use_id=%s", hookInput.ToolName, hookInput.ToolUseID)

	if hookInput.ToolName == "ExitPlanMode" {
		var planInput struct {
			Plan string `json:"plan"`
		}
		if len(hookInput.ToolInput) > 0 {
			json.Unmarshal(hookInput.ToolInput, &planInput)
		}
		if planInput.Plan != "" {
			planPayload, _ := json.Marshal(map[string]interface{}{
				"streamId":     sf.StreamID,
				"type":         "metadata",
				"subtype":      "plan",
				"planMarkdown": planInput.Plan,
				"timestamp":    time.Now().Unix(),
			})
			HookPostMetadata(sf, planPayload)
		}
	}

	var toolInput interface{}
	if len(hookInput.ToolInput) > 0 {
		json.Unmarshal(hookInput.ToolInput, &toolInput)
	}

	p := map[string]interface{}{
		"streamId":        sf.StreamID,
		"type":            "metadata",
		"subtype":         "tool_use",
		"toolName":        hookInput.ToolName,
		"toolInput":       toolInput,
		"toolUseId":       hookInput.ToolUseID,
		"claudeSessionId": claudeSessionId,
		"transcriptPath":  transcriptPath,
		"timestamp":       time.Now().Unix(),
	}

	if tl := readTranscriptIncrement(sf.StreamID, transcriptPath); len(tl) > 0 {
		p["transcriptLines"] = tl
	}

	payload, _ := json.Marshal(p)
	HookPostMetadata(sf, payload)
	os.Exit(0)
}

func handleHookPostTool() {
	util.DebugLog("[post-tool] checkpoint A: entry")
	stdinData, sf, _, transcriptPath, claudeSessionId := hookReadStdinAndFindSession()
	util.DebugLog("[post-tool] checkpoint B: stdin read, %d bytes, streamId=%s", len(stdinData), sf.StreamID)

	var hookInput struct {
		ToolName     string          `json:"tool_name"`
		ToolInput    json.RawMessage `json:"tool_input"`
		ToolResponse json.RawMessage `json:"tool_response"`
		ToolUseID    string          `json:"tool_use_id"`
	}
	if err := json.Unmarshal(stdinData, &hookInput); err != nil {
		util.DebugLog("[post-tool] json unmarshal error: %v", err)
		os.Exit(0)
	}
	util.DebugLog("[post-tool] checkpoint C: parsed tool_name=%s tool_use_id=%s", hookInput.ToolName, hookInput.ToolUseID)

	var toolResponse interface{}
	if len(hookInput.ToolResponse) > 0 {
		var structured interface{}
		if err := json.Unmarshal(hookInput.ToolResponse, &structured); err == nil {
			toolResponse = structured
		} else {
			toolResponse = string(hookInput.ToolResponse)
		}
		if s, ok := toolResponse.(string); ok && len(s) > 2000 {
			toolResponse = s[:2000]
		}
	}

	var toolInput interface{}
	if len(hookInput.ToolInput) > 0 {
		json.Unmarshal(hookInput.ToolInput, &toolInput)
	}

	transcriptLines := readTranscriptIncrement(sf.StreamID, transcriptPath)
	usage := extractUsageFromTranscript(transcriptLines)

	p := map[string]interface{}{
		"streamId":        sf.StreamID,
		"type":            "metadata",
		"subtype":         "tool_use_end",
		"toolName":        hookInput.ToolName,
		"toolInput":       toolInput,
		"toolResponse":    toolResponse,
		"toolUseId":       hookInput.ToolUseID,
		"claudeSessionId": claudeSessionId,
		"transcriptPath":  transcriptPath,
		"timestamp":       time.Now().Unix(),
	}

	if usage != nil {
		p["usage"] = usage
	}

	if len(transcriptLines) > 0 {
		p["transcriptLines"] = transcriptLines
	}

	payload, _ := json.Marshal(p)
	util.DebugLog("[post-tool] checkpoint D: posting metadata for tool_use_end %s", hookInput.ToolUseID)
	HookPostMetadata(sf, payload)
	util.DebugLog("[post-tool] checkpoint E: done")
	os.Exit(0)
}

func handleHookSubagentStart() {
	stdinData, sf, _, transcriptPath, claudeSessionId := hookReadStdinAndFindSession()
	_ = claudeSessionId

	var hookInput struct {
		AgentID   string `json:"agent_id"`
		AgentType string `json:"agent_type"`
	}
	if err := json.Unmarshal(stdinData, &hookInput); err != nil {
		os.Exit(0)
	}

	p := map[string]interface{}{
		"streamId":       sf.StreamID,
		"type":           "metadata",
		"subtype":        "subagent_start",
		"agentId":        hookInput.AgentID,
		"agentType":      hookInput.AgentType,
		"transcriptPath": transcriptPath,
		"timestamp":      time.Now().Unix(),
	}

	if tl := readTranscriptIncrement(sf.StreamID, transcriptPath); len(tl) > 0 {
		p["transcriptLines"] = tl
	}

	payload, _ := json.Marshal(p)
	HookPostMetadata(sf, payload)
	os.Exit(0)
}

func handleHookSubagentStop() {
	stdinData, sf, _, transcriptPath, claudeSessionId := hookReadStdinAndFindSession()
	_ = claudeSessionId

	var hookInput struct {
		AgentID             string `json:"agent_id"`
		AgentType           string `json:"agent_type"`
		AgentTranscriptPath string `json:"agent_transcript_path"`
	}
	if err := json.Unmarshal(stdinData, &hookInput); err != nil {
		os.Exit(0)
	}

	transcriptLines := readTranscriptIncrement(sf.StreamID, transcriptPath)
	agentTranscriptLines := readTranscriptIncrement(sf.StreamID, hookInput.AgentTranscriptPath)

	p := map[string]interface{}{
		"streamId":            sf.StreamID,
		"type":                "metadata",
		"subtype":             "subagent_stop",
		"agentId":             hookInput.AgentID,
		"agentType":           hookInput.AgentType,
		"transcriptPath":      transcriptPath,
		"agentTranscriptPath": hookInput.AgentTranscriptPath,
		"timestamp":           time.Now().Unix(),
	}

	if len(transcriptLines) > 0 {
		p["transcriptLines"] = transcriptLines
	}
	if len(agentTranscriptLines) > 0 {
		p["agentTranscriptLines"] = agentTranscriptLines
		toolUseIDs := extractToolUseIDs(agentTranscriptLines)
		if len(toolUseIDs) > 0 {
			p["toolUseIds"] = toolUseIDs
		}
	}

	payload, _ := json.Marshal(p)
	HookPostMetadata(sf, payload)
	os.Exit(0)
}

func handleHookStop() {
	_, sf, cwd, transcriptPath, _ := hookReadStdinAndFindSession()

	transcriptLines := readTranscriptIncrement(sf.StreamID, transcriptPath)

	text := extractAssistantText(transcriptLines)
	usage := extractUsageFromTranscript(transcriptLines)

	if text != "" || usage != nil {
		p := map[string]interface{}{
			"streamId":  sf.StreamID,
			"type":      "metadata",
			"subtype":   "assistant_response",
			"timestamp": time.Now().Unix(),
		}
		if text != "" {
			p["text"] = text
		}
		if usage != nil {
			p["usage"] = usage
		}
		if len(transcriptLines) > 0 {
			p["transcriptLines"] = transcriptLines
		}
		payload, _ := json.Marshal(p)
		HookPostMetadata(sf, payload)
	}

	// Auto-git: block stop if uncommitted changes exist
	if os.Getenv("AGENTICS_AUTO_GIT") == "1" {
		out, err := exec.Command("git", "-C", cwd, "status", "--porcelain").Output()
		if err == nil && len(strings.TrimSpace(string(out))) > 0 {
			hint := os.Getenv("AGENTICS_COMMIT_MESSAGE_HINT")
			if hint == "" {
				hint = "Use semantic commits: feat(scope): description"
			}
			reason := fmt.Sprintf(
				"You have uncommitted changes — commit them before finishing the session.\n\nCommit message guidance: %s\n\nUncommitted files:\n%s",
				hint, strings.TrimSpace(string(out)),
			)
			output, _ := json.Marshal(map[string]interface{}{
				"decision": "block",
				"reason":   reason,
			})
			os.Stdout.Write(output)
			os.Exit(1)
		}
	}

	// Job mode: enforce that stop_broadcast was called before allowing Claude to exit.
	// Only active when AGENTICS_JOB_MODE=1 (set by pks-cli job runner).
	// Regular interactive users (npx vibecast) are unaffected.
	if os.Getenv("AGENTICS_JOB_MODE") == "1" {
		sockPath := control.ControlSocketPath()
		statusBody, err := control.ControlHTTPRequest(sockPath, "GET", "/status")
		if err == nil {
			var statusResp struct {
				Phase string `json:"phase"`
			}
			if json.Unmarshal([]byte(statusBody), &statusResp) == nil && statusResp.Phase == "live" {
				// stop_broadcast has not been called yet
				count := readStopBlockCount(sf.StreamID)
				if count < 2 {
					writeStopBlockCount(sf.StreamID, count+1)
					reason := "Before finishing, you must call the stop_broadcast MCP tool to finalize the session.\n\n" +
						"This tool records what was accomplished and triggers proper session cleanup.\n\n" +
						"Tool: stop_broadcast\n" +
						"Parameters:\n" +
						"  - message (string): A concise summary of what was accomplished this session\n" +
						"  - conclusion (string): One of \"success\", \"failure\", or \"cancelled\"\n\n" +
						"Example: call stop_broadcast with message=\"Implemented feature X and wrote tests\" conclusion=\"success\"\n\n" +
						"Please call stop_broadcast now, then you may finish."
					output, _ := json.Marshal(map[string]interface{}{
						"decision": "block",
						"reason":   reason,
					})
					os.Stdout.Write(output)
					os.Exit(2)
				}
				// Fallback after 2 blocked attempts: auto-call stop_broadcast as "incomplete"
				stopPayload, _ := json.Marshal(map[string]string{
					"conclusion": "incomplete",
					"message":    "Session ended without explicit stop_broadcast call",
				})
				control.ControlHTTPRequestWithBody(sockPath, "POST", "/stop-broadcast", stopPayload)
			}
		}
	}

	os.Exit(0)
}

func readStopBlockCount(streamID string) int {
	path := filepath.Join(transcriptCursorDir(streamID), "stop_blocks")
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var n int
	fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &n)
	return n
}

func writeStopBlockCount(streamID string, n int) {
	dir := transcriptCursorDir(streamID)
	os.MkdirAll(dir, 0755)
	path := filepath.Join(dir, "stop_blocks")
	os.WriteFile(path, []byte(fmt.Sprintf("%d", n)), 0644)
}

func extractToolUseIDs(lines []map[string]interface{}) []string {
	var ids []string
	for _, line := range lines {
		t, _ := line["type"].(string)
		if t != "assistant" {
			continue
		}
		msg, ok := line["message"].(map[string]interface{})
		if !ok {
			continue
		}
		content, ok := msg["content"].([]interface{})
		if !ok {
			continue
		}
		for _, block := range content {
			blockMap, ok := block.(map[string]interface{})
			if !ok {
				continue
			}
			blockType, _ := blockMap["type"].(string)
			if blockType == "tool_use" {
				if id, ok := blockMap["id"].(string); ok && id != "" {
					ids = append(ids, id)
				}
			}
		}
	}
	return ids
}

func extractAssistantText(lines []map[string]interface{}) string {
	var parts []string
	for _, line := range lines {
		t, _ := line["type"].(string)
		if t != "assistant" {
			continue
		}
		msg, ok := line["message"].(map[string]interface{})
		if !ok {
			continue
		}
		content, ok := msg["content"].([]interface{})
		if !ok {
			continue
		}
		for _, block := range content {
			blockMap, ok := block.(map[string]interface{})
			if !ok {
				continue
			}
			blockType, _ := blockMap["type"].(string)
			if blockType == "text" {
				if text, ok := blockMap["text"].(string); ok && text != "" {
					parts = append(parts, text)
				}
			}
		}
	}
	return strings.Join(parts, "\n")
}
