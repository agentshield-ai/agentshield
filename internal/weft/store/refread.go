package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// Reading refs (POST /api/v1/refs/read, the adapter's refs route,
// expand_columns, evaluations). A ref's value is walked as stored JSON, so
// objects and rows keep the publisher's key order and number text; refs met
// on the way are followed only into projects the context's ref scope allows
// (refscope.go).

// maxRefDepth bounds how many nested refs one read follows (a value may
// refer to itself).
const maxRefDepth = 16

// errRefDenied marks a nested ref into a project the caller may not read;
// the whole ref then reads as null.
var errRefDenied = errors.New("ref into a project this request may not read")

type tableHandle struct{ projectID, digest string }

// refNode is a value reached while reading a ref: stored JSON, or a table
// (whose rows are read only when the walk ends on it), and the project the
// value belongs to.
type refNode struct {
	raw   json.RawMessage
	table *tableHandle
	pid   string
}

// ReadRefs resolves refs to decoded values. Unknown refs, refs that cannot
// be walked and refs into projects the context may not read yield nil.
func (s *Store) ReadRefs(ctx context.Context, refList []string) ([]any, error) {
	out := make([]any, len(refList))
	for i, r := range refList {
		raw, err := s.ReadRefRaw(ctx, r)
		if err != nil {
			if refUnreadable(err) {
				continue
			}
			var fe *ForbiddenError
			if errors.As(err, &fe) {
				continue
			}
			return nil, err
		}
		out[i], _ = refs.Decode(raw)
	}
	return out, nil
}

// ReadRefsRaw resolves refs to their stored JSON (null for unknown refs,
// refs that cannot be walked and nested refs into projects the context may
// not read). A top-level ref into such a project is a ForbiddenError.
func (s *Store) ReadRefsRaw(ctx context.Context, refList []string) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, len(refList))
	for i, r := range refList {
		raw, err := s.ReadRefRaw(ctx, r)
		if err != nil {
			if refUnreadable(err) {
				out[i] = json.RawMessage("null")
				continue
			}
			return nil, err
		}
		out[i] = raw
	}
	return out, nil
}

func refUnreadable(err error) bool {
	var inv *InvalidError
	return errors.Is(err, ErrNotFound) || errors.Is(err, errRefDenied) || errors.As(err, &inv)
}

// ReadRef resolves one ref, walking its extra path, to a decoded value.
func (s *Store) ReadRef(ctx context.Context, ref string) (any, error) {
	raw, err := s.ReadRefRaw(ctx, ref)
	if err != nil {
		return nil, err
	}
	return refs.Decode(raw)
}

// ReadRefRaw resolves one ref, walking its extra path, to its stored JSON.
func (s *Store) ReadRefRaw(ctx context.Context, ref string) (json.RawMessage, error) {
	n, err := s.readRef(ctx, ref, "", 0)
	if err != nil {
		return nil, err
	}
	return s.nodeJSON(ctx, n)
}

// readRef resolves ref, found in a value of project origin ("" for a
// top-level ref), to a node.
func (s *Store) readRef(ctx context.Context, ref, origin string, depth int) (refNode, error) {
	if depth > maxRefDepth {
		return refNode{}, Invalidf("refs nested too deeply")
	}
	r, err := refs.Parse(ref)
	if err != nil {
		return refNode{}, &InvalidError{Msg: err.Error()}
	}
	pid := r.ProjectID()
	if origin == "" {
		if err := CheckRefProject(ctx, ref, pid); err != nil {
			return refNode{}, err
		}
	} else if !canFollow(ctx, origin, pid) {
		return refNode{}, errRefDenied
	}
	cur := refNode{pid: pid}
	switch r.Kind {
	case refs.KindObject, refs.KindFunction:
		o, err := s.ReadObject(ctx, pid, r.Name, r.Digest)
		if err != nil {
			return refNode{}, err
		}
		if o.valRaw != "" && json.Valid([]byte(o.valRaw)) {
			cur.raw = json.RawMessage(o.valRaw)
		} else if cur.raw, err = json.Marshal(o.Val); err != nil {
			return refNode{}, err
		}
	case refs.KindTable:
		cur.table = &tableHandle{pid, r.Name}
	case refs.KindSpan:
		c, err := s.GetCall(ctx, pid, r.Name, false, false, false)
		if err != nil {
			return refNode{}, err
		}
		if cur.raw, err = json.Marshal(spanValue(ctx, c)); err != nil {
			return refNode{}, err
		}
	default:
		return refNode{}, Invalidf("unknown ref kind %q", r.Kind)
	}
	extra := r.Extra
	for len(extra) > 0 {
		// Follow nested refs before walking further.
		if cur, err = s.followNested(ctx, cur, depth); err != nil {
			return refNode{}, err
		}
		if len(extra) < 2 {
			return refNode{}, Invalidf("ref path must be pairs like attr/<name>")
		}
		kind, key := extra[0], extra[1]
		extra = extra[2:]
		if cur.table != nil {
			t := cur.table
			var rowDigest string
			switch kind {
			case "id":
				rowDigest = key
			case "index":
				n, err := strconv.Atoi(key)
				if err != nil {
					return refNode{}, Invalidf("bad index %q", key)
				}
				ds, err := s.tableRowDigests(ctx, s.r, t.projectID, t.digest)
				if err != nil {
					return refNode{}, err
				}
				if n < 0 || n >= len(ds) {
					return refNode{}, ErrNotFound
				}
				rowDigest = ds[n]
			default:
				return refNode{}, Invalidf("cannot walk %q into a table", kind)
			}
			raw, err := s.tableRowRaw(ctx, t.projectID, rowDigest)
			if err != nil {
				return refNode{}, err
			}
			cur = refNode{raw: raw, pid: t.projectID}
			continue
		}
		next, err := walkJSON(cur.raw, kind, key)
		if err != nil {
			return refNode{}, err
		}
		cur.raw = next
	}
	// A walk that ends on a nested ref resolves it (e.g. dataset/attr/rows).
	if len(r.Extra) > 0 {
		return s.followNested(ctx, cur, depth)
	}
	return cur, nil
}

// followNested resolves cur when it is a JSON string holding a ref.
func (s *Store) followNested(ctx context.Context, cur refNode, depth int) (refNode, error) {
	if cur.table != nil || len(cur.raw) == 0 || cur.raw[0] != '"' {
		return cur, nil
	}
	var str string
	if json.Unmarshal(cur.raw, &str) != nil || !refs.IsRef(str) {
		return cur, nil
	}
	nr, err := refs.Parse(str)
	if err != nil {
		return refNode{}, &InvalidError{Msg: err.Error()}
	}
	if nr.Kind == refs.KindTable && len(nr.Extra) == 0 {
		if !canFollow(ctx, cur.pid, nr.ProjectID()) {
			return refNode{}, errRefDenied
		}
		return refNode{table: &tableHandle{nr.ProjectID(), nr.Name}, pid: nr.ProjectID()}, nil
	}
	return s.readRef(ctx, str, cur.pid, depth+1)
}

// nodeJSON is a node's JSON: a table's rows as a list.
func (s *Store) nodeJSON(ctx context.Context, n refNode) (json.RawMessage, error) {
	if n.table == nil {
		return n.raw, nil
	}
	rows, err := s.QueryTable(ctx, TableQuery{ProjectID: n.table.projectID, Digest: n.table.digest, Limit: 10000})
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteByte('[')
	for i, row := range rows {
		if i > 0 {
			b.WriteByte(',')
		}
		if row.valRaw != "" && json.Valid([]byte(row.valRaw)) {
			b.WriteString(row.valRaw)
		} else {
			v, err := json.Marshal(row.Val)
			if err != nil {
				return nil, err
			}
			b.Write(v)
		}
	}
	b.WriteByte(']')
	return b.Bytes(), nil
}

// tableRowRaw reads one row's stored JSON.
func (s *Store) tableRowRaw(ctx context.Context, projectID, rowDigest string) (json.RawMessage, error) {
	var val string
	err := s.r.QueryRowContext(ctx, `SELECT val FROM table_rows WHERE project_id = ? AND digest = ?`, projectID, rowDigest).Scan(&val)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if !json.Valid([]byte(val)) {
		return nil, ErrNotFound
	}
	return json.RawMessage(val), nil
}

// walkJSON takes one step (attr/key into an object, index into a list) in
// stored JSON without decoding the rest, so the result keeps its key order
// and number text. As when decoding, the last of duplicate keys wins.
func walkJSON(raw json.RawMessage, kind, key string) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, ErrNotFound
	}
	switch tok {
	case json.Delim('{'):
		if kind != "attr" && kind != "key" {
			return nil, Invalidf("cannot walk %q into an object", kind)
		}
		var found json.RawMessage
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, ErrNotFound
			}
			var v json.RawMessage
			if err := dec.Decode(&v); err != nil {
				return nil, ErrNotFound
			}
			if k, _ := kt.(string); k == key {
				found = v
			}
		}
		if found == nil {
			return nil, ErrNotFound
		}
		return found, nil
	case json.Delim('['):
		if kind != "index" {
			return nil, Invalidf("cannot walk %q into a list", kind)
		}
		n, err := strconv.Atoi(key)
		if err != nil || n < 0 {
			return nil, ErrNotFound
		}
		for i := 0; dec.More(); i++ {
			var v json.RawMessage
			if err := dec.Decode(&v); err != nil {
				return nil, ErrNotFound
			}
			if i == n {
				return v, nil
			}
		}
		return nil, ErrNotFound
	}
	return nil, ErrNotFound
}
