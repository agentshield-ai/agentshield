package store

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// The query language (see docs/weft/API.md) is a small Mongo-style
// expression tree. It is compiled to a parameterised SQLite boolean
// expression. Field paths are resolved by a per-table resolver so the same
// compiler serves calls, feedback and costs.

// fieldResolver maps a dotted field path to a SQL expression. timeField
// marks columns stored as unix microseconds so that string literals compared
// against them are parsed as timestamps.
type fieldResolver func(path string) (expr string, args []any, timeField bool, err error)

type exprCompiler struct {
	field fieldResolver
	depth int
	// nodes counts operators and non-literal operands, literals the
	// literal operands (a $in list's items, mostly): a query's cost, both to
	// compile and per row evaluated, is bounded by them.
	nodes, literals int
}

// Query size limits (docs/weft/API.md "Query language"). Each operand is
// compiled once, so the SQL grows linearly with the query; the budgets
// bound it and the per-row work, whoever sends the query (a reader's
// spans/query, a saved Monitor run on every ended span).
const (
	maxExprDepth    = 64
	maxExprNodes    = 1000
	maxExprLiterals = 10000
	maxExprSQL      = 256 << 10
)

// ValidateCallsQuery compiles a spans query ({"$expr": ...}) as a spans
// query would, so a saved query (a Monitor's) that could never run is
// refused when it is saved.
func ValidateCallsQuery(q any) error {
	_, _, err := compileQuery(q, callField)
	return err
}

// validateSavedQuery refuses a Monitor whose query does not compile within
// the limits: monitors run it on every ended span of their project.
func validateSavedQuery(base *string, val any) error {
	if base == nil || *base != "Monitor" {
		return nil
	}
	m, _ := val.(map[string]any)
	if q, ok := m["query"]; ok && q != nil {
		if err := ValidateCallsQuery(q); err != nil {
			return Invalidf("invalid monitor query: %v", err)
		}
	}
	return nil
}

func (c *exprCompiler) node() error {
	c.nodes++
	if c.nodes > maxExprNodes {
		return Invalidf("query too large: more than %d operations", maxExprNodes)
	}
	return nil
}

func (c *exprCompiler) literalNode() error {
	c.literals++
	if c.literals > maxExprLiterals {
		return Invalidf("query too large: more than %d literals", maxExprLiterals)
	}
	return nil
}

// compileQuery compiles {"$expr": op} (or a bare op) into SQL.
func compileQuery(q any, field fieldResolver) (string, []any, error) {
	if q == nil {
		return "", nil, nil
	}
	m, ok := q.(map[string]any)
	if !ok {
		return "", nil, Invalidf("query must be an object")
	}
	if len(m) == 0 {
		return "", nil, nil
	}
	if e, ok := m["$expr"]; ok {
		if e == nil {
			return "", nil, nil
		}
		if em, ok := e.(map[string]any); ok && len(em) == 0 {
			return "", nil, nil
		}
		m2, ok := e.(map[string]any)
		if !ok {
			return "", nil, Invalidf("$expr must be an object")
		}
		m = m2
	}
	c := &exprCompiler{field: field}
	sql, args, err := c.op(m)
	if err != nil {
		return "", nil, err
	}
	if len(sql) > maxExprSQL {
		return "", nil, Invalidf("query too large")
	}
	return sql, args, nil
}

func (c *exprCompiler) op(v any) (string, []any, error) {
	c.depth++
	defer func() { c.depth-- }()
	if c.depth > maxExprDepth {
		return "", nil, Invalidf("query nested too deeply")
	}
	if err := c.node(); err != nil {
		return "", nil, err
	}
	m, ok := v.(map[string]any)
	if !ok || len(m) != 1 {
		return "", nil, Invalidf("operation must be an object with exactly one operator")
	}
	for key, arg := range m {
		switch key {
		case "$and", "$or":
			list, ok := arg.([]any)
			if !ok {
				return "", nil, Invalidf("%s expects a list", key)
			}
			if len(list) == 0 {
				if key == "$and" {
					return "1", nil, nil
				}
				return "0", nil, nil
			}
			parts := make([]string, 0, len(list))
			var args []any
			for _, x := range list {
				s, a, err := c.op(x)
				if err != nil {
					return "", nil, err
				}
				parts = append(parts, "("+s+")")
				args = append(args, a...)
			}
			joiner := " AND "
			if key == "$or" {
				joiner = " OR "
			}
			return strings.Join(parts, joiner), args, nil
		case "$not":
			list, ok := arg.([]any)
			var inner any = arg
			if ok {
				if len(list) != 1 {
					return "", nil, Invalidf("$not expects one operation")
				}
				inner = list[0]
			}
			s, a, err := c.op(inner)
			if err != nil {
				return "", nil, err
			}
			return "NOT COALESCE((" + s + "), 0)", a, nil
		case "$eq", "$gt", "$gte", "$lt", "$lte":
			list, ok := arg.([]any)
			if !ok || len(list) != 2 {
				return "", nil, Invalidf("%s expects two operands", key)
			}
			return c.compare(key, list[0], list[1])
		case "$in":
			list, ok := arg.([]any)
			if !ok || len(list) != 2 {
				return "", nil, Invalidf("$in expects [operand, [operands]]")
			}
			lhs, la, isTime, err := c.operand(list[0], false)
			if err != nil {
				return "", nil, err
			}
			items, ok := list[1].([]any)
			if !ok {
				return "", nil, Invalidf("$in expects a list of operands")
			}
			if len(items) == 0 {
				return "0", nil, nil
			}
			ph := make([]string, 0, len(items))
			args := append([]any{}, la...)
			for _, it := range items {
				if refColumnSQL[lhs] {
					it = normRefLiteral(it)
				}
				s, a, _, err := c.operand(it, isTime)
				if err != nil {
					return "", nil, err
				}
				ph = append(ph, s)
				args = append(args, a...)
			}
			return lhs + " IN (" + strings.Join(ph, ", ") + ")", args, nil
		case "$contains":
			cm, ok := arg.(map[string]any)
			if !ok {
				return "", nil, Invalidf("$contains expects an object")
			}
			in, ia, _, err := c.operand(cm["input"], false)
			if err != nil {
				return "", nil, err
			}
			sub, sa, _, err := c.operand(cm["substr"], false)
			if err != nil {
				return "", nil, err
			}
			ci, _ := cm["case_insensitive"].(bool)
			args := append(ia, sa...)
			if ci {
				return "instr(lower(CAST(" + in + " AS TEXT)), lower(CAST(" + sub + " AS TEXT))) > 0", args, nil
			}
			return "instr(CAST(" + in + " AS TEXT), CAST(" + sub + " AS TEXT)) > 0", args, nil
		default:
			return "", nil, Invalidf("unknown operator %q", key)
		}
	}
	return "", nil, Invalidf("empty operation")
}

var cmpOps = map[string]string{"$eq": "=", "$gt": ">", "$gte": ">=", "$lt": "<", "$lte": "<="}

func (c *exprCompiler) compare(op string, a, b any) (string, []any, error) {
	// Each side is compiled once. A literal compared with a time field is
	// read as a timestamp, and one compared with a ref column as a ref:
	// only a literal side (cheap to compile) is compiled again for that.
	ls, la, aTime, err := c.operand(a, false)
	if err != nil {
		return "", nil, err
	}
	if refColumnSQL[ls] {
		b = normRefLiteral(b)
	}
	rs, ra, bTime, err := c.operand(b, aTime)
	if err != nil {
		return "", nil, err
	}
	if (bTime || refColumnSQL[rs]) && literalOperand(a) {
		if refColumnSQL[rs] {
			a = normRefLiteral(a)
		}
		if ls, la, _, err = c.operand(a, bTime); err != nil {
			return "", nil, err
		}
	}
	if op == "$eq" {
		if isNullLiteral(b) {
			return ls + " IS NULL", la, nil
		}
		if isNullLiteral(a) {
			return rs + " IS NULL", ra, nil
		}
	}
	return ls + " " + cmpOps[op] + " " + rs, append(append([]any{}, la...), ra...), nil
}

// literalOperand reports whether an operand is a literal, possibly under
// $convert: the only operands whose SQL depends on the other side of a
// comparison. Called on operands that compiled, so its depth is bounded.
func literalOperand(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return true
	}
	if len(m) != 1 {
		return false
	}
	if _, ok := m["$literal"]; ok {
		return true
	}
	if cv, ok := m["$convert"].(map[string]any); ok {
		return literalOperand(cv["input"])
	}
	return false
}

// refColumnSQL are columns holding refs, which are stored with the current
// kind names; a literal compared with one is read the same way, so a query
// may name a span or function ref with its old kind ("call", "op").
var refColumnSQL = map[string]bool{"f.ref": true, "f.call_ref": true, "f.runnable_ref": true, "f.annotation_ref": true,
	"f.trigger_ref": true, "c.op_name": true}

func normRefLiteral(v any) any {
	switch t := v.(type) {
	case string:
		return refs.Normalize(t)
	case map[string]any:
		if l, ok := t["$literal"].(string); ok && len(t) == 1 {
			return map[string]any{"$literal": refs.Normalize(l)}
		}
	}
	return v
}

func isNullLiteral(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	l, has := m["$literal"]
	return has && l == nil
}

// operand compiles an operand. asTime converts string literals to micros.
func (c *exprCompiler) operand(v any, asTime bool) (string, []any, bool, error) {
	m, ok := v.(map[string]any)
	if !ok {
		// Bare scalars are treated as literals for convenience.
		if err := c.literalNode(); err != nil {
			return "", nil, false, err
		}
		return literal(v, asTime)
	}
	if l, ok := m["$literal"]; ok && len(m) == 1 {
		if err := c.literalNode(); err != nil {
			return "", nil, false, err
		}
		return literal(l, asTime)
	}
	if f, ok := m["$getField"]; ok && len(m) == 1 {
		if err := c.node(); err != nil {
			return "", nil, false, err
		}
		path, ok := f.(string)
		if !ok || path == "" {
			return "", nil, false, Invalidf("$getField expects a field path")
		}
		s, a, isTime, err := c.field(path)
		return s, a, isTime, err
	}
	if cv, ok := m["$convert"]; ok && len(m) == 1 {
		// $convert nests like an operator: it counts towards the depth
		// and the operation budget.
		c.depth++
		defer func() { c.depth-- }()
		if c.depth > maxExprDepth {
			return "", nil, false, Invalidf("query nested too deeply")
		}
		if err := c.node(); err != nil {
			return "", nil, false, err
		}
		cm, ok := cv.(map[string]any)
		if !ok {
			return "", nil, false, Invalidf("$convert expects an object")
		}
		in, a, isTime, err := c.operand(cm["input"], asTime)
		if err != nil {
			return "", nil, false, err
		}
		to, _ := cm["to"].(string)
		switch to {
		case "double":
			return "CAST(" + in + " AS REAL)", a, isTime, nil
		case "int":
			return "CAST(" + in + " AS INTEGER)", a, isTime, nil
		case "string":
			return "CAST(" + in + " AS TEXT)", a, false, nil
		case "bool":
			// The input once: NULL and the false-like values are 0.
			return "COALESCE((" + in + ") NOT IN (0, '', 'false', 'False'), 0)", a, false, nil
		case "exists":
			return "(" + in + " IS NOT NULL)", a, false, nil
		default:
			return "", nil, false, Invalidf("$convert: unsupported target %q", to)
		}
	}
	s, a, err := c.op(m)
	return "(" + s + ")", a, false, err
}

func literal(v any, asTime bool) (string, []any, bool, error) {
	switch t := v.(type) {
	case nil:
		return "NULL", nil, false, nil
	case bool:
		if t {
			return "1", nil, false, nil
		}
		return "0", nil, false, nil
	case string:
		if asTime {
			us, err := ParseTime(t)
			if err == nil {
				return "?", []any{us}, false, nil
			}
		}
		return "?", []any{t}, false, nil
	case json.Number:
		if asTime {
			if f, err := t.Float64(); err == nil {
				return "?", []any{timeNumber(f)}, false, nil
			}
		}
		if i, err := t.Int64(); err == nil {
			return "?", []any{i}, false, nil
		}
		f, err := t.Float64()
		if err != nil {
			return "", nil, false, Invalidf("bad number %q", t)
		}
		return "?", []any{f}, false, nil
	case float64:
		if asTime {
			return "?", []any{timeNumber(t)}, false, nil
		}
		return "?", []any{t}, false, nil
	case int, int64:
		return "?", []any{t}, false, nil
	case map[string]any, []any:
		b, _ := json.Marshal(t)
		return "?", []any{string(b)}, false, nil
	default:
		return "", nil, false, Invalidf("unsupported literal %T", v)
	}
}

// timeNumber interprets a numeric timestamp: seconds, or micros when huge.
func timeNumber(f float64) int64 {
	if f > 1e14 {
		return int64(f)
	}
	return int64(f * 1e6)
}

// jsonPath builds a SQLite JSON path from dotted segments.
func jsonPath(segs []string) string {
	var b strings.Builder
	b.WriteByte('$')
	for _, s := range segs {
		if s == "" {
			continue
		}
		if _, err := strconv.Atoi(s); err == nil {
			b.WriteString("[" + s + "]")
			continue
		}
		b.WriteString(`."`)
		b.WriteString(strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`))
		b.WriteByte('"')
	}
	return b.String()
}

// jsonField returns json_extract(col, path) for a dotted path below col.
func jsonField(col string, rest []string) (string, []any) {
	return "json_extract(" + col + ", ?)", []any{jsonPath(rest)}
}

// splitField splits on dots, honouring a [bracketed.segment].
func splitField(path string) []string {
	var out []string
	for len(path) > 0 {
		if path[0] == '[' {
			if j := strings.IndexByte(path, ']'); j > 0 {
				out = append(out, path[1:j])
				path = strings.TrimPrefix(path[j+1:], ".")
				continue
			}
		}
		i := strings.IndexByte(path, '.')
		if i < 0 {
			out = append(out, path)
			break
		}
		out = append(out, path[:i])
		path = path[i+1:]
	}
	return out
}

func unknownField(path string) error { return Invalidf("unknown field %q", path) }

// sortDirection validates a sort direction.
func sortDirection(d string) (string, error) {
	switch strings.ToLower(d) {
	case "", "asc":
		return "ASC", nil
	case "desc":
		return "DESC", nil
	}
	return "", fmt.Errorf("invalid sort direction %q", d)
}
