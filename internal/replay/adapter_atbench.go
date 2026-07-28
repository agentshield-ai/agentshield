package replay

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ATBenchAdapter parses the AI45Research/ATBench dataset (Apache-2.0), a corpus
// of 1,000 agent trajectories labelled roughly 50/50 safe and unsafe.
//
// Row shape (HF config "ATBench", split "test"):
//
//	id                int64
//	label             int64   0 = safe, 1 = unsafe
//	risk_source       string  e.g. tool_description_injection
//	failure_mode      string
//	reason            string
//	real_world_harm   string
//	tool_used         list of OpenAI-style tool schemas
//	contents          list of conversations, each a list of turns
//
// Turn shapes, verified uniform across all 1,000 rows (9,009 turns):
//
//	{"role":"user",        "content": "..."}
//	{"role":"environment", "content": "..."}
//	{"role":"agent",       "thought": "...", "action": "..."}
//
// An agent action is either a JSON object {"name":..., "arguments":...} or the
// sentinel Complete{"response": ...}, which carries no tool call. Complete is
// the only sentinel prefix in the corpus, and it is NOT always the final turn:
// 546 of 1,520 occurrences are followed by further user and agent turns, so a
// conversation is a sequence of episodes rather than a single task.
//
// An environment turn is the response to the immediately preceding agent call,
// so it is attached to that call rather than emitted as its own event. This
// invariant holds exactly: every one of the 2,971 environment turns follows a
// tool call, and only one of the 2,972 calls (the last turn of one trace) has
// no environment turn after it.
//
// Where a tool description has been poisoned, `description` holds the attacker's
// text and `_original_description` preserves the clean version. The adapter uses
// `description`, because that is what the agent actually saw.
type ATBenchAdapter struct{}

func (a *ATBenchAdapter) Name() string { return "atbench" }

// Extract satisfies TraceAdapter, discarding the ground-truth label.
func (a *ATBenchAdapter) Extract(row map[string]interface{}) ([]ExtractedEvent, error) {
	_, events, err := a.ExtractLabelled(row)
	return events, err
}

// ExtractLabelled parses a row into its ground-truth label and its events.
func (a *ATBenchAdapter) ExtractLabelled(row map[string]interface{}) (TraceLabel, []ExtractedEvent, error) {
	label := TraceLabel{
		Present:     true,
		TraceID:     asIntString(row["id"]),
		RiskSource:  asString(row["risk_source"]),
		FailureMode: asString(row["failure_mode"]),
	}
	rawLabel, ok := row["label"]
	if !ok {
		return TraceLabel{}, nil, fmt.Errorf("row has no label column")
	}
	n, ok := asInt(rawLabel)
	if !ok {
		return TraceLabel{}, nil, fmt.Errorf("label %v is not numeric", rawLabel)
	}
	label.Malicious = n == 1

	sessionID := "atbench-" + label.TraceID

	// Tool schemas, keyed by name so a call can borrow its declared description.
	schemas := parseToolSchemas(row["tool_used"])

	var events []ExtractedEvent

	// One synthetic tool_description event per declared tool. No shipped
	// producer emits these, so they are marked replay-only and are excluded
	// from the production-reproducible score.
	for _, name := range sortedSchemaNames(schemas) {
		s := schemas[name]
		if strings.TrimSpace(s.Description) == "" {
			continue
		}
		events = append(events, ExtractedEvent{
			SessionID:   sessionID,
			Kind:        EventKindDescription,
			ToolName:    name,
			Description: truncate(s.Description, maxContentLen),
			Args:        map[string]string{},
		})
	}

	conversations, ok := row["contents"].([]interface{})
	if !ok {
		return label, events, fmt.Errorf("contents is not a list")
	}

	for _, rawConv := range conversations {
		turns, ok := rawConv.([]interface{})
		if !ok {
			continue
		}
		for i, rawTurn := range turns {
			turn, ok := rawTurn.(map[string]interface{})
			if !ok {
				continue
			}
			if asString(turn["role"]) != "agent" {
				continue
			}
			name, args, argsJSON, ok := parseAgentAction(turn["action"])
			if !ok {
				// Complete{...} sentinel or an unparseable action: no tool call.
				continue
			}

			call := ExtractedEvent{
				SessionID:     sessionID,
				Kind:          EventKindCall,
				ToolName:      name,
				Args:          args,
				ArgumentsJSON: truncate(argsJSON, maxContentLen),
			}
			if p := firstOf(args, "path", "file_path", "filePath", "file"); p != "" {
				call.FilePath = p
			}
			if u := firstOf(args, "url", "uri", "link"); u != "" {
				call.URL = u
			}
			if s, ok := schemas[name]; ok {
				call.Description = truncate(s.Description, maxContentLen)
			}

			// The environment turn immediately after a call is its response.
			var response string
			if i+1 < len(turns) {
				if next, ok := turns[i+1].(map[string]interface{}); ok &&
					asString(next["role"]) == "environment" {
					response = truncate(asString(next["content"]), maxContentLen)
				}
			}
			call.Content = response
			events = appendCallWithResult(events, call)
		}
	}

	return label, events, nil
}

// maxContentLen bounds any single text field fed to the rule engine.
const maxContentLen = 10000

// toolSchema is one entry of the tool_used column.
type toolSchema struct {
	Name string
	// Description is the description the agent saw, which is the attacker's
	// text when the tool has been poisoned.
	Description string
	// OriginalDescription is the clean text preserved alongside a poisoned
	// description. Empty when the tool was not poisoned.
	OriginalDescription string
}

// Poisoned reports whether the declared description differs from the preserved
// original, which identifies exactly which tool was tampered with.
func (s toolSchema) Poisoned() bool {
	return s.OriginalDescription != "" && s.OriginalDescription != s.Description
}

func parseToolSchemas(raw interface{}) map[string]toolSchema {
	out := map[string]toolSchema{}
	list, ok := raw.([]interface{})
	if !ok {
		return out
	}
	for _, item := range list {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		name := asString(m["name"])
		if name == "" {
			continue
		}
		out[name] = toolSchema{
			Name:                name,
			Description:         asString(m["description"]),
			OriginalDescription: asString(m["_original_description"]),
		}
	}
	return out
}

func sortedSchemaNames(m map[string]toolSchema) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	// Deterministic order so reports and testcase exports are reproducible.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	return names
}

// parseAgentAction decodes an agent turn's action string. It returns ok=false
// for the Complete{...} sentinel and for anything that is not a tool call.
//
// The corpus contains 2,966 calls whose "arguments" is an object and 6 whose
// "arguments" is a JSON string that must be decoded a second time.
func parseAgentAction(raw interface{}) (name string, args map[string]string, argsJSON string, ok bool) {
	s, isStr := raw.(string)
	if !isStr {
		return "", nil, "", false
	}
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") {
		// Complete{"response": ...} and any other Sentinel{...} form.
		return "", nil, "", false
	}
	var action struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(s), &action); err != nil {
		return "", nil, "", false
	}
	if action.Name == "" {
		return "", nil, "", false
	}

	parsed := map[string]interface{}{}
	if len(action.Arguments) > 0 {
		if err := json.Unmarshal(action.Arguments, &parsed); err != nil {
			// "arguments" is a JSON-encoded string rather than an object.
			var inner string
			if json.Unmarshal(action.Arguments, &inner) == nil {
				if err := json.Unmarshal([]byte(inner), &parsed); err != nil {
					parsed = map[string]interface{}{"raw": inner}
				}
			}
		}
	}

	// Re-serialise canonically so the `arguments` blob is stable regardless of
	// which of the two encodings the row used.
	blob, err := json.Marshal(parsed)
	if err != nil {
		blob = []byte("{}")
	}
	return action.Name, flattenInput(parsed), string(blob), true
}

func asString(v interface{}) string {
	s, _ := v.(string)
	return s
}

func asInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	}
	return 0, false
}

func asIntString(v interface{}) string {
	if n, ok := asInt(v); ok {
		return fmt.Sprintf("%d", n)
	}
	return asString(v)
}

func init() {
	var _ TraceAdapter = (*ATBenchAdapter)(nil)
	var _ LabelledAdapter = (*ATBenchAdapter)(nil)
}
