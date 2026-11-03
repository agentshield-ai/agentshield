package models

import (
	"fmt"
	"strings"
	"unicode"
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
	// is computed as enforce mode would act whatever the configured mode,
	// without the verdict cache, in a fixed session window
	// (session.ReplayMaxEvents, session.ReplayTTL), so it depends only on
	// the recording and the ruleset; the evaluate span is tagged agentshield.replay,
	// the verdict is neither stored nor pushed to Weft, and the session is
	// kept out of other sessions' cross-session fields (its own cover only
	// its replay job's sessions, see ReplayScope). It is honoured only
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

// MaxIDLength is the longest event, session or tool call id the engine's
// HTTP API accepts, in bytes. Weft's replay session ids
// ("weft-replay:<job>:<session>") are kept within it.
const MaxIDLength = 256

// ReplayEventPrefix starts the event id of every replayed evaluation
// ("weft-replay-<job>-<n>"). Weft versions before the replay attribute
// recognised replays by it, so live traffic must not use it either.
const ReplayEventPrefix = "weft-replay-"

// IsReplaySession reports whether sessionID is in the replay namespace.
func IsReplaySession(sessionID string) bool {
	return strings.HasPrefix(strings.TrimLeftFunc(sessionID, unicode.IsSpace), ReplaySessionPrefix)
}

// ReplayScope returns the part of a replay session id that names its replay
// job, "weft-replay:<job>:", which every session of that job shares. A
// replayed session's cross-session fields are computed over its own job's
// sessions only. An id in the namespace without a job part is its own
// scope; an id outside the namespace has none ("").
func ReplayScope(sessionID string) string {
	if !IsReplaySession(sessionID) {
		return ""
	}
	id := strings.TrimLeftFunc(sessionID, unicode.IsSpace)
	rest := id[len(ReplaySessionPrefix):]
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		return id[:len(ReplaySessionPrefix)+i+1]
	}
	return id
}

// IsReplayEventID reports whether eventID carries the replay event prefix.
func IsReplayEventID(eventID string) bool {
	return strings.HasPrefix(strings.TrimLeftFunc(eventID, unicode.IsSpace), ReplayEventPrefix)
}

// CheckID rejects an identifier with control characters (tab, newline and
// carriage return included) or leading or trailing whitespace. Identifiers
// are compared exactly by the engine but trimmed by some consumers (Weft),
// so padded ids could fall on different sides of the replay namespace on
// each side and hide live traffic. No shipped plugin produces such ids.
func CheckID(field, id string) error {
	for _, r := range id {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return fmt.Errorf("%s contains control characters", field)
		}
	}
	if strings.TrimFunc(id, unicode.IsSpace) != id {
		return fmt.Errorf("%s has leading or trailing whitespace", field)
	}
	return nil
}

// CheckReplayNamespace enforces that the replay namespace is used by replay
// only, and replay only inside it. A replay must carry replay: true and a
// session id starting with ReplaySessionPrefix; a request with a session id
// in that namespace, or an event id starting with ReplayEventPrefix, must be
// a replay. Weft drops replayed evaluations from sessions and detections, so
// a live call that could borrow either id would be hidden from it.
func CheckReplayNamespace(req *EvaluationRequest) error {
	for _, f := range []struct{ name, v string }{{"event_id", req.EventID}, {"session_id", req.SessionID}, {"tool_call_id", req.ToolCallID}} {
		if err := CheckID(f.name, f.v); err != nil {
			return err
		}
	}
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
