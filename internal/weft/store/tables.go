package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// TableRow is one row of a table query.
type TableRow struct {
	Digest        string `json:"digest"`
	Val           any    `json:"val"`
	OriginalIndex int    `json:"original_index"`

	valRaw string
}

// MarshalJSON emits val with its original key order.
func (r TableRow) MarshalJSON() ([]byte, error) {
	type plain TableRow
	if r.valRaw == "" || !json.Valid([]byte(r.valRaw)) {
		return json.Marshal(plain(r))
	}
	return json.Marshal(struct {
		plain
		Val json.RawMessage `json:"val"`
	}{plain(r), json.RawMessage(r.valRaw)})
}

const maxTableRows = 1_000_000

func (s *Store) insertRows(ctx context.Context, tx *sql.Tx, projectID string, rows []any, raws []json.RawMessage) ([]string, error) {
	n := len(rows)
	if raws != nil {
		n = len(raws)
	}
	digests := make([]string, n)
	for i := 0; i < n; i++ {
		var row any
		var raw []byte
		if raws != nil {
			raw = raws[i]
		} else {
			row = rows[i]
		}
		_, d, canon, err := prepValue(refs.DigesterFrom(ctx), row, raw)
		if err != nil {
			return nil, Invalidf("row %d is not JSON-serialisable", i)
		}
		digests[i] = d
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO table_rows(project_id, digest, val) VALUES(?,?,?)`, projectID, d, canon); err != nil {
			return nil, err
		}
	}
	return digests, nil
}

func (s *Store) insertTable(ctx context.Context, tx *sql.Tx, projectID string, rowDigests []string) (string, error) {
	if rowDigests == nil {
		rowDigests = []string{}
	}
	d := refs.DigesterFrom(ctx).Table(rowDigests)
	canon, err := refs.CanonicalJSON(rowDigests)
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO tables(project_id, digest, row_digests, created_at) VALUES(?,?,?,?)`,
		projectID, d, string(canon), nowMicros())
	return d, err
}

// CreateTable stores rows and returns the table digest and row digests.
func (s *Store) CreateTable(ctx context.Context, projectID string, rows []any) (string, []string, error) {
	return s.createTable(ctx, projectID, rows, nil)
}

// CreateTableRaw stores rows given as raw JSON, preserving key order.
func (s *Store) CreateTableRaw(ctx context.Context, projectID string, rows []json.RawMessage) (string, []string, error) {
	if rows == nil {
		rows = []json.RawMessage{}
	}
	return s.createTable(ctx, projectID, nil, rows)
}

func (s *Store) createTable(ctx context.Context, projectID string, rows []any, raws []json.RawMessage) (string, []string, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return "", nil, &InvalidError{Msg: err.Error()}
	}
	if len(rows) > maxTableRows || len(raws) > maxTableRows {
		return "", nil, Invalidf("too many rows")
	}
	var digest string
	var rowDigests []string
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.touchProject(ctx, tx, pid); err != nil {
			return err
		}
		var err error
		if rowDigests, err = s.insertRows(ctx, tx, pid, rows, raws); err != nil {
			return err
		}
		digest, err = s.insertTable(ctx, tx, pid, rowDigests)
		return err
	})
	return digest, nonNil(rowDigests), err
}

func (s *Store) tableRowDigests(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, projectID, digest string) ([]string, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT row_digests FROM tables WHERE project_id = ? AND digest = ?`, projectID, digest).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// TableUpdate is one /table/update operation.
type TableUpdate struct {
	Append *struct{ Row json.RawMessage } `json:"append"`
	Pop    *struct{ Index int }           `json:"pop"`
	Insert *struct {
		Index int
		Row   json.RawMessage
	} `json:"insert"`
	Replace *struct {
		Index int
		Row   json.RawMessage
	} `json:"replace"`
}

// UpdateTable applies updates to a table, producing a new table.
func (s *Store) UpdateTable(ctx context.Context, projectID, baseDigest string, updates []TableUpdate) (string, []string, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return "", nil, &InvalidError{Msg: err.Error()}
	}
	var digest string
	var updated []string
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		rows, err := s.tableRowDigests(ctx, tx, pid, baseDigest)
		if err != nil {
			return err
		}
		for i, u := range updates {
			switch {
			case u.Append != nil:
				ds, err := s.insertRows(ctx, tx, pid, nil, []json.RawMessage{rowOrNull(u.Append.Row)})
				if err != nil {
					return err
				}
				rows = append(rows, ds[0])
				updated = append(updated, ds[0])
			case u.Pop != nil:
				if u.Pop.Index < 0 || u.Pop.Index >= len(rows) {
					return Invalidf("update %d: pop index %d out of range", i, u.Pop.Index)
				}
				rows = append(rows[:u.Pop.Index], rows[u.Pop.Index+1:]...)
			case u.Insert != nil:
				if u.Insert.Index < 0 || u.Insert.Index > len(rows) {
					return Invalidf("update %d: insert index %d out of range", i, u.Insert.Index)
				}
				ds, err := s.insertRows(ctx, tx, pid, nil, []json.RawMessage{rowOrNull(u.Insert.Row)})
				if err != nil {
					return err
				}
				rows = append(rows[:u.Insert.Index], append([]string{ds[0]}, rows[u.Insert.Index:]...)...)
				updated = append(updated, ds[0])
			case u.Replace != nil:
				if u.Replace.Index < 0 || u.Replace.Index >= len(rows) {
					return Invalidf("update %d: replace index %d out of range", i, u.Replace.Index)
				}
				ds, err := s.insertRows(ctx, tx, pid, nil, []json.RawMessage{rowOrNull(u.Replace.Row)})
				if err != nil {
					return err
				}
				rows[u.Replace.Index] = ds[0]
				updated = append(updated, ds[0])
			default:
				return Invalidf("update %d: expected append, pop, insert or replace", i)
			}
		}
		if len(rows) > maxTableRows {
			return Invalidf("too many rows")
		}
		digest, err = s.insertTable(ctx, tx, pid, rows)
		return err
	})
	return digest, nonNil(updated), err
}

func rowOrNull(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage("null")
	}
	return r
}

// TableQuery is the body of /table/query.
type TableQuery struct {
	ProjectID string `json:"project_id"`
	Digest    string `json:"digest"`
	Filter    *struct {
		RowDigests []string `json:"row_digests"`
	} `json:"filter"`
	Limit  int      `json:"limit"`
	Offset int      `json:"offset"`
	SortBy []SortBy `json:"sort_by"`
}

// QueryTable returns rows of a table.
func (s *Store) QueryTable(ctx context.Context, q TableQuery) ([]TableRow, error) {
	pid, err := refs.NormalizeProject(q.ProjectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	if _, err := s.tableRowDigests(ctx, s.r, pid, q.Digest); err != nil {
		return nil, err
	}
	conds := []string{"t.project_id = ?", "t.digest = ?"}
	args := []any{pid, pid, q.Digest}
	if q.Filter != nil && len(q.Filter.RowDigests) > 0 {
		c, a := inList("j.value", q.Filter.RowDigests)
		conds = append(conds, c)
		args = append(args, a...)
	}
	order := "CAST(j.key AS INTEGER)"
	var orderArgs []any
	if len(q.SortBy) > 0 {
		var parts []string
		for _, sb := range q.SortBy {
			dir, err := sortDirection(sb.Direction)
			if err != nil {
				return nil, &InvalidError{Msg: err.Error()}
			}
			segs := splitField(sb.Field)
			if len(segs) > 0 && segs[0] == "val" {
				segs = segs[1:]
			}
			parts = append(parts, "json_extract(r.val, ?) "+dir)
			orderArgs = append(orderArgs, jsonPath(segs))
		}
		order = strings.Join(parts, ", ") + ", CAST(j.key AS INTEGER)"
	}
	limit := q.Limit
	if limit <= 0 || limit > 100000 {
		limit = 100000
	}
	args = append(args, orderArgs...)
	args = append(args, limit, q.Offset)
	rows, err := s.r.QueryContext(ctx, `SELECT j.key, j.value, r.val FROM tables t, json_each(t.row_digests) j
		JOIN table_rows r ON r.project_id = ? AND r.digest = j.value
		WHERE `+strings.Join(conds, " AND ")+` ORDER BY `+order+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TableRow{}
	for rows.Next() {
		var idx int
		var d, val string
		if err := rows.Scan(&idx, &d, &val); err != nil {
			return nil, err
		}
		out = append(out, TableRow{Digest: d, Val: decodeJSON(val), OriginalIndex: idx, valRaw: val})
	}
	return out, rows.Err()
}

// TableCount returns the number of rows in a table.
func (s *Store) TableCount(ctx context.Context, projectID, digest string) (int, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return 0, &InvalidError{Msg: err.Error()}
	}
	rows, err := s.tableRowDigests(ctx, s.r, pid, digest)
	return len(rows), err
}

// TableRowByDigest reads one row value.
func (s *Store) TableRowByDigest(ctx context.Context, projectID, rowDigest string) (any, error) {
	var val string
	err := s.r.QueryRowContext(ctx, `SELECT val FROM table_rows WHERE project_id = ? AND digest = ?`, projectID, rowDigest).Scan(&val)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return decodeJSON(val), nil
}

// ---------------------------------------------------------------- files

const maxFileSize = 64 << 20

// CreateFile stores content and returns its digest.
func (s *Store) CreateFile(ctx context.Context, projectID string, content []byte) (string, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return "", &InvalidError{Msg: err.Error()}
	}
	if len(content) > maxFileSize {
		return "", Invalidf("file too large")
	}
	d := refs.DigesterFrom(ctx).Bytes(content)
	return d, s.createFile(ctx, pid, d, content) // filebackend.go
}

// FileContent reads a file and sniffs its content type.
func (s *Store) FileContent(ctx context.Context, projectID, digest string) ([]byte, string, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, "", &InvalidError{Msg: err.Error()}
	}
	return s.fileContent(ctx, pid, digest) // filebackend.go
}

// ---------------------------------------------------------------- refs

// ReadRefs resolves refs to values; unresolvable refs yield nil.
func (s *Store) ReadRefs(ctx context.Context, refList []string) ([]any, error) {
	out := make([]any, len(refList))
	for i, r := range refList {
		v, err := s.ReadRef(ctx, r)
		if err != nil && !errors.Is(err, ErrNotFound) {
			var inv *InvalidError
			if errors.As(err, &inv) {
				continue
			}
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// ReadRef resolves one ref, walking its extra path.
func (s *Store) ReadRef(ctx context.Context, ref string) (any, error) {
	r, err := refs.Parse(ref)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	pid := r.ProjectID()
	var cur any
	switch r.Kind {
	case refs.KindObject, refs.KindFunction:
		o, err := s.ReadObject(ctx, pid, r.Name, r.Digest)
		if err != nil {
			return nil, err
		}
		cur = o.Val
	case refs.KindTable:
		cur = tableHandle{pid, r.Name}
	case refs.KindSpan:
		c, err := s.GetCall(ctx, pid, r.Name, false, false, false)
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(c)
		cur, _ = refs.Decode(b)
	}
	extra := r.Extra
	for len(extra) > 0 {
		// Follow nested refs before walking further.
		if str, ok := cur.(string); ok && refs.IsRef(str) {
			nr, err := refs.Parse(str)
			if err != nil {
				return nil, &InvalidError{Msg: err.Error()}
			}
			if nr.Kind == refs.KindTable && len(nr.Extra) == 0 {
				cur = tableHandle{nr.ProjectID(), nr.Name}
			} else {
				v, err := s.ReadRef(ctx, str)
				if err != nil {
					return nil, err
				}
				cur = v
			}
		}
		if len(extra) < 2 {
			return nil, Invalidf("ref path must be pairs like attr/<name>")
		}
		kind, key := extra[0], extra[1]
		extra = extra[2:]
		switch t := cur.(type) {
		case tableHandle:
			switch kind {
			case "id":
				v, err := s.TableRowByDigest(ctx, t.projectID, key)
				if err != nil {
					return nil, err
				}
				cur = v
			case "index":
				n, err := strconv.Atoi(key)
				if err != nil {
					return nil, Invalidf("bad index %q", key)
				}
				ds, err := s.tableRowDigests(ctx, s.r, t.projectID, t.digest)
				if err != nil {
					return nil, err
				}
				if n < 0 || n >= len(ds) {
					return nil, ErrNotFound
				}
				v, err := s.TableRowByDigest(ctx, t.projectID, ds[n])
				if err != nil {
					return nil, err
				}
				cur = v
			default:
				return nil, Invalidf("cannot walk %q into a table", kind)
			}
		case map[string]any:
			if kind != "attr" && kind != "key" {
				return nil, Invalidf("cannot walk %q into an object", kind)
			}
			v, ok := t[key]
			if !ok {
				return nil, ErrNotFound
			}
			cur = v
		case []any:
			if kind != "index" {
				return nil, Invalidf("cannot walk %q into a list", kind)
			}
			n, err := strconv.Atoi(key)
			if err != nil || n < 0 || n >= len(t) {
				return nil, ErrNotFound
			}
			cur = t[n]
		default:
			return nil, ErrNotFound
		}
	}
	// A walk that ends on a nested ref resolves it (e.g. dataset/attr/rows).
	if str, ok := cur.(string); ok && len(r.Extra) > 0 && refs.IsRef(str) {
		nr, err := refs.Parse(str)
		if err != nil {
			return nil, &InvalidError{Msg: err.Error()}
		}
		if nr.Kind == refs.KindTable && len(nr.Extra) == 0 {
			cur = tableHandle{nr.ProjectID(), nr.Name}
		} else {
			return s.ReadRef(ctx, str)
		}
	}
	if t, ok := cur.(tableHandle); ok {
		rows, err := s.QueryTable(ctx, TableQuery{ProjectID: t.projectID, Digest: t.digest, Limit: 10000})
		if err != nil {
			return nil, err
		}
		vals := make([]any, len(rows))
		for i, row := range rows {
			vals[i] = row.Val
		}
		return vals, nil
	}
	return cur, nil
}

type tableHandle struct{ projectID, digest string }
