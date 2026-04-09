package replay

import (
	"encoding/json"
	"fmt"
	"log/slog"
)

// NlileAdapter parses the nlile/misc-merged-claude-code-traces-v1 dataset.
//
// The dataset has columns: user_prompt, assistant_response, messages_json, has_empty_response.
// messages_json contains a JSON array of messages in the Claude API format:
//
//	[{"role":"user","content":"..."}, {"role":"assistant","content":[...blocks...]}]
//
// Assistant messages may contain structured content blocks including tool_use:
//
//	{"type":"tool_use","id":"...","name":"Bash","input":{"command":"ls -la"}}
//
// And tool_result blocks in user messages:
//
//	{"type":"tool_result","tool_use_id":"...","content":"output text"}
type NlileAdapter struct{}

func (a *NlileAdapter) Name() string { return "nlile" }

func (a *NlileAdapter) Extract(row map[string]interface{}) ([]ExtractedEvent, error) {
	messagesRaw, ok := row["messages_json"]
	if !ok {
		// Fall back to user_prompt + assistant_response (simpler format)
		return a.extractFromPromptResponse(row)
	}

	messagesStr, ok := messagesRaw.(string)
	if !ok {
		return nil, fmt.Errorf("messages_json is not a string")
	}

	var messages []nlileMessage
	if err := json.Unmarshal([]byte(messagesStr), &messages); err != nil {
		return nil, fmt.Errorf("parsing messages_json: %w", err)
	}

	var events []ExtractedEvent
	// We need to track tool_use IDs to pair them with tool_result content.
	toolResults := make(map[string]string) // tool_use_id -> result content

	// First pass: collect tool results
	for _, msg := range messages {
		if msg.Role != "user" {
			continue
		}
		blocks, ok := parseContentBlocks(msg.Content)
		if !ok {
			continue
		}
		for _, block := range blocks {
			if block.Type == "tool_result" && block.ToolUseID != "" {
				content := extractTextContent(block.Content)
				if content != "" {
					toolResults[block.ToolUseID] = content
				}
			}
		}
	}

	// Second pass: extract tool_use events
	for _, msg := range messages {
		if msg.Role != "assistant" {
			continue
		}
		blocks, ok := parseContentBlocks(msg.Content)
		if !ok {
			continue
		}
		for _, block := range blocks {
			if block.Type != "tool_use" || block.Name == "" {
				continue
			}
			event := ExtractedEvent{
				ToolName: block.Name,
				Args:     flattenInput(block.Input),
			}

			// Set file_path for read/write/edit tools
			if path := firstOf(event.Args, "path", "file_path", "filePath"); path != "" {
				event.FilePath = path
			}
			// Set URL for web tools
			if u := firstOf(event.Args, "url", "uri"); u != "" {
				event.URL = u
			}
			// Attach tool result content if available
			if block.ID != "" {
				if resultContent, ok := toolResults[block.ID]; ok {
					event.Content = truncate(resultContent, 10000)
				}
			}
			events = append(events, event)
		}
	}

	return events, nil
}

// extractFromPromptResponse handles the simpler case where messages_json is absent.
func (a *NlileAdapter) extractFromPromptResponse(row map[string]interface{}) ([]ExtractedEvent, error) {
	respRaw, ok := row["assistant_response"]
	if !ok {
		return nil, nil
	}
	respStr, ok := respRaw.(string)
	if !ok || respStr == "" {
		return nil, nil
	}

	// Try to parse the response as JSON blocks (some traces store structured content)
	var blocks []nlileContentBlock
	if err := json.Unmarshal([]byte(respStr), &blocks); err != nil {
		// Not structured — no tool calls to extract
		return nil, nil
	}

	var events []ExtractedEvent
	for _, block := range blocks {
		if block.Type != "tool_use" || block.Name == "" {
			continue
		}
		event := ExtractedEvent{
			ToolName: block.Name,
			Args:     flattenInput(block.Input),
		}
		if path := firstOf(event.Args, "path", "file_path", "filePath"); path != "" {
			event.FilePath = path
		}
		if u := firstOf(event.Args, "url", "uri"); u != "" {
			event.URL = u
		}
		events = append(events, event)
	}
	return events, nil
}

// nlileMessage is a message in the Claude API format.
type nlileMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string or []contentBlock
}

// nlileContentBlock is a content block within a message.
type nlileContentBlock struct {
	Type      string                 `json:"type"`
	ID        string                 `json:"id,omitempty"`
	Name      string                 `json:"name,omitempty"`
	Text      string                 `json:"text,omitempty"`
	Input     map[string]interface{} `json:"input,omitempty"`
	ToolUseID string                 `json:"tool_use_id,omitempty"`
	Content   interface{}            `json:"content,omitempty"` // tool_result content
}

// parseContentBlocks handles the polymorphic content field: either a string or []block.
func parseContentBlocks(content interface{}) ([]nlileContentBlock, bool) {
	if content == nil {
		return nil, false
	}
	// If it's already a slice, try to decode
	switch v := content.(type) {
	case []interface{}:
		var blocks []nlileContentBlock
		data, err := json.Marshal(v)
		if err != nil {
			return nil, false
		}
		if err := json.Unmarshal(data, &blocks); err != nil {
			return nil, false
		}
		return blocks, true
	case string:
		// Plain text content — no tool calls
		return nil, false
	}
	return nil, false
}

// extractTextContent extracts text from a tool_result content field,
// which can be a string or a list of {type:"text", text:"..."} blocks.
func extractTextContent(content interface{}) string {
	if content == nil {
		return ""
	}
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				if m["type"] == "text" {
					if text, ok := m["text"].(string); ok {
						return text
					}
				}
			}
		}
	}
	return ""
}

// flattenInput converts a map[string]interface{} to map[string]string.
func flattenInput(input map[string]interface{}) map[string]string {
	if input == nil {
		return map[string]string{}
	}
	result := make(map[string]string, len(input))
	for k, v := range input {
		switch val := v.(type) {
		case string:
			result[k] = val
		case float64:
			result[k] = fmt.Sprintf("%g", val)
		case bool:
			if val {
				result[k] = "true"
			} else {
				result[k] = "false"
			}
		case nil:
			continue
		default:
			// Marshal complex types to JSON
			data, err := json.Marshal(val)
			if err != nil {
				slog.Debug("Skipping non-serializable field", "key", k)
				continue
			}
			result[k] = string(data)
		}
	}
	return result
}

// truncate limits a string to maxLen bytes.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
