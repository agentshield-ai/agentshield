// Package legacy holds the names Weft used before it had its own: the
// feedback types, attribute keys, query fields and column name that clients
// of the Weave product send, and that databases written by earlier Weft
// versions contain.
//
// Two places use it:
//
//   - the store's one-time migration, which renames stored values, and its
//     query compiler, which reads queries saved before the rename (saved
//     views, dashboards and monitors are content-addressed objects, so their
//     stored text is not rewritten);
//   - the compatibility adapter (package compat), which translates what the
//     official weave client sends and receives when --weave-compat is on.
//
// Nothing else in Weft refers to these names. See docs/weft/COMPATIBILITY.md.
package legacy

import "strings"

// FeedbackRefColumn is the name the feedback table's ref column had before
// it was renamed to "ref", and the JSON field name the weave client uses.
const FeedbackRefColumn = "weave_ref"

// RefScheme and FileScheme are the URI prefixes the weave client writes for
// object refs and file digests; Weft's own are "weft:///" and "weft-file://".
const (
	RefScheme  = "weave:///"
	FileScheme = "weave-file://"
)

// feedbackTypes maps exact legacy feedback types to Weft's.
var feedbackTypes = map[string]string{
	"wandb.reaction.1":          "weft.reaction",
	"wandb.note.1":              "weft.note",
	"wandb.agent_monitor":       "weft.signal",
	"wandb.agent_user_feedback": "weft.user_feedback",
}

// feedbackPrefixes maps legacy feedback type prefixes to Weft's.
var feedbackPrefixes = [][2]string{
	{"wandb.runnable.", "weft.scorer."},
	{"wandb.annotation.", "weft.annotation."},
}

// FeedbackTypeRename is one rename applied by the store migration.
type FeedbackTypeRename struct {
	Old, New string
	Prefix   bool // Old and New are prefixes; the rest of the type is kept
}

// FeedbackTypeRenames lists every feedback type rename, exact names first.
func FeedbackTypeRenames() []FeedbackTypeRename {
	out := make([]FeedbackTypeRename, 0, len(feedbackTypes)+len(feedbackPrefixes))
	for _, k := range []string{"wandb.reaction.1", "wandb.note.1", "wandb.agent_monitor", "wandb.agent_user_feedback"} {
		out = append(out, FeedbackTypeRename{Old: k, New: feedbackTypes[k]})
	}
	for _, p := range feedbackPrefixes {
		out = append(out, FeedbackTypeRename{Old: p[0], New: p[1], Prefix: true})
	}
	return out
}

// FeedbackType maps a legacy feedback type to Weft's; other types are
// returned unchanged.
func FeedbackType(t string) string {
	if n, ok := feedbackTypes[t]; ok {
		return n
	}
	for _, p := range feedbackPrefixes {
		if strings.HasPrefix(t, p[0]) {
			return p[1] + t[len(p[0]):]
		}
	}
	return t
}

// FeedbackTypeOut maps a Weft feedback type back to the legacy name the
// weave client expects; other types are returned unchanged.
func FeedbackTypeOut(t string) string {
	for k, v := range feedbackTypes {
		if t == v {
			return k
		}
	}
	for _, p := range feedbackPrefixes {
		if strings.HasPrefix(t, p[1]) {
			return p[0] + t[len(p[1]):]
		}
	}
	return t
}

// FeedbackTypePattern maps a feedback type pattern (an exact type, or a
// prefix ending in "*") as saved in automation triggers.
func FeedbackTypePattern(p string) string {
	switch p {
	case "wandb.reaction.*", "wandb.reaction.1*":
		return "weft.reaction"
	case "wandb.note.*":
		return "weft.note"
	case "wandb.*":
		return "weft.*"
	}
	if base, ok := strings.CutSuffix(p, "*"); ok {
		return FeedbackType(base) + "*"
	}
	return FeedbackType(p)
}

// IsLegacyFeedbackType reports whether t is a legacy feedback type name.
func IsLegacyFeedbackType(t string) bool { return strings.HasPrefix(t, "wandb.") }

// attrKeys maps legacy attribute keys (flat, dotted) to Weft's.
var attrKeys = map[string]string{
	"weave.op_name":      "weft.op_name",
	"weave.display_name": "weft.display_name",
	"wandb.display_name": "weft.display_name",
	"wandb.thread_id":    "weft.thread_id",
	"wandb.thread_name":  "weft.conversation.name",
	"wandb.turn_id":      "weft.turn_id",
	"wandb.is_turn":      "weft.is_turn",
	"weave.is_turn":      "weft.is_turn",
	"weave.span_kind":    "weft.span_kind",
	"weave.kind":         "weft.span_kind",
	"weave.color":        "weft.color",
	"weave.agent.name":   "weft.agent.name",
}

// attrPrefixes maps legacy attribute key prefixes to Weft's.
var attrPrefixes = [][2]string{
	{"weave.parent_call.", "weft.parent_call."},
	{"weave.eval.", "weft.eval."},
}

// AttributeKey maps a legacy flat attribute key to Weft's. ok is false for
// keys that are not legacy names.
func AttributeKey(k string) (string, bool) {
	if n, ok := attrKeys[k]; ok {
		return n, true
	}
	for _, p := range attrPrefixes {
		if strings.HasPrefix(k, p[0]) {
			return p[1] + k[len(p[0]):], true
		}
	}
	return k, false
}

// nestedKeys maps the keys of a nested attributes.weave map that have a
// Weft equivalent under attributes.weft. The weave client's other nested
// keys (client_version, os_name, ...) are kept where they are.
var nestedKeys = map[string]string{
	"kind":         "span_kind",
	"span_kind":    "span_kind",
	"color":        "color",
	"display_name": "display_name",
	"eval":         "eval",
	"parent_call":  "parent_call",
	"is_turn":      "is_turn",
	"op_name":      "op_name",
}

// Resource attribute keys naming the target project.
const (
	ResourceEntity  = "wandb.entity"
	ResourceProject = "wandb.project"
)

// RewriteAttributes renames legacy keys in a call's attributes: flat dotted
// keys at the top level and in otel.attributes, and the nested
// attributes.weave map. Weft's own keys win when both are present. It
// reports whether anything changed.
func RewriteAttributes(attrs map[string]any) bool {
	if attrs == nil {
		return false
	}
	changed := renameFlat(attrs)
	if o, ok := attrs["otel"].(map[string]any); ok {
		if a, ok := o["attributes"].(map[string]any); ok && renameFlat(a) {
			changed = true
		}
	}
	if w, ok := attrs["weave"].(map[string]any); ok {
		weft, _ := attrs["weft"].(map[string]any)
		for k, nk := range nestedKeys {
			v, ok := w[k]
			if !ok {
				continue
			}
			if weft == nil {
				weft = map[string]any{}
			}
			if _, exists := weft[nk]; !exists {
				weft[nk] = v
			}
			delete(w, k)
			changed = true
		}
		if weft != nil {
			attrs["weft"] = weft
		}
		if len(w) == 0 {
			delete(attrs, "weave")
		}
	}
	return changed
}

func renameFlat(m map[string]any) bool {
	changed := false
	for k, v := range m {
		nk, ok := AttributeKey(k)
		if !ok {
			continue
		}
		if _, exists := m[nk]; !exists {
			m[nk] = v
		}
		delete(m, k)
		changed = true
	}
	return changed
}

// AttributeFilterSQL is a SQLite condition on a JSON column that is true for
// attributes that may hold legacy keys (a cheap prefilter for the migration).
func AttributeFilterSQL(col string) string {
	return col + ` LIKE '%"weave%' OR ` + col + ` LIKE '%"wandb.%'`
}

// QueryField maps a legacy call query field path (summary.weave.*,
// feedback.wandb.*) to Weft's; other paths are returned unchanged.
func QueryField(path string) string {
	if rest, ok := strings.CutPrefix(path, "summary.weave."); ok {
		return "summary.weft." + rest
	}
	if rest, ok := strings.CutPrefix(path, "feedback.wandb."); ok {
		segs := strings.SplitN(rest, ".", 3)
		if len(segs) < 2 {
			return path
		}
		t := FeedbackType("wandb." + segs[0] + "." + segs[1])
		if len(segs) == 3 {
			return "feedback." + t + "." + segs[2]
		}
		return "feedback." + t
	}
	return path
}

// IsLegacyQueryField reports whether path uses a legacy prefix.
func IsLegacyQueryField(path string) bool {
	return strings.HasPrefix(path, "summary.weave.") || strings.HasPrefix(path, "feedback.wandb.")
}
