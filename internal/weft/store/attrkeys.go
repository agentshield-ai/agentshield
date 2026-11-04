package store

import "strings"

// Weft's own span attribute keys that were renamed, and the names earlier
// SDK versions used for them (docs/weft/DESIGN.md §4, API.md "Span
// attributes"). Weft's SDKs and plugins emit only the new keys. Spans sent
// by earlier SDK versions, and spans stored before the rename, carry the old
// ones; stored spans are not rewritten, so everything that reads one of
// these keys (ingest, the agents index, run status, queries) reads the new
// key and then the old one. This is Weft's own backward compatibility, not
// the compatibility adapter's (which maps another product's names onto the
// new keys).
//
// Like every attribute key, each is read from OTel span attributes (flat
// dotted keys), from a call's own attributes as a flat dotted key, or as
// nested maps (attributes.weft.parent_span.id).
const (
	// AttrSpanName overrides an OTel span's name as the stored span name.
	AttrSpanName = "weft.span.name"
	// AttrSessionID is the span's session (stored session_id).
	AttrSessionID = "weft.session_id"
	// AttrParentSpan prefixes the link of a span that started its own trace
	// inside another span (the caller): .id and .trace_id.
	AttrParentSpan        = "weft.parent_span"
	AttrParentSpanID      = AttrParentSpan + ".id"
	AttrParentSpanTraceID = AttrParentSpan + ".trace_id"
	// AttrEvalRunID and AttrEvalPredictionSpanID link a span to an
	// evaluation run and its prediction (the predict_and_score span).
	AttrEvalRunID            = "weft.eval.run_id"
	AttrEvalPredictionSpanID = "weft.eval.prediction_span_id"
	// AttrLinkedSpans lists the spans an EvaluationLogger prediction links
	// to: [{span_id, trace_id?}] (or span ids / span refs).
	AttrLinkedSpans = "weft.linked_spans"
	// LinkedSpanID is the id field of an AttrLinkedSpans item.
	LinkedSpanID = "span_id"
)

// DeprecatedAttrKey is a renamed attribute key: New and the Old name
// earlier SDK versions emitted. A key ending in "." is a prefix (every key
// under it is renamed the same way).
type DeprecatedAttrKey struct{ New, Old string }

// DeprecatedAttrKeys lists every renamed Weft attribute key. It is the only
// place the old names are spelled; they are still read, never written.
var DeprecatedAttrKeys = []DeprecatedAttrKey{
	{AttrSpanName, "weft.op_name"},
	{AttrSessionID, "weft.thread_id"},
	{AttrParentSpan + ".", "weft.parent_call."},
	{AttrEvalPredictionSpanID, "weft.eval.predict_and_score_call_id"},
	{AttrLinkedSpans, "weft.linked_calls"},
}

// DeprecatedLinkedSpanID is the id field of a weft.linked_calls item.
const DeprecatedLinkedSpanID = "call_id"

// deprecatedAgentFields maps the agents API span fields that mirrored the
// old weft.parent_call.* keys to their new names (query aliases only).
var deprecatedAgentFields = map[string]string{
	"parent_call_id":       "linked_parent_span_id",
	"parent_call_trace_id": "linked_parent_trace_id",
}

// AttrKeys returns key followed by the deprecated names it is also read
// under (none for keys that were not renamed).
func AttrKeys(key string) []string {
	if old, ok := DeprecatedAttrName(key); ok {
		return []string{key, old}
	}
	return []string{key}
}

// DeprecatedAttrName returns the earlier name of a renamed key (or of a key
// under a renamed prefix); ok is false for other keys.
func DeprecatedAttrName(key string) (string, bool) { return renameAttrKey(key, true) }

// renameAttrKey maps a key between its new and its deprecated name: to the
// old name when toOld, else to the new one. ok is false when key is not a
// renamed key in that direction.
func renameAttrKey(key string, toOld bool) (string, bool) {
	for _, d := range DeprecatedAttrKeys {
		from, to := d.New, d.Old
		if !toOld {
			from, to = d.Old, d.New
		}
		if p, ok := strings.CutSuffix(from, "."); ok {
			if key == p {
				return strings.TrimSuffix(to, "."), true
			}
			if rest, ok := strings.CutPrefix(key, from); ok {
				return to + rest, true
			}
			continue
		}
		if key == from {
			return to, true
		}
	}
	return "", false
}

// renamedAttrPath maps a field path below attributes (split into
// segments, in any of the forms keys are stored in: nested, one flat
// dotted segment, or under otel.attributes) to the same path under the
// renamed key's other name (the deprecated one when toOld, else the new
// one). ok is false when the path names no renamed key. The id field of a
// linked-spans item is renamed too (span_id / call_id).
func renamedAttrPath(segs []string, toOld bool) ([]string, bool) {
	prefix := 0
	if len(segs) >= 3 && segs[0] == "otel" && segs[1] == "attributes" {
		prefix = 2
	}
	body := segs[prefix:]
	linked, itemFrom, itemTo := AttrLinkedSpans, LinkedSpanID, DeprecatedLinkedSpanID
	if !toOld {
		linked, _ = DeprecatedAttrName(AttrLinkedSpans)
		itemFrom, itemTo = itemTo, itemFrom
	}
	build := func(key string, n int, flat bool) []string {
		other, _ := renameAttrKey(key, toOld)
		out := append([]string{}, segs[:prefix]...)
		if flat {
			out = append(out, other)
		} else {
			out = append(out, strings.Split(other, ".")...)
		}
		at := len(out)
		out = append(out, body[n:]...)
		if key == linked {
			for i := at; i < len(out); i++ {
				if out[i] == itemFrom {
					out[i] = itemTo
					break
				}
			}
		}
		return out
	}
	// A flat dotted segment ("weft.parent_span.id"), possibly followed by
	// item fields.
	if len(body) > 0 && strings.Contains(body[0], ".") {
		if _, ok := renameAttrKey(body[0], toOld); ok {
			return build(body[0], 1, true), true
		}
	}
	// Nested segments: the longest prefix of body that names a key.
	for n := len(body); n >= 2; n-- {
		if key := strings.Join(body[:n], "."); key != "" {
			if _, ok := renameAttrKey(key, toOld); ok {
				return build(key, n, false), true
			}
		}
	}
	return nil, false
}

// attrJSONCol is a SQL expression reading attribute key from calls alias
// a's attributes in every stored form (nested, flat, under otel.attributes),
// under its new name first and then its deprecated one; ” when absent.
func attrJSONCol(a, key string) string {
	var parts []string
	for _, k := range AttrKeys(key) {
		quoted := strings.ReplaceAll(k, `"`, `\"`)
		parts = append(parts,
			`json_extract(`+a+`.attributes, '$.`+k+`')`,
			`json_extract(`+a+`.attributes, '$.otel.attributes."`+quoted+`"')`,
			`json_extract(`+a+`.attributes, '$."`+quoted+`"')`)
	}
	return `COALESCE(` + strings.Join(parts, ", ") + `, '')`
}
