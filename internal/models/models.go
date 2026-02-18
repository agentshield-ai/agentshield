package models

// EvaluationRequest represents a request for evaluation
// Shared between evaluate and triage packages to avoid circular dependencies
type EvaluationRequest struct {
	EventID   string            `json:"event_id"`
	SessionID string            `json:"session_id,omitempty"`
	Tool      string            `json:"tool,omitempty"`
	Args      map[string]string `json:"args,omitempty"`
	Fields    map[string]string `json:"fields"`
	Mode      string            `json:"mode,omitempty"` // Mode override (can only downgrade)
}

// Action represents the action to take based on evaluation
type Action string

const (
	ActionAllow Action = "allow"
	ActionBlock Action = "block"
	ActionLog   Action = "log"
)