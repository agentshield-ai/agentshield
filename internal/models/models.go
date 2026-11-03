package models

import (
	"fmt"
	"strings"
)

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
	// for session ids in the replay namespace (ReplaySessionPrefix), and the
	// namespace is reserved for it: the HTTP API rejects a replay flag on any
	// other session and a namespaced session or event id without the flag
	// (CheckReplayNamespace), so a caller can neither hide live traffic nor
	// remove a live session from correlation.
	Replay bool `json:"replay,omitempty"`
}

// ReplaySessionPrefix is the session namespace of replayed evaluations.
// Weft's replay always uses "weft-replay:<job>:<session>".
const ReplaySessionPrefix = "weft-replay:"

// ReplayEventPrefix starts the event id of every replayed evaluation
// ("weft-replay-<job>-<n>"). Weft versions before the replay attribute
// recognised replays by it, so live traffic must not use it either.
const ReplayEventPrefix = "weft-replay-"

// IsReplaySession reports whether sessionID is in the replay namespace.
func IsReplaySession(sessionID string) bool {
	return strings.HasPrefix(sessionID, ReplaySessionPrefix)
}

// IsReplayEventID reports whether eventID carries the replay event prefix.
func IsReplayEventID(eventID string) bool {
	return strings.HasPrefix(eventID, ReplayEventPrefix)
}

// CheckReplayNamespace enforces that the replay namespace is used by replay
// only, and replay only inside it. A replay must carry replay: true and a
// session id starting with ReplaySessionPrefix; a request with a session id
// in that namespace, or an event id starting with ReplayEventPrefix, must be
// a replay. Weft drops replayed evaluations from sessions and detections, so
// a live call that could borrow either id would be hidden from it.
func CheckReplayNamespace(req *EvaluationRequest) error {
	if req.Replay {
		if !IsReplaySession(req.SessionID) {
			return fmt.Errorf("replay is only allowed for session ids starting with %q", ReplaySessionPrefix)
		}
		return nil
	}
	if IsReplaySession(req.SessionID) {
		return fmt.Errorf("session ids starting with %q are reserved for replay (replay: true)", ReplaySessionPrefix)
	}
	if IsReplayEventID(req.EventID) {
		return fmt.Errorf("event ids starting with %q are reserved for replay (replay: true)", ReplayEventPrefix)
	}
	return nil
}

// Action represents the action to take based on evaluation
type Action string

const (
	ActionAllow           Action = "allow"
	ActionBlock           Action = "block"
	ActionLog             Action = "log"
	ActionRequireApproval Action = "require_approval"
)
