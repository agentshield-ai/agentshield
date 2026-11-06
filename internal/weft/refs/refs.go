// Package refs implements Weft's content addressing: canonical JSON,
// digests and weft:/// reference URIs (docs/weft/DESIGN.md §10.2, §10.3).
package refs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Kind is the kind of thing a ref points at.
type Kind string

const (
	KindObject   Kind = "object"
	KindFunction Kind = "function"
	KindTable    Kind = "table"
	KindSpan     Kind = "span"
)

// legacyKinds are the kind names refs had before Weft's own API (§10.2).
// Parse accepts them and reads them as the current kind; nothing writes them.
var legacyKinds = map[string]Kind{"op": KindFunction, "call": KindSpan}

// Ref is a parsed weft:/// URI.
type Ref struct {
	Entity  string
	Project string
	Kind    Kind
	// Name is the object/function name, the table digest or the span id.
	Name string
	// Digest is the version digest for object and function refs.
	Digest string
	// Extra is the path walked into the value: pairs like attr/rows, id/<d>.
	Extra []string
}

// ProjectID returns "entity/project".
func (r Ref) ProjectID() string { return r.Entity + "/" + r.Project }

// String formats the ref with the weft scheme.
func (r Ref) String() string {
	var b strings.Builder
	b.WriteString("weft:///")
	b.WriteString(r.Entity)
	b.WriteByte('/')
	b.WriteString(r.Project)
	b.WriteByte('/')
	b.WriteString(string(r.Kind))
	b.WriteByte('/')
	switch r.Kind {
	case KindObject, KindFunction:
		b.WriteString(url.PathEscape(r.Name))
		b.WriteByte(':')
		b.WriteString(r.Digest)
	default:
		b.WriteString(r.Name)
	}
	for _, e := range r.Extra {
		b.WriteByte('/')
		b.WriteString(url.PathEscape(e))
	}
	return b.String()
}

// Base returns the ref without its extra path.
func (r Ref) Base() Ref {
	r.Extra = nil
	return r
}

// IsRef reports whether s looks like a weft ref URI.
func IsRef(s string) bool {
	return strings.HasPrefix(s, "weft:///")
}

// Parse parses a ref URI.
func Parse(s string) (Ref, error) {
	rest, ok := strings.CutPrefix(s, "weft:///")
	if !ok {
		return Ref{}, fmt.Errorf("not a ref: %q", s)
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 4 {
		return Ref{}, fmt.Errorf("malformed ref: %q", s)
	}
	r := Ref{Entity: parts[0], Project: parts[1], Kind: Kind(parts[2])}
	if k, ok := legacyKinds[parts[2]]; ok {
		r.Kind = k
	}
	head, err := url.PathUnescape(parts[3])
	if err != nil {
		return Ref{}, fmt.Errorf("malformed ref: %q", s)
	}
	switch r.Kind {
	case KindObject, KindFunction:
		i := strings.LastIndexByte(head, ':')
		if i < 0 {
			r.Name = head
			r.Digest = "latest"
		} else {
			r.Name, r.Digest = head[:i], head[i+1:]
		}
	case KindTable, KindSpan:
		r.Name = head
	default:
		return Ref{}, fmt.Errorf("unknown ref kind %q in %q", r.Kind, s)
	}
	if r.Name == "" || r.Entity == "" || r.Project == "" {
		return Ref{}, fmt.Errorf("malformed ref: %q", s)
	}
	for _, p := range parts[4:] {
		e, err := url.PathUnescape(p)
		if err != nil {
			return Ref{}, fmt.Errorf("malformed ref: %q", s)
		}
		r.Extra = append(r.Extra, e)
	}
	return r, nil
}

// ObjectRef builds an object or function ref.
func ObjectRef(projectID, name, digest string, function bool) string {
	ent, proj := SplitProject(projectID)
	k := KindObject
	if function {
		k = KindFunction
	}
	return Ref{Entity: ent, Project: proj, Kind: k, Name: name, Digest: digest}.String()
}

// TableRef builds a table ref.
func TableRef(projectID, digest string) string {
	ent, proj := SplitProject(projectID)
	return Ref{Entity: ent, Project: proj, Kind: KindTable, Name: digest}.String()
}

// SpanRef builds a span ref.
func SpanRef(projectID, spanID string) string {
	ent, proj := SplitProject(projectID)
	return Ref{Entity: ent, Project: proj, Kind: KindSpan, Name: spanID}.String()
}

// Normalize rewrites a ref written with a legacy kind name ("op", "call")
// to the current one ("function", "span"), keeping the rest of the string
// as it is. Other strings are returned unchanged.
func Normalize(s string) string {
	ent, proj, kind, rest, ok := splitKind(s)
	if !ok {
		return s
	}
	if k, legacy := legacyKinds[kind]; legacy {
		return "weft:///" + ent + "/" + proj + "/" + string(k) + "/" + rest
	}
	return s
}

// Variants returns s and, for a ref of a kind that had another name, the
// same ref spelled with that name, so stored values written before Weft's
// own API (which are content-addressed and never rewritten) still match.
func Variants(s string) []string {
	ent, proj, kind, rest, ok := splitKind(s)
	if !ok {
		return []string{s}
	}
	for old, k := range legacyKinds {
		switch kind {
		case old:
			return []string{"weft:///" + ent + "/" + proj + "/" + string(k) + "/" + rest, s}
		case string(k):
			return []string{s, "weft:///" + ent + "/" + proj + "/" + old + "/" + rest}
		}
	}
	return []string{s}
}

// splitKind splits weft:///<entity>/<project>/<kind>/<rest>.
func splitKind(s string) (ent, proj, kind, rest string, ok bool) {
	tail, found := strings.CutPrefix(s, "weft:///")
	if !found {
		return "", "", "", "", false
	}
	parts := strings.SplitN(tail, "/", 4)
	if len(parts) < 4 || parts[0] == "" || parts[1] == "" {
		return "", "", "", "", false
	}
	return parts[0], parts[1], parts[2], parts[3], true
}

// SplitProject splits "entity/project". A bare name gets the "default" entity.
func SplitProject(projectID string) (string, string) {
	if i := strings.IndexByte(projectID, '/'); i >= 0 {
		return projectID[:i], projectID[i+1:]
	}
	return "default", projectID
}

// NormalizeProject returns a project id in "entity/project" form, or an error.
func NormalizeProject(projectID string) (string, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "", fmt.Errorf("project_id is required")
	}
	ent, proj := SplitProject(projectID)
	if ent == "" || proj == "" || strings.Contains(proj, "/") {
		return "", fmt.Errorf("project_id must be \"entity/project\", got %q", projectID)
	}
	for _, part := range []string{ent, proj} {
		for _, r := range part {
			if r < 0x20 || r == ':' || r == '?' || r == '#' || r == '\\' {
				return "", fmt.Errorf("invalid character in project_id %q", projectID)
			}
		}
	}
	return ent + "/" + proj, nil
}

// ValidateNewProject checks a normalised project id for a project that does
// not exist yet. Besides what NormalizeProject refuses, a new project's
// entity and name may not contain '"', ';', '<', '>', DEL or other control
// characters, or be "." or "..": ids end up in file names, object-store
// keys, headers and HTML. Projects created before keep working.
func ValidateNewProject(projectID string) error {
	ent, proj := SplitProject(projectID)
	for _, part := range []string{ent, proj} {
		if part == "." || part == ".." {
			return fmt.Errorf("project_id parts may not be %q: %q", part, projectID)
		}
		for _, r := range part {
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == '"' || r == ';' || r == '<' || r == '>' {
				return fmt.Errorf("invalid character %q in project_id %q", r, projectID)
			}
		}
	}
	return nil
}

// CanonicalJSON encodes v with sorted keys, no insignificant whitespace and
// no HTML escaping. Numbers decoded with UseNumber keep their text form.
func CanonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Decode parses JSON into generic values, preserving number text.
func Decode(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// CollectRefs returns the ref strings found in v (depth limited).
func CollectRefs(v any) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(any, int)
	walk = func(x any, depth int) {
		if depth > 8 {
			return
		}
		switch t := x.(type) {
		case string:
			if IsRef(t) && !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		case map[string]any:
			for _, y := range t {
				walk(y, depth+1)
			}
		case []any:
			for _, y := range t {
				walk(y, depth+1)
			}
		}
	}
	walk(v, 0)
	return out
}
