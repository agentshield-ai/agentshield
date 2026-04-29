// Package proxyadapter ingests per-request observations from forward proxies
// (CrabTrap, mitmproxy, etc.) and forwards them as evaluation requests to the
// AgentShield engine. The adapter consumes a well-defined NDJSON schema; it
// is the operator's responsibility to transform their proxy's native log
// format into this schema (typically with a small jq filter).
//
// Posting these observations through /api/v1/evaluate places them in the
// same audit trail and verdict cache as tool-call events, and — when the
// session_id is shared — exposes them to AgentShield's per-session
// behavioural rules (e.g. ai_agent_recon_then_proxy_egress.yml fires on a
// recon tool call followed by a proxy-observed private-network connection).
package proxyadapter

// Observation is the canonical NDJSON shape this adapter consumes — one JSON
// object per line. Required fields fail-fast at parse time; optional fields
// produce a normalised default when absent. CrabTrap, mitmproxy, Squid, etc.
// can all feed this schema with a small upstream transform.
type Observation struct {
	// Required.
	RequestID string `json:"request_id"`
	Method    string `json:"method"`
	URL       string `json:"url"`

	// Optional — used for session correlation. When SessionID is empty the
	// adapter falls back to SrcIP (so proxy-observations from the same client
	// share a window in AgentShield's session registry); operators wanting
	// strict tool-call ↔ proxy correlation should arrange for the agent
	// harness to inject a session header that the proxy forwards.
	SessionID string `json:"session_id,omitempty"`
	SrcIP     string `json:"src_ip,omitempty"`

	// Optional — informational metadata propagated as proxy.* fields so
	// rules can match on the upstream proxy's own decision.
	Status     int    `json:"status,omitempty"`      // HTTP-style
	Decision   string `json:"decision,omitempty"`    // "block" | "allow" | "flag"
	DecidedBy  string `json:"decided_by,omitempty"`  // "rule" | "llm" | "human"
	DurationMs int64  `json:"duration_ms,omitempty"`
	User       string `json:"user,omitempty"`
	Timestamp  string `json:"timestamp,omitempty"` // ISO 8601; informational only
}
