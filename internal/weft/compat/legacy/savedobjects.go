package legacy

import (
	"bytes"
	"encoding/json"
	"sort"
)

// Field names that Weft's own saved objects, automation runs and feedback
// payloads used before Weft named spans, functions and sessions its own way
// (DESIGN.md §10): op/call/thread where it now says name/span/session. Some
// of them are also the weave client's names, which is why its objects (a
// Monitor or AnnotationSpec it publishes) still carry them; readers of
// those object classes accept both.

// SavedObjectRename lists the renamed keys and keyed string values of one
// saved object class.
type SavedObjectRename struct {
	// Keys maps old object keys to new ones, at any depth.
	Keys map[string]string
	// Values maps, for a key (at any depth), its old string values (or the
	// strings of its array value) to new ones.
	Values map[string]map[string]string
}

var filterKeys = map[string]string{"op_names": "names", "call_ids": "ids", "thread_ids": "session_ids"}

var savedObjectRenames = map[string]SavedObjectRename{
	// Monitor: names (span names to score), scopes value span (per span).
	"Monitor": {Keys: map[string]string{"op_names": "names"},
		Values: map[string]map[string]string{"scopes": {"call": "span"}}},
	// Automation: trigger.names, trigger.min_spans.
	"Automation": {Keys: map[string]string{"op_names": "names", "min_calls": "min_spans"}},
	// SavedView: definition.filter (a span filter).
	"SavedView": {Keys: filterKeys},
	// Dashboard: filter and panels[].filter (span filters), panels[].group_by.
	"Dashboard": {Keys: filterKeys,
		Values: map[string]map[string]string{"group_by": {"op_name": "name", "op": "name", "thread": "session", "thread_id": "session"}}},
	// Leaderboard: weft_dynamic.evaluation_run_ids.
	"Leaderboard": {Keys: map[string]string{"evaluation_call_ids": "evaluation_run_ids"}},
	// AnnotationSpec: names (the span names it applies to).
	"AnnotationSpec": {Keys: map[string]string{"op_scope": "names"}},
}

// SavedObjectClasses lists the classes SavedObjectRenames covers, sorted.
func SavedObjectClasses() []string {
	out := make([]string, 0, len(savedObjectRenames))
	for c := range savedObjectRenames {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// RenameSavedObject returns the value of a saved object of class cls with
// the earlier names replaced, keeping the rest of the bytes (key order,
// spacing, numbers) as they are; b itself when nothing changes.
func RenameSavedObject(cls string, b []byte) []byte {
	rn, ok := savedObjectRenames[cls]
	if !ok {
		return b
	}
	out := b
	if len(rn.Values) > 0 {
		out = RewriteKeyedValues(out, rn.Values)
	}
	if len(rn.Keys) > 0 {
		out = JSONRewrite{Key: func(k string) (string, bool) {
			n, ok := rn.Keys[k]
			return n, ok
		}}.Apply(out)
	}
	return out
}

// AutomationEventKeys maps the earlier keys of an automation run's event
// (webhook payload) to the current ones.
var AutomationEventKeys = map[string]string{"call_id": "span_id", "total_calls": "total_spans", "error_calls": "error_spans",
	"op_names": "names"}

// SignalPayloadKeys maps the earlier keys of a weft.signal feedback payload
// to the current ones.
var SignalPayloadKeys = map[string]string{"scorer_call_id": "scorer_span_id"}

// RenameKeys rewrites the object keys of a JSON document by names, keeping
// the rest of the bytes.
func RenameKeys(b []byte, names map[string]string) []byte {
	return JSONRewrite{Key: func(k string) (string, bool) {
		n, ok := names[k]
		return n, ok
	}}.Apply(b)
}

// RewriteKeyedValues replaces, in a JSON document, the plain (escape-free)
// string values of the given keys, or the strings of their array values,
// by the given maps; the rest of the bytes is kept. Invalid JSON is
// returned unchanged.
func RewriteKeyedValues(b []byte, byKey map[string]map[string]string) []byte {
	type repl struct {
		start, end int
		val        string
	}
	var reps []repl
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	// A stack of containers: for objects, whether the next string is a key.
	type frame struct{ object, wantKey bool }
	var stack []frame
	var pending map[string]string // the map for the value being read
	pendingArr := false           // inside an array value of a mapped key
	arrDepth := 0
	// literal checks that b[end-len(s)-2:end] is the plain literal "s".
	literal := func(end int, s string) (int, bool) {
		start := end - len(s) - 2
		if start < 0 || b[start] != '"' || b[end-1] != '"' || string(b[start+1:end-1]) != s {
			return 0, false
		}
		return start, true
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			break // io.EOF or invalid JSON
		}
		end := int(dec.InputOffset())
		var top *frame
		if len(stack) > 0 {
			top = &stack[len(stack)-1]
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{', '[':
				if pending != nil && t == '[' && !pendingArr {
					pendingArr, arrDepth = true, len(stack)+1
				} else if !pendingArr {
					pending = nil
				}
				stack = append(stack, frame{object: t == '{', wantKey: t == '{'})
			case '}', ']':
				stack = stack[:len(stack)-1]
				if pendingArr && len(stack) < arrDepth {
					pendingArr, pending = false, nil
				}
				if len(stack) > 0 && stack[len(stack)-1].object {
					stack[len(stack)-1].wantKey = true
				}
			}
			continue
		case string:
			if top != nil && top.object && top.wantKey {
				top.wantKey = false
				pending = byKey[t]
				continue
			}
			if pending != nil && (!pendingArr || len(stack) == arrDepth) {
				if n, ok := pending[t]; ok {
					if start, ok := literal(end, t); ok {
						reps = append(reps, repl{start, end, n})
					}
				}
			}
		}
		// A scalar value: the next string of the enclosing object is a key.
		if !pendingArr {
			pending = nil
		}
		if top != nil && top.object {
			top.wantKey = true
		}
	}
	if len(reps) == 0 {
		return b
	}
	out := make([]byte, 0, len(b))
	last := 0
	for _, r := range reps {
		out = append(out, b[last:r.start]...)
		out = append(out, '"')
		out = append(out, r.val...)
		out = append(out, '"')
		last = r.end
	}
	return append(out, b[last:]...)
}
