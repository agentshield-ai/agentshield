package replay

import (
	"encoding/json"
	"fmt"
)

// WildClawAdapter parses the sammshen/wildclaw-opus-traces dataset.
//
// Each row contains HTTP-level request/response pairs in JSONL format.
// Requests have the structure:
//
//	{"type":"request", "body":{"model":"...", "messages":[...], "tools":[...]}}
//
// Assistant messages within the body contain tool_calls:
//
//	{"role":"assistant", "content":[...], "tool_calls":[
//	  {"id":"...", "type":"function", "function":{"name":"exec", "arguments":"{...}"}}
//	]}
type WildClawAdapter struct{}

func (a *WildClawAdapter) Name() string { return "wildclaw" }

func (a *WildClawAdapter) Extract(row map[string]interface{}) ([]ExtractedEvent, error) {
	// The WildClaw dataset may store traces as a JSON string or as structured data.
	// Try multiple known column layouts.
	var events []ExtractedEvent

	// Layout 1: row has a "messages" field directly (pre-parsed)
	if msgs, ok := row["messages"]; ok {
		return a.extractFromMessages(msgs)
	}

	// Layout 2: row has a "body" field with request body
	if body, ok := row["body"]; ok {
		return a.extractFromBody(body)
	}

	// Layout 3: row is a JSON string (raw JSONL line)
	for _, key := range []string{"text", "content", "data"} {
		if raw, ok := row[key]; ok {
			if s, ok := raw.(string); ok {
				var parsed map[string]interface{}
				if err := json.Unmarshal([]byte(s), &parsed); err == nil {
					return a.Extract(parsed) // recursive with parsed data
				}
			}
		}
	}

	if len(events) == 0 {
		return nil, nil
	}
	return events, nil
}

func (a *WildClawAdapter) extractFromBody(body interface{}) ([]ExtractedEvent, error) {
	bodyMap, ok := body.(map[string]interface{})
	if !ok {
		// Body might be a JSON string
		if s, ok := body.(string); ok {
			if err := json.Unmarshal([]byte(s), &bodyMap); err != nil {
				return nil, nil
			}
		} else {
			return nil, nil
		}
	}
	msgs, ok := bodyMap["messages"]
	if !ok {
		return nil, nil
	}
	return a.extractFromMessages(msgs)
}

func (a *WildClawAdapter) extractFromMessages(msgs interface{}) ([]ExtractedEvent, error) {
	msgSlice, ok := msgs.([]interface{})
	if !ok {
		return nil, nil
	}

	var events []ExtractedEvent
	// Collect tool results for content enrichment
	toolResults := make(map[string]string)

	// First pass: collect tool results
	for _, m := range msgSlice {
		msg, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		if msg["role"] != "tool" {
			continue
		}
		if toolCallID, ok := msg["tool_call_id"].(string); ok {
			if content, ok := msg["content"].(string); ok {
				toolResults[toolCallID] = content
			}
		}
	}

	// Second pass: extract tool_calls from assistant messages
	for _, m := range msgSlice {
		msg, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		if msg["role"] != "assistant" {
			continue
		}

		toolCalls, ok := msg["tool_calls"].([]interface{})
		if !ok {
			continue
		}

		for _, tc := range toolCalls {
			tcMap, ok := tc.(map[string]interface{})
			if !ok {
				continue
			}
			fn, ok := tcMap["function"].(map[string]interface{})
			if !ok {
				continue
			}
			name, _ := fn["name"].(string)
			if name == "" {
				continue
			}

			// Parse function arguments (may be a JSON string)
			args := parseArguments(fn["arguments"])

			event := ExtractedEvent{
				ToolName: name,
				Args:     args,
			}
			if path := firstOf(args, "path", "file_path", "filePath"); path != "" {
				event.FilePath = path
			}
			if u := firstOf(args, "url", "uri"); u != "" {
				event.URL = u
			}
			// Attach tool result content
			if tcID, ok := tcMap["id"].(string); ok {
				if content, ok := toolResults[tcID]; ok {
					event.Content = truncate(content, 10000)
				}
			}
			events = append(events, event)
		}
	}

	return events, nil
}

// parseArguments parses a function arguments field, which may be a JSON string or a map.
func parseArguments(argsRaw interface{}) map[string]string {
	switch v := argsRaw.(type) {
	case string:
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(v), &parsed); err != nil {
			return map[string]string{"raw": v}
		}
		return flattenInput(parsed)
	case map[string]interface{}:
		return flattenInput(v)
	default:
		return map[string]string{}
	}
}

func init() {
	// Ensure WildClawAdapter satisfies TraceAdapter
	var _ TraceAdapter = (*WildClawAdapter)(nil)
}

// wildclaw sessionID extraction helper
func wildclaw_sessionID(row map[string]interface{}) string {
	if meta, ok := row["task_metadata"].(map[string]interface{}); ok {
		if id, ok := meta["task_id"].(string); ok {
			return id
		}
	}
	if id, ok := row["request_id"].(string); ok {
		return id
	}
	return fmt.Sprintf("wildclaw-%d", row["row_idx"])
}
