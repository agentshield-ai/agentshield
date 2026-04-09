package replay

import (
	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/google/uuid"
)

// BuildEvaluationRequest converts an ExtractedEvent into an EvaluationRequest
// with correctly populated Fields for Sigma rule matching.
//
// The field enrichment logic mirrors:
//   - plugins/openclaw/src/normalise.ts   (tool name -> command mapping)
//   - internal/server/server.go:454-486   (field population from args)
func BuildEvaluationRequest(event ExtractedEvent) *models.EvaluationRequest {
	args := make(map[string]string)
	fields := make(map[string]string)

	// Map tool name to event_type and command string.
	eventType, command := mapToolCall(event.ToolName, event.Args)

	fields["event_type"] = eventType
	fields["source"] = "huggingface"
	fields["tool"] = event.ToolName

	if command != "" {
		fields["command"] = command
		args["command"] = command
	}
	if event.FilePath != "" {
		fields["file_path"] = event.FilePath
	}
	if event.Content != "" {
		fields["content"] = event.Content
		fields["message"] = event.Content
	}
	if event.URL != "" {
		fields["url.full"] = event.URL
	}

	// Copy all original args into fields (mirrors server.go enrichFields).
	for k, v := range event.Args {
		if _, exists := fields[k]; !exists {
			fields[k] = v
		}
		args[k] = v
	}

	return &models.EvaluationRequest{
		EventID:   uuid.New().String(),
		SessionID: event.SessionID,
		Tool:      event.ToolName,
		Source:    "huggingface",
		Args:      args,
		Fields:    fields,
	}
}

// mapToolCall maps a tool name to an event_type and a command string.
// Handles both Claude Code tool names (Bash, Read, Write, Edit, Glob, Grep)
// and OpenClaw-normalized names (exec, read, write, edit).
func mapToolCall(toolName string, args map[string]string) (eventType string, command string) {
	switch toolName {
	// Execution tools
	case "exec", "Bash", "bash", "terminal", "shell", "computer":
		return "tool_call", args["command"]

	// File read
	case "read", "Read", "cat", "file_read":
		return "file_read", "Read: " + firstOf(args, "path", "file_path", "filePath")

	// File write
	case "write", "Write", "file_write", "create":
		return "file_write", "Write: " + firstOf(args, "path", "file_path", "filePath")

	// File edit
	case "edit", "Edit", "str_replace_editor":
		return "file_edit", "Edit: " + firstOf(args, "path", "file_path", "filePath")

	// Search tools (Claude Code specific)
	case "Glob", "glob":
		return "tool_call", "Glob: " + firstOf(args, "pattern", "path")
	case "Grep", "grep":
		return "tool_call", "Grep: " + firstOf(args, "pattern", "query")

	// Browser / web
	case "browser", "web_browser":
		return "browser_action", args["action"] + ": " + args["url"]
	case "web_fetch", "fetch", "http", "WebFetch":
		return "content_retrieval", args["url"]

	// Messaging
	case "message":
		return "message_send", "Message to " + args["channel"]

	// Agent spawning
	case "sessions_spawn", "Agent":
		return "agent_creation", "Spawn: " + firstOf(args, "agentId", "agent_id")

	default:
		return "tool_call", toolName
	}
}

// firstOf returns the value of the first key found in the map, or "".
func firstOf(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != "" {
			return v
		}
	}
	return ""
}
