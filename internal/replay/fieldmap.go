package replay

import (
	"sort"

	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/google/uuid"
)

// Field enrichment here mirrors two distinct production paths, and the
// distinction is load-bearing for the scoring report:
//
//   - The pre-execution path. A plugin builds an evaluation request
//     (plugins/openclaw/src/event-builder.ts, plugins/mcp-gateway/src/event-builder.ts)
//     carrying event_type, tool_name, command and params, and the server
//     enriches it (internal/server/server.go, enrichEvent) into tool,
//     event_type, context, source, command and file_path, then copies every
//     param into fields verbatim.
//   - The post-execution path. A plugin posts a tool result to /api/v1/audit,
//     and internal/toolresult.DetectionFields turns it into event_type
//     "tool_response" plus response and content.
//
// Nothing in either path can populate a tool's declared description, a JSON
// blob of a call's arguments, or response_text. Rules keyed on those fields
// cannot fire in production today. Replay can see all of them, so it populates
// them, and then reports separately on how much of its recall depends on them.
// See docs/replay-scoring.md and the tracking issue for the production gap.

// productionSuppliedFields are the field names a shipped producer can cause to
// be set, other than call arguments, which are copied through verbatim under
// whatever key the tool used.
var productionSuppliedFields = []string{
	"command", "content", "context", "event_type",
	"file_path", "response", "source", "tool",
}

// replayOnlyFields are populated by replay but unreachable in production, either
// because no producer sends them at all (arguments, description, response_text)
// or because replay attaches them to an event production could not have enriched
// that way (content and message taken from tool output on a pre-execution call,
// url spelled as uri or link).
var replayOnlyFields = []string{
	"arguments", "content@call", "description", "message@call",
	"response_text", "url.full", "url@alias",
}

// BuildEvaluationRequest converts an ExtractedEvent into an EvaluationRequest
// with every field a rule could legitimately see, including replay-only
// enrichment.
func BuildEvaluationRequest(event ExtractedEvent) *models.EvaluationRequest {
	req := buildBase(event)
	if req == nil {
		return nil
	}

	switch kindOf(event) {
	case EventKindDescription:
		// No production analogue; the whole event is replay-only.
		req.Fields["event_type"] = "tool_description"
		req.Fields["description"] = event.Description

	case EventKindResult:
		req.Fields["response_text"] = event.Response

	default: // EventKindCall
		if event.ArgumentsJSON != "" {
			req.Fields["arguments"] = event.ArgumentsJSON
		}
		if event.Description != "" {
			req.Fields["description"] = event.Description
		}
		// Content on a call event is the tool's output, which a pre-execution
		// evaluation cannot have seen. Production delivers that text later, as a
		// separate tool_response event, so attaching it here is replay-only.
		if event.Content != "" {
			if _, ok := req.Fields["content"]; !ok {
				req.Fields["content"] = event.Content
			}
			if _, ok := req.Fields["message"]; !ok {
				req.Fields["message"] = event.Content
			}
		}
		// event.URL is set from url, uri or link. Only a param literally named
		// "url" reaches production via the verbatim param copy, so filling the
		// plain field from the other spellings is replay-only enrichment.
		if event.URL != "" {
			if _, ok := req.Fields["url"]; !ok {
				req.Fields["url"] = event.URL
			}
			req.Fields["url.full"] = event.URL
		}
	}

	return req
}

// BuildProductionRequest converts an ExtractedEvent into the request a shipped
// producer would actually generate for it, with no replay-only enrichment. It
// returns nil for events that have no production analogue at all, which makes
// them ineligible for the production-reproducible score.
func BuildProductionRequest(event ExtractedEvent) *models.EvaluationRequest {
	if kindOf(event) == EventKindDescription {
		return nil
	}
	return buildBase(event)
}

// buildBase populates only the fields a production path can supply.
func buildBase(event ExtractedEvent) *models.EvaluationRequest {
	// Sized from the argument count alone. Adding a constant for the handful of
	// derived fields would be arithmetic on a length taken from untrusted
	// dataset input, which CodeQL flags as a possible allocation overflow, and
	// the difference is a capacity hint rather than a correctness property.
	args := make(map[string]string, len(event.Args))
	fields := make(map[string]string, len(event.Args))

	fields["source"] = "huggingface"
	fields["tool"] = event.ToolName

	switch kindOf(event) {
	case EventKindResult:
		// Mirrors internal/toolresult.DetectionFields: the audit scan sees the
		// output text under both response and content, and nothing else.
		fields["event_type"] = "tool_response"
		fields["response"] = event.Response
		fields["content"] = event.Response

	case EventKindDescription:
		// Caller decides; buildBase is only reached via BuildEvaluationRequest,
		// which overwrites event_type immediately.
		fields["event_type"] = "tool_call"

	default: // EventKindCall
		eventType, command := mapToolCall(event.ToolName, event.Args)
		fields["event_type"] = eventType
		if command != "" {
			fields["command"] = command
			args["command"] = command
		}
		if event.FilePath != "" {
			fields["file_path"] = event.FilePath
		}
		// Copy all original args into fields, mirroring the verbatim param copy
		// in server.go enrichEvent.
		for k, v := range event.Args {
			if _, exists := fields[k]; !exists {
				fields[k] = v
			}
			args[k] = v
		}
	}

	return &models.EvaluationRequest{
		EventID:   uuid.New().String(),
		SessionID: event.SessionID,
		Tool:      event.ToolName,
		Source:    "huggingface",
		Args:      args,
		Fields:    fields,
	}
}

func kindOf(event ExtractedEvent) EventKind {
	if event.Kind == "" {
		return EventKindCall
	}
	return event.Kind
}

// FieldProvenanceReport describes which rule-visible fields production can
// supply, for inclusion in the scoring report.
func FieldProvenanceReport() FieldProvenance {
	prod := append([]string(nil), productionSuppliedFields...)
	replayOnly := append([]string(nil), replayOnlyFields...)
	sort.Strings(prod)
	sort.Strings(replayOnly)
	return FieldProvenance{
		ProductionSupplied: prod,
		ReplayOnly:         replayOnly,
		Note: "Production-supplied fields are those a shipped plugin plus server enrichment " +
			"can populate (internal/server/server.go enrichEvent, internal/toolresult). " +
			"Call arguments also reach fields verbatim under their own keys. Replay-only " +
			"fields have no producer today, so rules keyed on them cannot fire outside replay.",
	}
}

// mapToolCall maps a tool name to an event_type and a command string.
//
// This deliberately mirrors normaliseToolCall and eventTypeForToolCall in
// plugins/openclaw/src/normalise.ts, including the default branch that uses the
// bare tool name as the command. Enriching the command beyond what the plugin
// produces would make replay detect things production cannot.
func mapToolCall(toolName string, args map[string]string) (eventType string, command string) {
	switch toolName {
	// Execution tools
	case "exec", "Bash", "bash", "terminal", "shell", "computer":
		return "tool_call", args["command"]

	// File read
	case "read", "Read", "cat", "file_read":
		return "file_read", "Read: " + firstOf(args, "path", "file_path", "filePath")

	// File write
	case "write", "Write", "file_write", "create":
		return "file_write", "Write: " + firstOf(args, "path", "file_path", "filePath")

	// File edit
	case "edit", "Edit", "str_replace_editor":
		return "file_edit", "Edit: " + firstOf(args, "path", "file_path", "filePath")

	// Search tools (Claude Code specific)
	case "Glob", "glob":
		return "tool_call", "Glob: " + firstOf(args, "pattern", "path")
	case "Grep", "grep":
		return "tool_call", "Grep: " + firstOf(args, "pattern", "query")

	// Browser / web
	case "browser", "web_browser":
		return "browser_action", args["action"] + ": " + args["url"]
	case "web_fetch", "fetch", "http", "WebFetch":
		return "content_retrieval", args["url"]

	// Messaging
	case "message":
		return "message_send", "Message to " + args["channel"]

	// Agent spawning
	case "sessions_spawn", "Agent":
		return "agent_creation", "Spawn: " + firstOf(args, "agentId", "agent_id")

	default:
		return "tool_call", toolName
	}
}

// firstOf returns the value of the first key found in the map, or "".
func firstOf(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != "" {
			return v
		}
	}
	return ""
}
