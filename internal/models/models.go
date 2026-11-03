package models

import "strings"

// EvaluationRequest represents a request for evaluation
// Shared between evaluate and triage packages to avoid circular dependencies
type EvaluationRequest struct {
	EventID   string                 `json:"event_id"`
	EventType string                 `json:"event_type,omitempty"`
	SessionID string                 `json:"session_id,omitempty"`
	Tool      string                 `json:"tool,omitempty"`
	ToolName  string                 `json:"tool_name,omitempty"` // Plugin compat alias for Tool
	Command   string                 `json:"command,omitempty"`   // Plugin compat: top-level command
	Source    string                 `json:"source,omitempty"`    // Event source (e.g. "openclaw")
	Args      map[string]string      `json:"args,omitempty"`
	Fields    map[string]string      `json:"fields"`
	RawParams map[string]interface{} `json:"params,omitempty"`  // Plugin compat alias for Args (accepts any JSON value)
	Context   string                 `json:"context,omitempty"` // Optional execution context: prod|test
	// ToolCallID is the harness's tool-use id (e.g. Claude Code's tool_use_id),
	// used to attach the verdict to the exact tool span in Weft.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// AgentName names the agent that made the call (gen_ai.agent.name).
	AgentName string `json:"agent_name,omitempty"`
	// Replay marks a re-evaluation of a recorded tool call (Weft's replay,
	// docs/weft/DESIGN.md §5.4) rather than live agent traffic. The verdict
	// is computed as usual; the evaluate span is tagged agentshield.replay,
	// the verdict is neither stored nor pushed to Weft, and the session is
	// kept out of other sessions' cross-session fields. It is honoured only
	// for session ids in the replay namespace (ReplaySessionPrefix): the HTTP
	// API rejects it on any other session, so a caller cannot use it to hide
	// live traffic or to remove a live session from correlation.
	Replay bool `json:"replay,omitempty"`
}

// ReplaySessionPrefix is the session namespace of replayed evaluations.
// Weft's replay always uses "weft-replay:<job>:<session>".
const ReplaySessionPrefix = "weft-replay:"

// IsReplaySession reports whether sessionID is in the replay namespace.
func IsReplaySession(sessionID string) bool {
	return strings.HasPrefix(sessionID, ReplaySessionPrefix)
}

// Action represents the action to take based on evaluation
type Action string

const (
	ActionAllow           Action = "allow"
	ActionBlock           Action = "block"
	ActionLog             Action = "log"
	ActionRequireApproval Action = "require_approval"
)
