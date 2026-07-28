package replay

import (
	"encoding/json"
)

// SmolAgentsAdapter parses smolagents trace datasets.
//
// These datasets typically have a structured format with tool calls
// represented as either direct fields or nested message structures.
// Common column names: "messages", "conversations", "tool_calls".
type SmolAgentsAdapter struct{}

func (a *SmolAgentsAdapter) Name() string { return "smolagents" }

func (a *SmolAgentsAdapter) Extract(row map[string]interface{}) ([]ExtractedEvent, error) {
	// Try multiple known column names
	for _, key := range []string{"messages", "conversations", "steps"} {
		if raw, ok := row[key]; ok {
			events, err := a.extractFromConversation(raw)
			if err == nil && len(events) > 0 {
				return events, nil
			}
		}
	}

	// Try a flat tool_calls column
	if raw, ok := row["tool_calls"]; ok {
		return a.extractFromToolCalls(raw)
	}

	// Try parsing the entire row if it looks like a single message
	if _, ok := row["role"]; ok {
		return a.extractFromSingleMessage(row)
	}

	return nil, nil
}

func (a *SmolAgentsAdapter) extractFromConversation(raw interface{}) ([]ExtractedEvent, error) {
	msgs, ok := raw.([]interface{})
	if !ok {
		// Might be a JSON string
		if s, ok := raw.(string); ok {
			if err := json.Unmarshal([]byte(s), &msgs); err != nil {
				return nil, nil
			}
		} else {
			return nil, nil
		}
	}

	var events []ExtractedEvent
	for _, m := range msgs {
		msg, ok := m.(map[string]interface{})
		if !ok {
			continue
		}

		role, _ := msg["role"].(string)

		// smolagents uses "tool-call" or "assistant" roles with tool info
		switch role {
		case "tool-call", "assistant":
			// Check for tool_calls array
			if tcs, ok := msg["tool_calls"].([]interface{}); ok {
				for _, tc := range tcs {
					if tcMap, ok := tc.(map[string]interface{}); ok {
						event := a.extractToolCall(tcMap)
						if event != nil {
							events = appendCallWithResult(events, *event)
						}
					}
				}
			}
			// Check for direct tool_name/arguments fields
			if name, ok := msg["tool_name"].(string); ok && name != "" {
				event := ExtractedEvent{
					ToolName: name,
					Args:     extractArgs(msg),
				}
				setFileAndURL(&event)
				events = appendCallWithResult(events, event)
			}
			// Check for name + arguments pattern
			if name, ok := msg["name"].(string); ok && name != "" {
				args := make(map[string]string)
				if argsRaw, ok := msg["arguments"]; ok {
					args = parseArguments(argsRaw)
				}
				event := ExtractedEvent{
					ToolName: name,
					Args:     args,
				}
				setFileAndURL(&event)
				events = appendCallWithResult(events, event)
			}
		}
	}

	return events, nil
}

func (a *SmolAgentsAdapter) extractFromToolCalls(raw interface{}) ([]ExtractedEvent, error) {
	tcs, ok := raw.([]interface{})
	if !ok {
		if s, ok := raw.(string); ok {
			if err := json.Unmarshal([]byte(s), &tcs); err != nil {
				return nil, nil
			}
		} else {
			return nil, nil
		}
	}

	var events []ExtractedEvent
	for _, tc := range tcs {
		if tcMap, ok := tc.(map[string]interface{}); ok {
			event := a.extractToolCall(tcMap)
			if event != nil {
				events = appendCallWithResult(events, *event)
			}
		}
	}
	return events, nil
}

func (a *SmolAgentsAdapter) extractFromSingleMessage(row map[string]interface{}) ([]ExtractedEvent, error) {
	name, _ := row["tool_name"].(string)
	if name == "" {
		name, _ = row["name"].(string)
	}
	if name == "" {
		return nil, nil
	}

	event := ExtractedEvent{
		ToolName: name,
		Args:     extractArgs(row),
	}
	setFileAndURL(&event)
	return []ExtractedEvent{event}, nil
}

func (a *SmolAgentsAdapter) extractToolCall(tc map[string]interface{}) *ExtractedEvent {
	// OpenAI-style: function.name + function.arguments
	if fn, ok := tc["function"].(map[string]interface{}); ok {
		name, _ := fn["name"].(string)
		if name == "" {
			return nil
		}
		args := parseArguments(fn["arguments"])
		event := ExtractedEvent{
			ToolName: name,
			Args:     args,
		}
		setFileAndURL(&event)
		return &event
	}

	// Direct style: name + arguments
	name, _ := tc["name"].(string)
	if name == "" {
		name, _ = tc["tool_name"].(string)
	}
	if name == "" {
		return nil
	}
	args := parseArguments(tc["arguments"])
	event := ExtractedEvent{
		ToolName: name,
		Args:     args,
	}
	setFileAndURL(&event)
	return &event
}

// extractArgs pulls string args from a message map.
func extractArgs(msg map[string]interface{}) map[string]string {
	if argsRaw, ok := msg["arguments"]; ok {
		return parseArguments(argsRaw)
	}
	if argsRaw, ok := msg["args"]; ok {
		return parseArguments(argsRaw)
	}
	if argsRaw, ok := msg["input"]; ok {
		if m, ok := argsRaw.(map[string]interface{}); ok {
			return flattenInput(m)
		}
	}
	return map[string]string{}
}

// setFileAndURL populates FilePath and URL from Args.
func setFileAndURL(event *ExtractedEvent) {
	if path := firstOf(event.Args, "path", "file_path", "filePath"); path != "" {
		event.FilePath = path
	}
	if u := firstOf(event.Args, "url", "uri"); u != "" {
		event.URL = u
	}
}

func init() {
	var _ TraceAdapter = (*SmolAgentsAdapter)(nil)
}
