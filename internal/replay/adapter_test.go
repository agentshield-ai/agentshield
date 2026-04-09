package replay

import (
	"encoding/json"
	"testing"
)

// --- NlileAdapter tests ---

func TestNlileAdapter_ToolUseBlocks(t *testing.T) {
	messages := []map[string]interface{}{
		{
			"role":    "user",
			"content": "List the files in the current directory",
		},
		{
			"role": "assistant",
			"content": []interface{}{
				map[string]interface{}{
					"type": "text",
					"text": "I'll list the files for you.",
				},
				map[string]interface{}{
					"type":  "tool_use",
					"id":    "tu_001",
					"name":  "Bash",
					"input": map[string]interface{}{"command": "ls -la"},
				},
			},
		},
		{
			"role": "user",
			"content": []interface{}{
				map[string]interface{}{
					"type":        "tool_result",
					"tool_use_id": "tu_001",
					"content":     "total 42\ndrwxr-xr-x 5 user user 4096 ...",
				},
			},
		},
		{
			"role": "assistant",
			"content": []interface{}{
				map[string]interface{}{
					"type":  "tool_use",
					"id":    "tu_002",
					"name":  "Read",
					"input": map[string]interface{}{"path": "/etc/passwd"},
				},
			},
		},
	}

	messagesJSON, _ := json.Marshal(messages)
	row := map[string]interface{}{
		"messages_json": string(messagesJSON),
	}

	adapter := &NlileAdapter{}
	events, err := adapter.Extract(row)
	if err != nil {
		t.Fatalf("Extract error: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}

	// First event: Bash tool
	if events[0].ToolName != "Bash" {
		t.Errorf("event[0].ToolName = %q, want Bash", events[0].ToolName)
	}
	if events[0].Args["command"] != "ls -la" {
		t.Errorf("event[0].Args[command] = %q, want 'ls -la'", events[0].Args["command"])
	}
	if events[0].Content == "" {
		t.Error("event[0] should have tool result content attached")
	}

	// Second event: Read tool
	if events[1].ToolName != "Read" {
		t.Errorf("event[1].ToolName = %q, want Read", events[1].ToolName)
	}
	if events[1].FilePath != "/etc/passwd" {
		t.Errorf("event[1].FilePath = %q, want /etc/passwd", events[1].FilePath)
	}
}

func TestNlileAdapter_StringContent(t *testing.T) {
	// Some messages have plain string content (no tool calls)
	messages := []map[string]interface{}{
		{"role": "user", "content": "Hello"},
		{"role": "assistant", "content": "Hi there!"},
	}
	messagesJSON, _ := json.Marshal(messages)
	row := map[string]interface{}{"messages_json": string(messagesJSON)}

	adapter := &NlileAdapter{}
	events, err := adapter.Extract(row)
	if err != nil {
		t.Fatalf("Extract error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events, want 0 (no tool calls)", len(events))
	}
}

func TestNlileAdapter_EmptyResponse(t *testing.T) {
	row := map[string]interface{}{
		"user_prompt":        "test",
		"assistant_response": "",
		"has_empty_response": true,
	}

	adapter := &NlileAdapter{}
	events, err := adapter.Extract(row)
	if err != nil {
		t.Fatalf("Extract error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events from empty response, want 0", len(events))
	}
}

// --- WildClawAdapter tests ---

func TestWildClawAdapter_ToolCalls(t *testing.T) {
	row := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{
				"role":    "user",
				"content": "Run ls",
			},
			map[string]interface{}{
				"role": "assistant",
				"tool_calls": []interface{}{
					map[string]interface{}{
						"id":   "call_001",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "exec",
							"arguments": `{"command":"ls -la /tmp"}`,
						},
					},
				},
			},
			map[string]interface{}{
				"role":         "tool",
				"tool_call_id": "call_001",
				"content":      "file1.txt\nfile2.txt",
			},
		},
	}

	adapter := &WildClawAdapter{}
	events, err := adapter.Extract(row)
	if err != nil {
		t.Fatalf("Extract error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].ToolName != "exec" {
		t.Errorf("ToolName = %q, want exec", events[0].ToolName)
	}
	if events[0].Args["command"] != "ls -la /tmp" {
		t.Errorf("Args[command] = %q, want 'ls -la /tmp'", events[0].Args["command"])
	}
	if events[0].Content == "" {
		t.Error("should have tool result content attached")
	}
}

func TestWildClawAdapter_NestedBody(t *testing.T) {
	row := map[string]interface{}{
		"body": map[string]interface{}{
			"messages": []interface{}{
				map[string]interface{}{
					"role": "assistant",
					"tool_calls": []interface{}{
						map[string]interface{}{
							"id":   "call_002",
							"type": "function",
							"function": map[string]interface{}{
								"name":      "read",
								"arguments": `{"path":"/etc/hosts"}`,
							},
						},
					},
				},
			},
		},
	}

	adapter := &WildClawAdapter{}
	events, err := adapter.Extract(row)
	if err != nil {
		t.Fatalf("Extract error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].ToolName != "read" {
		t.Errorf("ToolName = %q, want read", events[0].ToolName)
	}
	if events[0].FilePath != "/etc/hosts" {
		t.Errorf("FilePath = %q, want /etc/hosts", events[0].FilePath)
	}
}

// --- SmolAgentsAdapter tests ---

func TestSmolAgentsAdapter_DirectToolCalls(t *testing.T) {
	row := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{
				"role":      "tool-call",
				"tool_name": "web_search",
				"arguments": map[string]interface{}{"query": "latest news"},
			},
			map[string]interface{}{
				"role":      "tool-call",
				"tool_name": "python_interpreter",
				"arguments": map[string]interface{}{"code": "print('hello')"},
			},
		},
	}

	adapter := &SmolAgentsAdapter{}
	events, err := adapter.Extract(row)
	if err != nil {
		t.Fatalf("Extract error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].ToolName != "web_search" {
		t.Errorf("event[0].ToolName = %q, want web_search", events[0].ToolName)
	}
	if events[1].ToolName != "python_interpreter" {
		t.Errorf("event[1].ToolName = %q, want python_interpreter", events[1].ToolName)
	}
}

func TestSmolAgentsAdapter_OpenAIStyle(t *testing.T) {
	row := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{
				"role": "assistant",
				"tool_calls": []interface{}{
					map[string]interface{}{
						"function": map[string]interface{}{
							"name":      "calculator",
							"arguments": `{"expression":"2+2"}`,
						},
					},
				},
			},
		},
	}

	adapter := &SmolAgentsAdapter{}
	events, err := adapter.Extract(row)
	if err != nil {
		t.Fatalf("Extract error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].ToolName != "calculator" {
		t.Errorf("ToolName = %q, want calculator", events[0].ToolName)
	}
}

// --- SelectAdapter tests ---

func TestSelectAdapter(t *testing.T) {
	tests := []struct {
		dataset  string
		wantName string
		wantErr  bool
	}{
		{"nlile/misc-merged-claude-code-traces-v1", "nlile", false},
		{"sammshen/wildclaw-opus-traces", "wildclaw", false},
		{"smolagents/synthetic-traces-toolcalling", "smolagents", false},
		{"unknown/random-dataset", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.dataset, func(t *testing.T) {
			adapter, err := SelectAdapter(tt.dataset)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tt.dataset)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if adapter.Name() != tt.wantName {
				t.Errorf("adapter.Name() = %q, want %q", adapter.Name(), tt.wantName)
			}
		})
	}
}

// --- Helper tests ---

func TestFlattenInput(t *testing.T) {
	input := map[string]interface{}{
		"str":     "hello",
		"num":     float64(42),
		"boolVal": true,
		"null":    nil,
		"nested":  map[string]interface{}{"key": "val"},
	}
	result := flattenInput(input)
	if result["str"] != "hello" {
		t.Errorf("str = %q", result["str"])
	}
	if result["num"] != "42" {
		t.Errorf("num = %q", result["num"])
	}
	if result["boolVal"] != "true" {
		t.Errorf("boolVal = %q", result["boolVal"])
	}
	if _, ok := result["null"]; ok {
		t.Error("null should be skipped")
	}
	if result["nested"] == "" {
		t.Error("nested should be JSON-serialized")
	}
}

func TestParseArguments_JSONString(t *testing.T) {
	result := parseArguments(`{"command":"ls","path":"/tmp"}`)
	if result["command"] != "ls" {
		t.Errorf("command = %q", result["command"])
	}
	if result["path"] != "/tmp" {
		t.Errorf("path = %q", result["path"])
	}
}

func TestParseArguments_Map(t *testing.T) {
	result := parseArguments(map[string]interface{}{"key": "val"})
	if result["key"] != "val" {
		t.Errorf("key = %q", result["key"])
	}
}
