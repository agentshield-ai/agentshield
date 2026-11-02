// Package refs implements Weft's content addressing: canonical JSON,
// digests and weft:/// reference URIs.
package refs

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Kind is the kind of thing a ref points at.
type Kind string

const (
	KindObject Kind = "object"
	KindOp     Kind = "op"
	KindTable  Kind = "table"
	KindCall   Kind = "call"
)

// Ref is a parsed weft:/// (or weave:///) URI.
type Ref struct {
	Entity  string
	Project string
	Kind    Kind
	// Name is the object/op name, the table digest or the call id.
	Name string
	// Digest is the version digest for object and op refs.
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
	case KindObject, KindOp:
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

// IsRef reports whether s looks like a weft or weave ref URI.
func IsRef(s string) bool {
	return strings.HasPrefix(s, "weft:///") || strings.HasPrefix(s, "weave:///")
}

// Normalize rewrites a weave:/// ref to the weft:/// scheme; other strings are returned unchanged.
func Normalize(s string) string {
	if strings.HasPrefix(s, "weave:///") {
		return "weft:///" + s[len("weave:///"):]
	}
	return s
}

// Parse parses a ref URI.
func Parse(s string) (Ref, error) {
	var rest string
	switch {
	case strings.HasPrefix(s, "weft:///"):
		rest = s[len("weft:///"):]
	case strings.HasPrefix(s, "weave:///"):
		rest = s[len("weave:///"):]
	default:
		return Ref{}, fmt.Errorf("not a ref: %q", s)
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 4 {
		return Ref{}, fmt.Errorf("malformed ref: %q", s)
	}
	r := Ref{Entity: parts[0], Project: parts[1], Kind: Kind(parts[2])}
	head, err := url.PathUnescape(parts[3])
	if err != nil {
		return Ref{}, fmt.Errorf("malformed ref: %q", s)
	}
	switch r.Kind {
	case KindObject, KindOp:
		i := strings.LastIndexByte(head, ':')
		if i < 0 {
			r.Name = head
			r.Digest = "latest"
		} else {
			r.Name, r.Digest = head[:i], head[i+1:]
		}
	case KindTable, KindCall:
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

// ObjectRef builds an object or op ref.
func ObjectRef(projectID, name, digest string, op bool) string {
	ent, proj := SplitProject(projectID)
	k := KindObject
	if op {
		k = KindOp
	}
	return Ref{Entity: ent, Project: proj, Kind: k, Name: name, Digest: digest}.String()
}

// TableRef builds a table ref.
func TableRef(projectID, digest string) string {
	ent, proj := SplitProject(projectID)
	return Ref{Entity: ent, Project: proj, Kind: KindTable, Name: digest}.String()
}

// CallRef builds a call ref.
func CallRef(projectID, callID string) string {
	ent, proj := SplitProject(projectID)
	return Ref{Entity: ent, Project: proj, Kind: KindCall, Name: callID}.String()
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

// DigestBytes is the unpadded base64url SHA-256 of b.
func DigestBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Digest returns the digest of v's canonical JSON.
func Digest(v any) (string, []byte, error) {
	b, err := CanonicalJSON(v)
	if err != nil {
		return "", nil, err
	}
	return DigestBytes(b), b, nil
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

// NormalizeValue rewrites every weave:/// ref string inside v to weft:///.
func NormalizeValue(v any) any {
	switch t := v.(type) {
	case string:
		return Normalize(t)
	case map[string]any:
		for k, x := range t {
			t[k] = NormalizeValue(x)
		}
		return t
	case []any:
		for i, x := range t {
			t[i] = NormalizeValue(x)
		}
		return t
	default:
		return v
	}
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
