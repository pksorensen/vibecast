package mcp

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pksorensen/vibecast/internal/control"
	"github.com/pksorensen/vibecast/internal/hooks"
	"github.com/pksorensen/vibecast/internal/session"
	"github.com/pksorensen/vibecast/internal/types"
)

func execCommand(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return string(out), err
}

func parseID(raw json.RawMessage) interface{} {
	if raw == nil {
		return nil
	}
	var num int
	if err := json.Unmarshal(raw, &num); err == nil {
		return num
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		return str
	}
	return nil
}

// HandleMCPServe implements the "vibecast mcp serve" subcommand.
func HandleMCPServe() {
	sockPath := control.ControlSocketPath()
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	selectedStreamID := os.Getenv("VIBECAST_STREAM_ID")

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req types.JsonrpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}

		var resp *types.JsonrpcResponse

		switch req.Method {
		case "initialize":
			resp = &types.JsonrpcResponse{
				JSONRPC: "2.0",
				ID:      parseID(req.ID),
				Result: map[string]interface{}{
					"protocolVersion": "2024-11-05",
					"capabilities": map[string]interface{}{
						"tools":   map[string]interface{}{},
						"prompts": map[string]interface{}{},
					},
					"serverInfo": map[string]interface{}{
						"name":    "vibecast",
						"version": "0.1.0",
					},
				},
			}

		case "notifications/initialized":
			continue

		case "tools/list":
			resp = &types.JsonrpcResponse{
				JSONRPC: "2.0",
				ID:      parseID(req.ID),
				Result: map[string]interface{}{
					"tools": mcpToolsList(),
				},
			}

		case "prompts/list":
			resp = &types.JsonrpcResponse{
				JSONRPC: "2.0",
				ID:      parseID(req.ID),
				Result: map[string]interface{}{
					"prompts": []interface{}{
						map[string]interface{}{
							"name":        "restart",
							"description": "Restart Claude Code in the broadcast tmux session",
						},
						map[string]interface{}{
							"name":        "status",
							"description": "Get current broadcast status",
						},
						map[string]interface{}{
							"name":        "stop",
							"description": "Stop the broadcast",
						},
					},
				},
			}

		case "prompts/get":
			resp = handleMCPPromptGet(req, sockPath)

		case "tools/call":
			resp = handleMCPToolCall(req, sockPath, &selectedStreamID)

		default:
			if req.ID != nil {
				resp = &types.JsonrpcResponse{
					JSONRPC: "2.0",
					ID:      parseID(req.ID),
					Error:   &types.RPCError{Code: -32601, Message: "Method not found"},
				}
			} else {
				continue
			}
		}

		if resp != nil {
			out, _ := json.Marshal(resp)
			fmt.Fprintf(os.Stdout, "%s\n", out)
		}
	}
}

func mcpToolsList() []interface{} {
	return []interface{}{
		map[string]interface{}{
			"name":        "restart_claude",
			"description": "Restart Claude Code in the broadcast tmux session. The broadcast continues uninterrupted — only Claude is restarted with a fresh context.",
			"inputSchema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		map[string]interface{}{
			"name":        "get_broadcast_status",
			"description": "Get current broadcast status including stream ID, viewer count, uptime, and URL.",
			"inputSchema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		map[string]interface{}{
			"name":        "stop_broadcast",
			"description": "Stop the broadcast and end the session. Optionally include a completion message (e.g. summary of what was accomplished) and conclusion status.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"message": map[string]interface{}{
						"type":        "string",
						"description": "A completion message or summary of what was accomplished. This will be posted as a comment on the associated task.",
					},
					"conclusion": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"success", "failure", "cancelled"},
						"description": "The conclusion status of the session (default: success)",
					},
				},
			},
		},
		map[string]interface{}{
			"name":        "share_image",
			"description": "Share an image with the live broadcast audience. The image will be queued for the stream owner's approval before being shown to viewers.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"image_path": map[string]interface{}{
						"type":        "string",
						"description": "Absolute path to the image file to share",
					},
					"caption": map[string]interface{}{
						"type":        "string",
						"description": "Optional caption describing the image",
					},
				},
				"required": []string{"image_path"},
			},
		},
		map[string]interface{}{
			"name":        "list_sessions",
			"description": "List all running vibecast broadcast sessions.",
			"inputSchema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
		map[string]interface{}{
			"name":        "select_session",
			"description": "Select which vibecast broadcast session to target for subsequent tool calls.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"stream_id": map[string]interface{}{
						"type":        "string",
						"description": "The stream ID of the session to select",
					},
				},
				"required": []string{"stream_id"},
			},
		},
		map[string]interface{}{
			"name":        "change_broadcast_url",
			"description": "Change the broadcast server host mid-session.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"server_host": map[string]interface{}{
						"type":        "string",
						"description": "The server host to connect to (e.g. \"agentics.dk\" or \"localhost:3000\")",
					},
				},
				"required": []string{"server_host"},
			},
		},
		map[string]interface{}{
			"name":        "configure_otel",
			"description": "Dynamically configure OpenTelemetry tracing on the running CLI.",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"endpoint": map[string]interface{}{
						"type":        "string",
						"description": "OTLP HTTP endpoint host:port",
					},
					"insecure": map[string]interface{}{
						"type":    "boolean",
						"description": "Use HTTP instead of HTTPS (default: true)",
						"default": true,
					},
					"service_name": map[string]interface{}{
						"type":        "string",
						"description": "Service name for traces (default: \"vibecast-cli\")",
					},
				},
				"required": []string{"endpoint"},
			},
		},
		map[string]interface{}{
			"name":        "debug_env",
			"description": "List all environment variables visible to the MCP server process.",
			"inputSchema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	}
}

func handleMCPToolCall(req types.JsonrpcRequest, sockPath string, selectedStreamID *string) *types.JsonrpcResponse {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &types.JsonrpcResponse{
			JSONRPC: "2.0",
			ID:      parseID(req.ID),
			Error:   &types.RPCError{Code: -32602, Message: "Invalid params"},
		}
	}

	var resultText string
	var isError bool

	switch params.Name {
	case "restart_claude":
		var args struct {
			ClearContext bool `json:"clearContext"`
		}
		if params.Arguments != nil {
			json.Unmarshal(params.Arguments, &args)
		}
		path := "/restart-claude"
		if args.ClearContext {
			path += "?clearContext=true"
		}
		body, err := control.ControlHTTPRequest(sockPath, "POST", path)
		if err != nil {
			resultText = fmt.Sprintf("Failed to restart Claude: %v", err)
			isError = true
		} else {
			resultText = body
		}

	case "get_broadcast_status":
		body, err := control.ControlHTTPRequest(sockPath, "GET", "/status")
		if err != nil {
			resultText = fmt.Sprintf("Failed to get status: %v", err)
			isError = true
		} else {
			resultText = body
		}

	case "stop_broadcast":
		var args struct {
			Message    string `json:"message"`
			Conclusion string `json:"conclusion"`
		}
		if params.Arguments != nil {
			json.Unmarshal(params.Arguments, &args)
		}

		// Auto-git: refuse to stop if uncommitted changes exist.
		// Excludes .claude/ — those are job-scoped config files written by the runner,
		// not work product. They must not be committed to the repository.
		if os.Getenv("AGENTICS_AUTO_GIT") == "1" {
			cwd, _ := os.Getwd()
			out, err := execCommand("git", "-C", cwd, "status", "--porcelain")
			if err == nil {
				var workLines []string
				for _, line := range strings.Split(out, "\n") {
					// Strip leading status chars and spaces to get the path
					path := strings.TrimSpace(line)
					if len(path) >= 3 {
						path = strings.TrimSpace(path[2:])
					}
					if path == "" || strings.HasPrefix(path, ".claude/") {
						continue
					}
					workLines = append(workLines, line)
				}
				if len(workLines) > 0 {
					hint := os.Getenv("AGENTICS_COMMIT_MESSAGE_HINT")
					if hint == "" {
						hint = "Use semantic commits: feat(scope): description"
					}
					resultText = fmt.Sprintf(
						"Cannot end session: uncommitted changes detected.\n\nCommit your work first. Do NOT commit files under .claude/ — those are job-scoped config files.\nCommit message guidance: %s\n\nUncommitted files:\n%s",
						hint, strings.Join(workLines, "\n"),
					)
					isError = true
					break
				}
			}
		}

		// Check if background agents are still running via tmux pane capture.
		// Claude Code shows "N local agents" in the status bar when subagents are active.
		if tmuxPane := os.Getenv("TMUX_PANE"); tmuxPane != "" {
			if out, err := exec.Command("tmux", "capture-pane", "-p", "-t", tmuxPane).Output(); err == nil {
				if matched, _ := regexp.MatchString(`\d+ local agents?`, string(out)); matched {
					resultText = "Cannot end session: background agents are still running.\n\nRun this bash command to wait for them to finish, then call stop_broadcast again:\n  bash -c 'while tmux capture-pane -p -t $TMUX_PANE 2>/dev/null | grep -qE \"[0-9]+ local agents?\"; do sleep 15; done'\n\nDo NOT retry stop_broadcast immediately — wait for agents to finish first."
					isError = true
					break
				}
			}
		}

		// Auto-git: push committed work back to origin so it lands in the project repo.
		var gitPushError string
		if os.Getenv("AGENTICS_AUTO_GIT") == "1" {
			cwd, _ := os.Getwd()
			branch := "main"
			if out, err := execCommand("git", "-C", cwd, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
				if b := strings.TrimSpace(out); b != "" && b != "HEAD" {
					branch = b
				}
			}
			pushCmd := exec.Command("git", "-C", cwd, "push", "origin", branch)
			if pushOut, err := pushCmd.CombinedOutput(); err != nil {
				gitPushError = fmt.Sprintf("push failed: %v\n%s", err, strings.TrimSpace(string(pushOut)))
				fmt.Fprintf(os.Stderr, "auto-git: %s\n", gitPushError)
			} else {
				fmt.Fprintf(os.Stderr, "auto-git: push ok (branch: %s)\n", branch)
			}
		}

		// Capture latest git commit and branch for task history
		var gitCommit, gitBranch string
		{
			cwd, _ := os.Getwd()
			if out, err := execCommand("git", "-C", cwd, "log", "-1", "--format=%H"); err == nil {
				gitCommit = strings.TrimSpace(out)
			}
			if out, err := execCommand("git", "-C", cwd, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
				gitBranch = strings.TrimSpace(out)
			}
		}

		conclusion := args.Conclusion
		message := args.Message
		if gitPushError != "" {
			// git push failed — escalate to failure so the job/task is marked accordingly
			conclusion = "failure"
			message = fmt.Sprintf("%s\n\n⚠️ git push failed: %s", message, gitPushError)
		}

		stopPayload, _ := json.Marshal(map[string]string{
			"message":      message,
			"conclusion":   conclusion,
			"gitCommit":    gitCommit,
			"gitBranch":    gitBranch,
			"gitPushError": gitPushError,
		})
		body, err := control.ControlHTTPRequestWithBody(sockPath, "POST", "/stop-broadcast", stopPayload)
		if err != nil {
			resultText = fmt.Sprintf("Failed to stop broadcast: %v", err)
			isError = true
		} else {
			resultText = body
		}

	case "share_image":
		var args struct {
			ImagePath string `json:"image_path"`
			Caption   string `json:"caption"`
		}
		if params.Arguments != nil {
			json.Unmarshal(params.Arguments, &args)
		}
		if args.ImagePath == "" {
			resultText = "image_path is required"
			isError = true
			break
		}

		imgData, err := os.ReadFile(args.ImagePath)
		if err != nil {
			resultText = fmt.Sprintf("Failed to read image: %v", err)
			isError = true
			break
		}

		ext := strings.ToLower(filepath.Ext(args.ImagePath))
		mimeType := "image/png"
		switch ext {
		case ".jpg", ".jpeg":
			mimeType = "image/jpeg"
		case ".gif":
			mimeType = "image/gif"
		case ".webp":
			mimeType = "image/webp"
		case ".svg":
			mimeType = "image/svg+xml"
		case ".png":
			mimeType = "image/png"
		}

		idBytes := make([]byte, 4)
		rand.Read(idBytes)
		imageID := fmt.Sprintf("%x", idBytes)

		b64 := base64.StdEncoding.EncodeToString(imgData)
		dataURI := fmt.Sprintf("data:%s;base64,%s", mimeType, b64)

		sf, err := session.ResolveTargetSession(*selectedStreamID)
		if err != nil {
			resultText = err.Error()
			isError = true
			break
		}

		payload := map[string]interface{}{
			"streamId":  sf.StreamID,
			"type":      "metadata",
			"subtype":   "image_share",
			"imageId":   imageID,
			"imageData": dataURI,
			"caption":   args.Caption,
			"status":    "pending",
			"timestamp": time.Now().Unix(),
		}
		payloadBytes, _ := json.Marshal(payload)

		hooks.HookPostMetadata(sf, payloadBytes)

		imgPayload, _ := json.Marshal(map[string]interface{}{
			"imageId":   imageID,
			"imageData": dataURI,
			"caption":   args.Caption,
			"timestamp": time.Now().Unix(),
		})
		control.ControlHTTPRequestWithBody(sockPath, "POST", "/image-queued", imgPayload)

		resultText = fmt.Sprintf("Image queued for approval (id: %s)", imageID)

	case "list_sessions":
		session.CleanStaleSessions()
		active := session.FindAllActiveSessions()
		if len(active) == 0 {
			resultText = "No active vibecast sessions found."
			break
		}
		var lines []string
		for _, sf := range active {
			uptime := time.Since(time.Unix(sf.StartedAt, 0)).Truncate(time.Second)
			selected := ""
			if *selectedStreamID == sf.StreamID {
				selected = " (selected)"
			}
			lines = append(lines, fmt.Sprintf("- **%s**%s — %s · %s · up %s (pid %d)",
				sf.StreamID, selected, sf.ServerHost, sf.Project, uptime, sf.PID))
		}
		resultText = fmt.Sprintf("Active sessions (%d):\n%s", len(active), strings.Join(lines, "\n"))

	case "select_session":
		var args struct {
			StreamID string `json:"stream_id"`
		}
		if params.Arguments != nil {
			json.Unmarshal(params.Arguments, &args)
		}
		if args.StreamID == "" {
			resultText = "stream_id is required"
			isError = true
			break
		}
		sf, err := session.ResolveTargetSession(args.StreamID)
		if err != nil && args.StreamID != "" {
			path := filepath.Join(session.SessionsDir(), args.StreamID+".json")
			sfDirect, readErr := session.ReadSessionFile(path)
			if readErr != nil {
				resultText = fmt.Sprintf("Session %q not found", args.StreamID)
				isError = true
				break
			}
			sf = sfDirect
		}
		*selectedStreamID = sf.StreamID
		resultText = fmt.Sprintf("Selected session: %s (%s · %s)", sf.StreamID, sf.ServerHost, sf.Project)

	case "change_broadcast_url":
		var args struct {
			ServerHost string `json:"server_host"`
		}
		if params.Arguments != nil {
			json.Unmarshal(params.Arguments, &args)
		}
		if args.ServerHost == "" {
			resultText = "server_host is required"
			isError = true
			break
		}
		body, err := control.ControlHTTPRequest(sockPath, "POST", "/change-server?host="+args.ServerHost)
		if err != nil {
			resultText = fmt.Sprintf("Failed to change server: %v", err)
			isError = true
		} else {
			resultText = body
		}

	case "configure_otel":
		var args struct {
			Endpoint    string `json:"endpoint"`
			Insecure    *bool  `json:"insecure"`
			ServiceName string `json:"service_name"`
		}
		if params.Arguments != nil {
			json.Unmarshal(params.Arguments, &args)
		}
		if args.Endpoint == "" {
			resultText = "endpoint is required (e.g. \"localhost:21208\")"
			isError = true
			break
		}
		insecure := true
		if args.Insecure != nil {
			insecure = *args.Insecure
		}
		payload, _ := json.Marshal(map[string]interface{}{
			"endpoint":     args.Endpoint,
			"insecure":     insecure,
			"service_name": args.ServiceName,
		})
		body, err := control.ControlHTTPRequestWithBody(sockPath, "POST", "/configure-otel", payload)
		if err != nil {
			resultText = fmt.Sprintf("Failed to configure OTEL: %v", err)
			isError = true
		} else {
			resultText = fmt.Sprintf("OpenTelemetry configured: %s", body)
		}

	case "debug_env":
		envVars := os.Environ()
		resultText = strings.Join(envVars, "\n")

	default:
		return &types.JsonrpcResponse{
			JSONRPC: "2.0",
			ID:      parseID(req.ID),
			Error:   &types.RPCError{Code: -32602, Message: "Unknown tool: " + params.Name},
		}
	}

	return &types.JsonrpcResponse{
		JSONRPC: "2.0",
		ID:      parseID(req.ID),
		Result: map[string]interface{}{
			"content": []interface{}{
				map[string]interface{}{
					"type": "text",
					"text": resultText,
				},
			},
			"isError": isError,
		},
	}
}

func handleMCPPromptGet(req types.JsonrpcRequest, sockPath string) *types.JsonrpcResponse {
	var params struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &types.JsonrpcResponse{
			JSONRPC: "2.0",
			ID:      parseID(req.ID),
			Error:   &types.RPCError{Code: -32602, Message: "Invalid params"},
		}
	}

	var toolName, description string
	switch params.Name {
	case "restart":
		toolName = "restart_claude"
		description = "Restart Claude Code in the broadcast tmux session."
	case "status":
		toolName = "get_broadcast_status"
		description = "Get current broadcast status."
	case "stop":
		toolName = "stop_broadcast"
		description = "Stop the broadcast entirely."
	default:
		return &types.JsonrpcResponse{
			JSONRPC: "2.0",
			ID:      parseID(req.ID),
			Error:   &types.RPCError{Code: -32602, Message: "Unknown prompt: " + params.Name},
		}
	}

	return &types.JsonrpcResponse{
		JSONRPC: "2.0",
		ID:      parseID(req.ID),
		Result: map[string]interface{}{
			"description": description,
			"messages": []interface{}{
				map[string]interface{}{
					"role": "user",
					"content": map[string]interface{}{
						"type": "text",
						"text": fmt.Sprintf("Use the %s tool from the vibecast MCP server to %s", toolName, description),
					},
				},
			},
		},
	}
}
