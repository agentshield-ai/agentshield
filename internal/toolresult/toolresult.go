// Package toolresult reconciles the tool-result audit event schema emitted by
// AgentShield plugins with the field names the Sigma rules match on, so that
// tool OUTPUT — the vector for indirect prompt injection — can be scanned for
// malicious content.
//
// Producers (OpenClaw, Codex, Gemini, and the Claude hook) post post-tool
// events with event_type "tool_result" and the output text in "result_summary".
// The detection rules that inspect tool output (e.g.
// ai_agent_openclaw_prompt_injection, ai_agent_mcp_tool_poisoning) were written
// against event_type "tool_response" and read the text from "response"/"content".
// Historically those events posted to /audit, which never ran the engine, so the
// rules could not fire. DetectionFields bridges the two vocabularies so the audit
// handler can run a detection-only pass.
package toolresult

import "strings"

// DetectionFields maps a decoded tool-result audit payload to the flat field map
// the rule engine evaluates. It returns ok=false when the payload is not a tool
// result, or carries no output text to scan (nothing to detect on).
func DetectionFields(payload map[string]any) (fields map[string]string, ok bool) {
	if asString(payload["event_type"]) != "tool_result" {
		return nil, false
	}
	text := asString(payload["result_summary"])
	if em := asString(payload["error_message"]); em != "" {
		// Tool error text is equally attacker-influenced and can carry injection.
		if text == "" {
			text = em
		} else {
			text += "\n" + em
		}
	}
	if strings.TrimSpace(text) == "" {
		return nil, false
	}
	fields = map[string]string{
		// Canonical event type the tool-output rules match on. Producers emit
		// "tool_result"; the rules were authored against "tool_response".
		"event_type": "tool_response",
		// The rules variously read "response" and "content"; populate both from
		// the single output-text field the producers provide ("result_summary").
		"response": text,
		"content":  text,
	}
	if s := asString(payload["source"]); s != "" {
		fields["source"] = s
	}
	if t := asString(payload["tool_name"]); t != "" {
		fields["tool"] = t
	}
	return fields, true
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}
