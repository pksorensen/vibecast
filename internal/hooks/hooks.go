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
	"regexp"
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
		fmt.Fprintf(os.Stderr, "usage: vibecast hook <prompt|session|tool|post-tool|subagent-start|subagent-stop|stop|permission-request|pre-compact|post-compact>\n")
		os.Exit(1)
	}

	// Reconstruct parent context from W3C traceparent injected by the stream into the tmux session.
	// This connects hook spans as children of the vibecast.stream.start span.
	parentCtx := telemetry.ContextFromTraceparent(os.Getenv("TRACEPARENT"))
	ctx, span := telemetry.Tracer().Start(parentCtx, "vibecast.hook",
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
	case "task-created":
		handleHookTaskCreated()
	case "task-completed":
		handleHookTaskCompleted()
	case "stop":
		handleHookStop()
	case "permission-request":
		handleHookPermissionRequest()
	case "pre-compact":
		handleHookPreCompact()
	case "post-compact":
		handleHookPostCompact()
	default:
		fmt.Fprintf(os.Stderr, "usage: vibecast hook <prompt|session|tool|post-tool|subagent-start|subagent-stop|stop|permission-request|pre-compact|post-compact>\n")
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

	util.DebugLog("hookReadStdin: found session sessionId=%s, claudeSessionId=%s", sf.SessionID, base.SessionID)
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
			attribute.String("session.id", sf.SessionID),
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
		"sessionId": sf.SessionID,
		"type":      "metadata",
		"subtype":   "prompt",
		"prompt":    hookInput.Prompt,
		"timestamp": time.Now().Unix(),
	}

	if tl := readTranscriptIncrement(sf.SessionID, transcriptPath); len(tl) > 0 {
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
		"sessionId":       sf.SessionID,
		"type":            "metadata",
		"subtype":         "session_start",
		"source":          hookInput.Source,
		"claudeSessionId": claudeSessionId,
		"timestamp":       time.Now().Unix(),
	}

	if summary := readFirstUserPrompt(transcriptPath); summary != "" {
		p["sessionSummary"] = summary
	}

	if tl := readTranscriptIncrement(sf.SessionID, transcriptPath); len(tl) > 0 {
		p["transcriptLines"] = tl
	}

	payload, _ := json.Marshal(p)
	HookPostMetadata(sf, payload)

	broadcastID := sf.BroadcastID
	if broadcastID == "" {
		broadcastID = sf.SessionID
	}
	viewerURL := util.BuildViewerURL(sf.ServerHost, broadcastID)
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

	// Job mode: block writes/edits outside the allowed job work tree.
	// VIBECAST_ALLOWED_DIRECTORIES is set by the runner to the job work tree path.
	if os.Getenv("AGENTICS_JOB_MODE") == "1" {
		if allowedDir := os.Getenv("VIBECAST_ALLOWED_DIRECTORIES"); allowedDir != "" {
			// Extract file path from Write, Edit, MultiEdit, NotebookEdit tool inputs
			var filePath string
			switch hookInput.ToolName {
			case "Write", "Edit", "MultiEdit":
				var inp struct {
					FilePath string `json:"file_path"`
				}
				json.Unmarshal(hookInput.ToolInput, &inp)
				filePath = inp.FilePath
			case "NotebookEdit":
				var inp struct {
					NotebookPath string `json:"notebook_path"`
				}
				json.Unmarshal(hookInput.ToolInput, &inp)
				filePath = inp.NotebookPath
			}
			if filePath != "" {
				abs, err := filepath.Abs(filePath)
				if err == nil {
					allowed := filepath.Clean(allowedDir)
					// Block if the resolved path is not inside the job work tree
					if abs != allowed && !strings.HasPrefix(abs, allowed+string(filepath.Separator)) {
						reason := fmt.Sprintf(
							"Access denied: %s is outside the job work tree.\n\nThis job is restricted to: %s\n\nAll file modifications must be made within the job work tree.",
							abs, allowed,
						)
						output, _ := json.Marshal(map[string]interface{}{
							"decision": "block",
							"reason":   reason,
						})
						os.Stdout.Write(output)
						os.Exit(1)
					}
				}
			}
		}
	}

	if hookInput.ToolName == "ExitPlanMode" {
		var planInput struct {
			Plan string `json:"plan"`
		}
		if len(hookInput.ToolInput) > 0 {
			json.Unmarshal(hookInput.ToolInput, &planInput)
		}
		if planInput.Plan != "" {
			planPayload, _ := json.Marshal(map[string]interface{}{
				"sessionId":    sf.SessionID,
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
		"sessionId":       sf.SessionID,
		"type":            "metadata",
		"subtype":         "tool_use",
		"toolName":        hookInput.ToolName,
		"toolInput":       toolInput,
		"toolUseId":       hookInput.ToolUseID,
		"claudeSessionId": claudeSessionId,
		"transcriptPath":  transcriptPath,
		"timestamp":       time.Now().Unix(),
	}

	if tl := readTranscriptIncrement(sf.SessionID, transcriptPath); len(tl) > 0 {
		p["transcriptLines"] = tl
	}

	payload, _ := json.Marshal(p)
	HookPostMetadata(sf, payload)
	os.Exit(0)
}

func handleHookPostTool() {
	util.DebugLog("[post-tool] checkpoint A: entry")
	stdinData, sf, _, transcriptPath, claudeSessionId := hookReadStdinAndFindSession()
	util.DebugLog("[post-tool] checkpoint B: stdin read, %d bytes, sessionId=%s", len(stdinData), sf.SessionID)

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

	transcriptLines := readTranscriptIncrement(sf.SessionID, transcriptPath)
	usage := extractUsageFromTranscript(transcriptLines)

	p := map[string]interface{}{
		"sessionId":       sf.SessionID,
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
		ToolInput struct {
			Prompt       string `json:"prompt"`
			Description  string `json:"description"`
			SubagentType string `json:"subagent_type"`
		} `json:"tool_input"`
	}
	if err := json.Unmarshal(stdinData, &hookInput); err != nil {
		os.Exit(0)
	}

	// Resolve subagent prompt suffix (env var or file)
	suffix := os.Getenv("SUBAGENT_PROMPT_SUFFIX")
	if suffix == "" {
		if f := os.Getenv("SUBAGENT_PROMPT_SUFFIX_FILE"); f != "" {
			if b, err := os.ReadFile(f); err == nil {
				suffix = strings.TrimSpace(string(b))
			}
		}
	}

	p := map[string]interface{}{
		"sessionId":      sf.SessionID,
		"type":           "metadata",
		"subtype":        "subagent_start",
		"agentId":        hookInput.AgentID,
		"agentType":      hookInput.AgentType,
		"prompt":         hookInput.ToolInput.Prompt,
		"description":    hookInput.ToolInput.Description,
		"subagentType":   hookInput.ToolInput.SubagentType,
		"promptSuffix":   suffix,
		"transcriptPath": transcriptPath,
		"timestamp":      time.Now().Unix(),
	}

	if tl := readTranscriptIncrement(sf.SessionID, transcriptPath); len(tl) > 0 {
		p["transcriptLines"] = tl
	}

	payload, _ := json.Marshal(p)
	HookPostMetadata(sf, payload)

	// Inject additionalContext into the subagent if a suffix is configured
	if suffix != "" {
		out, _ := json.Marshal(map[string]interface{}{
			"additionalContext": suffix,
		})
		os.Stdout.Write(out)
	}

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

	transcriptLines := readTranscriptIncrement(sf.SessionID, transcriptPath)
	agentTranscriptLines := readTranscriptIncrement(sf.SessionID, hookInput.AgentTranscriptPath)
	agentPrompt := readFirstUserPrompt(hookInput.AgentTranscriptPath)

	p := map[string]interface{}{
		"sessionId":           sf.SessionID,
		"type":                "metadata",
		"subtype":             "subagent_stop",
		"agentId":             hookInput.AgentID,
		"agentType":           hookInput.AgentType,
		"transcriptPath":      transcriptPath,
		"agentTranscriptPath": hookInput.AgentTranscriptPath,
		"prompt":              agentPrompt,
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

func handleHookTaskCreated() {
	stdinData, sf, _, _, _ := hookReadStdinAndFindSession()

	var hookInput struct {
		TaskID          string `json:"task_id"`
		TaskSubject     string `json:"task_subject"`
		TaskDescription string `json:"task_description"`
		TeammateName    string `json:"teammate_name"`
		TeamName        string `json:"team_name"`
	}
	if err := json.Unmarshal(stdinData, &hookInput); err != nil {
		os.Exit(0)
	}

	p := map[string]interface{}{
		"sessionId":       sf.SessionID,
		"type":            "metadata",
		"subtype":         "task_created",
		"taskId":          hookInput.TaskID,
		"taskSubject":     hookInput.TaskSubject,
		"taskDescription": hookInput.TaskDescription,
		"teammateName":    hookInput.TeammateName,
		"teamName":        hookInput.TeamName,
		"timestamp":       time.Now().Unix(),
	}

	payload, _ := json.Marshal(p)
	HookPostMetadata(sf, payload)
	os.Exit(0)
}

func handleHookTaskCompleted() {
	stdinData, sf, _, _, _ := hookReadStdinAndFindSession()

	var hookInput struct {
		TaskID       string `json:"task_id"`
		TaskSubject  string `json:"task_subject"`
		TeammateName string `json:"teammate_name"`
		TeamName     string `json:"team_name"`
	}
	if err := json.Unmarshal(stdinData, &hookInput); err != nil {
		os.Exit(0)
	}

	p := map[string]interface{}{
		"sessionId":    sf.SessionID,
		"type":         "metadata",
		"subtype":      "task_completed",
		"taskId":       hookInput.TaskID,
		"taskSubject":  hookInput.TaskSubject,
		"teammateName": hookInput.TeammateName,
		"teamName":     hookInput.TeamName,
		"timestamp":    time.Now().Unix(),
	}

	payload, _ := json.Marshal(p)
	HookPostMetadata(sf, payload)
	os.Exit(0)
}

func handleHookStop() {
	_, sf, cwd, transcriptPath, _ := hookReadStdinAndFindSession()

	transcriptLines := readTranscriptIncrement(sf.SessionID, transcriptPath)

	text := extractAssistantText(transcriptLines)
	usage := extractUsageFromTranscript(transcriptLines)

	if text != "" || usage != nil {
		p := map[string]interface{}{
			"sessionId": sf.SessionID,
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
		// Check if background agents are still running.
		// Sleep 60s before blocking — the tmux pane won't update while the hook is
		// blocking Claude, so we give agents time to finish between hook invocations.
		if tmuxPane := os.Getenv("TMUX_PANE"); tmuxPane != "" {
			if out, err := exec.Command("tmux", "capture-pane", "-p", "-t", tmuxPane).Output(); err == nil {
				if matched, _ := regexp.MatchString(`\d+ local agents`, string(out)); matched {
					time.Sleep(60 * time.Second)
					reason := "Background agents are still running. Waiting for them to complete."
					output, _ := json.Marshal(map[string]interface{}{
						"decision": "block",
						"reason":   reason,
					})
					os.Stdout.Write(output)
					os.Exit(2)
				}
			}
		}

		sockPath := control.ControlSocketPath()
		statusBody, err := control.ControlHTTPRequest(sockPath, "GET", "/status")
		if err == nil {
			var statusResp struct {
				Phase string `json:"phase"`
			}
			if json.Unmarshal([]byte(statusBody), &statusResp) == nil && statusResp.Phase == "live" {
				// stop_broadcast has not been called yet
				count := readStopBlockCount(sf.SessionID)
				if count < 2 {
					writeStopBlockCount(sf.SessionID, count+1)
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

func handleHookPermissionRequest() {
	stdinData, sf, _, _, _ := hookReadStdinAndFindSession()

	var hookInput struct {
		ToolName              string          `json:"tool_name"`
		ToolInput             json.RawMessage `json:"tool_input"`
		ToolUseID             string          `json:"tool_use_id"`
		PermissionSuggestions json.RawMessage `json:"permission_suggestions"`
	}
	if err := json.Unmarshal(stdinData, &hookInput); err != nil {
		os.Exit(0)
	}

	var toolInput interface{}
	if len(hookInput.ToolInput) > 0 {
		json.Unmarshal(hookInput.ToolInput, &toolInput)
	}

	// Build a short label for the vote question (e.g. "Write(.claude/agents/foo.md)")
	question := hookInput.ToolName
	if ti, ok := toolInput.(map[string]interface{}); ok {
		for _, k := range []string{"file_path", "path", "command"} {
			if v, ok := ti[k].(string); ok && v != "" {
				question = hookInput.ToolName + "(" + v + ")"
				break
			}
		}
	}

	util.DebugLog("[permission-request] toolName=%s toolUseId=%s sessionId=%s question=%s", hookInput.ToolName, hookInput.ToolUseID, sf.SessionID, question)

	// Answering AskUserQuestion / AskFollowupQuestion is the implicit approval — there is
	// nothing for the team to vote on. Skip posting to avoid cluttering the chat.
	if hookInput.ToolName == "AskUserQuestion" || hookInput.ToolName == "AskFollowupQuestion" {
		os.Exit(0)
	}

	// Post to metadata — creates the vote record on the server and broadcasts a vote card to chat.
	// Then block and poll GET /question-vote until team resolves the vote (or 30s timeout).
	// If team votes Deny: output {"decision":"deny"} + exit 1. Allow or timeout: exit 0.
	//
	// When Claude Code doesn't supply a tool_use_id (common for PreToolUse permission hooks),
	// we generate a synthetic one using the same perm-<streamId>-<ms> format the server uses
	// as a fallback. Sending it ourselves ensures the server uses OUR id — so we can poll for it.
	syntheticId := hookInput.ToolUseID == ""
	toolUseId := hookInput.ToolUseID
	if toolUseId == "" {
		toolUseId = fmt.Sprintf("perm-%s-%d", sf.SessionID, time.Now().UnixMilli())
	}

	// Root span for the entire permission lifecycle — visible in Aspire traces.
	hookStart := time.Now()
	_, span := telemetry.Tracer().Start(
		telemetry.ContextFromTraceparent(os.Getenv("TRACEPARENT")),
		"vibecast.permission_request",
		trace.WithAttributes(
			attribute.String("session.id", sf.SessionID),
			attribute.String("tool.name", hookInput.ToolName),
			attribute.String("tool.use_id", toolUseId),
			attribute.String("question", question),
			attribute.Bool("synthetic_id", syntheticId),
		),
	)
	defer span.End()

	p := map[string]interface{}{
		"sessionId": sf.SessionID,
		"type":      "metadata",
		"subtype":   "permission_request",
		"toolName":  hookInput.ToolName,
		"toolInput": toolInput,
		"toolUseId": toolUseId,
		"question":  question,
		"timestamp": time.Now().Unix(),
	}
	if len(hookInput.PermissionSuggestions) > 0 {
		var ps interface{}
		json.Unmarshal(hookInput.PermissionSuggestions, &ps)
		p["permissionSuggestions"] = ps
	}
	payload, _ := json.Marshal(p)
	HookPostMetadata(sf, payload)
	span.AddEvent("metadata_posted", trace.WithAttributes(attribute.String("tool.use_id", toolUseId)))
	util.DebugLog("[permission-request] metadata posted toolUseId=%s syntheticId=%v", toolUseId, syntheticId)

	// Poll for the resolved vote (2s interval, 31s total — slightly longer than the 30s voteDeadline
	// so the server's auto-resolve fires first and the hook sees a resolved answer before timing out).
	scheme := "https"
	if util.IsLocalHost(sf.ServerHost) {
		scheme = "http"
	}
	voteURL := fmt.Sprintf("%s://%s/api/lives/question-vote?sessionId=%s&toolUseId=%s",
		scheme, sf.ServerHost, sf.SessionID, toolUseId)
	util.DebugLog("[permission-request] polling voteURL=%s", voteURL)

	deadline := time.Now().Add(31 * time.Second)
	attempt := 0
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		attempt++
		elapsedMs := time.Since(hookStart).Milliseconds()

		_, pollSpan := telemetry.Tracer().Start(context.Background(), "vibecast.permission_request.poll",
			trace.WithAttributes(
				attribute.String("session.id", sf.SessionID),
				attribute.String("tool.use_id", toolUseId),
				attribute.Int("poll.attempt", attempt),
				attribute.Int64("poll.elapsed_ms", elapsedMs),
			),
		)

		req, err := http.NewRequest("GET", voteURL, nil)
		if err != nil {
			pollSpan.RecordError(err)
			pollSpan.SetAttributes(attribute.String("poll.error", err.Error()))
			pollSpan.End()
			util.DebugLog("[permission-request] poll #%d request error: %v", attempt, err)
			continue
		}
		if token, _, authErr := auth.GetValidToken(); authErr == nil && token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			pollSpan.RecordError(err)
			pollSpan.SetAttributes(attribute.String("poll.error", err.Error()))
			pollSpan.End()
			util.DebugLog("[permission-request] poll #%d http error: %v", attempt, err)
			continue
		}
		httpStatus := resp.StatusCode
		var vote struct {
			ResolvedAnswer *string `json:"resolvedAnswer"`
			VoteDeadline   int64   `json:"voteDeadline"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&vote)
		resp.Body.Close()

		pollSpan.SetAttributes(
			attribute.Int("poll.http_status", httpStatus),
			attribute.Bool("poll.resolved", vote.ResolvedAnswer != nil),
		)
		if vote.ResolvedAnswer != nil {
			pollSpan.SetAttributes(attribute.String("poll.answer", *vote.ResolvedAnswer))
		}
		if decodeErr != nil {
			pollSpan.RecordError(decodeErr)
			pollSpan.SetAttributes(attribute.String("poll.decode_error", decodeErr.Error()))
			pollSpan.End()
			util.DebugLog("[permission-request] poll #%d decode error (status=%d): %v", attempt, httpStatus, decodeErr)
			continue
		}
		pollSpan.End()
		util.DebugLog("[permission-request] poll #%d status=%d resolved=%v answer=%v elapsed=%dms",
			attempt, httpStatus, vote.ResolvedAnswer != nil,
			func() string {
				if vote.ResolvedAnswer != nil {
					return *vote.ResolvedAnswer
				}
				return "<nil>"
			}(),
			elapsedMs,
		)

		if vote.ResolvedAnswer != nil {
			decision := *vote.ResolvedAnswer
			span.SetAttributes(
				attribute.String("decision", decision),
				attribute.String("decision.source", "team_vote"),
				attribute.Int("poll.final_attempt", attempt),
				attribute.Int64("poll.total_elapsed_ms", time.Since(hookStart).Milliseconds()),
			)
			span.AddEvent("vote_resolved", trace.WithAttributes(attribute.String("answer", decision)))
			util.DebugLog("[permission-request] resolved toolUseId=%s answer=%s attempt=%d", toolUseId, decision, attempt)
			if strings.EqualFold(decision, "deny") {
				span.SetStatus(codes.Error, "permission denied by team")
				out, _ := json.Marshal(map[string]interface{}{
					"decision": "deny",
					"reason":   "Team voted to deny this operation.",
				})
				os.Stdout.Write(out)
				os.Exit(1)
			}
			// Allow (or any answer that isn't "deny")
			os.Exit(0)
		}
	}

	// Timeout — allow by default so Claude isn't stuck indefinitely.
	span.SetAttributes(
		attribute.String("decision", "allow"),
		attribute.String("decision.source", "timeout"),
		attribute.Int("poll.total_attempts", attempt),
		attribute.Int64("poll.total_elapsed_ms", time.Since(hookStart).Milliseconds()),
	)
	span.AddEvent("permission_timed_out")
	util.DebugLog("[permission-request] vote timed out after %d polls, allowing by default toolUseId=%s", attempt, toolUseId)
	os.Exit(0)
}

func handleHookPreCompact() {
	stdinData, sf, _, transcriptPath, _ := hookReadStdinAndFindSession()

	var hookInput struct {
		Trigger            string `json:"trigger"`             // "auto" | "manual"
		CustomInstructions string `json:"custom_instructions"` // user's compact instructions, if any
	}
	json.Unmarshal(stdinData, &hookInput)

	_, span := telemetry.Tracer().Start(context.Background(), "vibecast.compact",
		trace.WithAttributes(
			attribute.String("session.id", sf.SessionID),
			attribute.String("compact.trigger", hookInput.Trigger),
		))
	// Span ends in post-compact — we stash the span context so it can be resumed.
	// For simplicity we just end the start span here and emit a separate end span in post-compact.
	span.End()

	p := map[string]interface{}{
		"sessionId": sf.SessionID,
		"type":      "metadata",
		"subtype":   "pre_compact",
		"trigger":   hookInput.Trigger,
		"timestamp": time.Now().Unix(),
	}
	if hookInput.CustomInstructions != "" {
		p["customInstructions"] = hookInput.CustomInstructions
	}
	if tl := readTranscriptIncrement(sf.SessionID, transcriptPath); len(tl) > 0 {
		p["transcriptLines"] = tl
	}

	payload, _ := json.Marshal(p)
	HookPostMetadata(sf, payload)
	os.Exit(0)
}

func handleHookPostCompact() {
	stdinData, sf, _, transcriptPath, _ := hookReadStdinAndFindSession()

	var hookInput struct {
		Summary string `json:"summary"` // the compact summary text Claude produced
	}
	json.Unmarshal(stdinData, &hookInput)

	_, span := telemetry.Tracer().Start(context.Background(), "vibecast.compact.end",
		trace.WithAttributes(
			attribute.String("session.id", sf.SessionID),
			attribute.Bool("has_summary", hookInput.Summary != ""),
		))
	span.End()

	p := map[string]interface{}{
		"sessionId": sf.SessionID,
		"type":      "metadata",
		"subtype":   "post_compact",
		"timestamp": time.Now().Unix(),
	}
	if hookInput.Summary != "" {
		// Truncate to avoid huge payloads — first 500 chars is plenty for the activity log.
		summary := hookInput.Summary
		if len(summary) > 500 {
			summary = summary[:500] + "…"
		}
		p["summary"] = summary
	}
	if tl := readTranscriptIncrement(sf.SessionID, transcriptPath); len(tl) > 0 {
		p["transcriptLines"] = tl
	}

	payload, _ := json.Marshal(p)
	HookPostMetadata(sf, payload)
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
