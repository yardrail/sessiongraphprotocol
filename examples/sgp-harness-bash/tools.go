package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func toolDefinitions() []chatTool {
	return []chatTool{
		{
			Type: "function",
			Function: chatToolDef{
				Name:        "read_file",
				Description: "Read the contents of a file at the given path and return them as text.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Absolute or relative path to the file to read",
						},
					},
					"required": []string{"path"},
				},
			},
		},
		{
			Type: "function",
			Function: chatToolDef{
				Name:        "teleport",
				Description: "Transfer this session to another harness binary. Pass the absolute path to the destination binary as destination. Call this tool alone, not combined with other tools.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"destination": map[string]any{
							"type":        "string",
							"description": "Absolute path to the destination harness binary",
						},
					},
					"required": []string{"destination"},
				},
			},
		},
	}
}

func executeTool(_ context.Context, name, argsJSON string) (string, bool) {
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return fmt.Sprintf("invalid arguments: %v", err), false
	}

	switch name {
	case "read_file":
		return execReadFile(args)
	default:
		return "unknown tool: " + name, false
	}
}

func execReadFile(args map[string]any) (string, bool) {
	path, ok := args["path"].(string)
	if !ok || strings.TrimSpace(path) == "" {
		return "read_file: path argument is required", false
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("read_file: %v", err), false
	}

	const maxOutput = 4000
	output := string(data)
	if len(output) > maxOutput {
		output = output[:maxOutput] + "\n[output truncated]"
	}

	return output, true
}
