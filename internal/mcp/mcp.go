package mcp

import (
	"encoding/json"
	"os"
)

// InjectMCPConfig adds the vibecast MCP server to .mcp.json.
func InjectMCPConfig() error {
	mcpPath := ".mcp.json"

	var config map[string]interface{}
	data, err := os.ReadFile(mcpPath)
	if err != nil {
		config = map[string]interface{}{
			"mcpServers": map[string]interface{}{},
		}
	} else {
		if err := json.Unmarshal(data, &config); err != nil {
			config = map[string]interface{}{
				"mcpServers": map[string]interface{}{},
			}
		}
	}

	servers, ok := config["mcpServers"].(map[string]interface{})
	if !ok {
		servers = map[string]interface{}{}
		config["mcpServers"] = servers
	}

	exePath, err := os.Executable()
	if err != nil {
		return err
	}

	servers["vibecast"] = map[string]interface{}{
		"command": exePath,
		"args":    []string{"mcp", "serve"},
	}

	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(mcpPath, out, 0644)
}

// RemoveMCPConfig removes the vibecast MCP server from .mcp.json.
func RemoveMCPConfig() {
	mcpPath := ".mcp.json"
	data, err := os.ReadFile(mcpPath)
	if err != nil {
		return
	}

	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		return
	}

	servers, ok := config["mcpServers"].(map[string]interface{})
	if !ok {
		return
	}

	delete(servers, "vibecast")

	if len(servers) == 0 {
		delete(config, "mcpServers")
	}

	out, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(mcpPath, out, 0644)
}
