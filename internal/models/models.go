package models

// EvaluationRequest represents a request for evaluation
// Shared between evaluate and triage packages to avoid circular dependencies
type EvaluationRequest struct {
	EventID   string                 `json:"event_id"`
	SessionID string                 `json:"session_id,omitempty"`
	Tool      string                 `json:"tool,omitempty"`
	ToolName  string                 `json:"tool_name,omitempty"` // Plugin compat alias for Tool
	Command   string                 `json:"command,omitempty"`   // Plugin compat: top-level command
	Args      map[string]string      `json:"args,omitempty"`
	Fields    map[string]string      `json:"fields"`
	RawParams map[string]interface{} `json:"params,omitempty"`  // Plugin compat alias for Args (accepts any JSON value)
	Mode      string                 `json:"mode,omitempty"`    // Mode override (can only downgrade)
	Context   string                 `json:"context,omitempty"` // Optional execution context: prod|test
}

// Action represents the action to take based on evaluation
type Action string

const (
	ActionAllow Action = "allow"
	ActionBlock Action = "block"
	ActionLog   Action = "log"
)
