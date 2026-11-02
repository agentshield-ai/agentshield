package evals

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// EvalExpr evaluates a query-language operation (API.md; optionally wrapped
// in {"$expr": ...}) against an in-memory document, with the semantics of the
// SDKs' local evaluators (sdk/python/src/weft/_expr.py). RuleScorer uses it.
func EvalExpr(expr any, doc map[string]any) (any, error) {
	m, ok := expr.(map[string]any)
	if ok && len(m) == 1 {
		if inner, ok := m["$expr"]; ok {
			return EvalExpr(inner, doc)
		}
	}
	if !ok || len(m) != 1 {
		return expr, nil
	}
	var op string
	var arg any
	for k, v := range m {
		op, arg = k, v
	}
	list := func() ([]any, error) {
		l, ok := arg.([]any)
		if !ok {
			return nil, fmt.Errorf("%s expects a list", op)
		}
		return l, nil
	}
	switch op {
	case "$literal":
		return arg, nil
	case "$getField":
		p, _ := arg.(string)
		return exprPath(doc, p), nil
	case "$and", "$or":
		l, err := list()
		if err != nil {
			return nil, err
		}
		for _, x := range l {
			v, err := EvalExpr(x, doc)
			if err != nil {
				return nil, err
			}
			if truthy(v) == (op == "$or") {
				return op == "$or", nil
			}
		}
		return op == "$and", nil
	case "$not":
		x := arg
		if l, ok := arg.([]any); ok && len(l) > 0 {
			x = l[0]
		}
		v, err := EvalExpr(x, doc)
		if err != nil {
			return nil, err
		}
		return !truthy(v), nil
	case "$eq", "$gt", "$gte", "$lt", "$lte":
		l, err := list()
		if err != nil || len(l) != 2 {
			return nil, fmt.Errorf("%s expects two operands", op)
		}
		a, err := EvalExpr(l[0], doc)
		if err != nil {
			return nil, err
		}
		b, err := EvalExpr(l[1], doc)
		if err != nil {
			return nil, err
		}
		return exprCompare(op, a, b), nil
	case "$in":
		l, err := list()
		if err != nil || len(l) != 2 {
			return nil, fmt.Errorf("$in expects [value, [options]]")
		}
		v, err := EvalExpr(l[0], doc)
		if err != nil {
			return nil, err
		}
		opts, _ := l[1].([]any)
		for _, o := range opts {
			ov, err := EvalExpr(o, doc)
			if err != nil {
				return nil, err
			}
			if exprCompare("$eq", v, ov) {
				return true, nil
			}
		}
		return false, nil
	case "$contains":
		a, _ := arg.(map[string]any)
		hay, err := EvalExpr(a["input"], doc)
		if err != nil {
			return nil, err
		}
		needle, err := EvalExpr(a["substr"], doc)
		if err != nil {
			return nil, err
		}
		h, n := exprString(hay), exprString(needle)
		if ci, _ := a["case_insensitive"].(bool); ci {
			h, n = strings.ToLower(h), strings.ToLower(n)
		}
		return strings.Contains(h, n), nil
	case "$convert":
		a, _ := arg.(map[string]any)
		v, err := EvalExpr(a["input"], doc)
		if err != nil {
			return nil, err
		}
		to, _ := a["to"].(string)
		return exprConvert(v, to)
	}
	return nil, fmt.Errorf("unsupported operator %q", op)
}

func exprPath(doc any, path string) any {
	cur := doc
	for _, part := range strings.Split(path, ".") {
		switch c := cur.(type) {
		case map[string]any:
			cur = c[part]
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(c) {
				return nil
			}
			cur = c[i]
		default:
			return nil
		}
	}
	return cur
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	if f, ok := store.ToFloat(v); ok {
		return f != 0
	}
	return true
}

func isNumber(v any) (float64, bool) {
	if _, b := v.(bool); b {
		return 0, false
	}
	return store.ToFloat(v)
}

func exprCompare(op string, a, b any) bool {
	an, aNum := isNumber(a)
	bn, bNum := isNumber(b)
	if op == "$eq" {
		if aNum && bNum {
			return an == bn
		}
		return jsonEqual(a, b)
	}
	if a == nil || b == nil {
		return false
	}
	var c int
	switch {
	case aNum && bNum:
		switch {
		case an < bn:
			c = -1
		case an > bn:
			c = 1
		}
	default:
		as, aok := a.(string)
		bs, bok := b.(string)
		if !aok || !bok {
			return false
		}
		c = strings.Compare(as, bs)
	}
	switch op {
	case "$gt":
		return c > 0
	case "$gte":
		return c >= 0
	case "$lt":
		return c < 0
	case "$lte":
		return c <= 0
	}
	return false
}

func exprString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	}
	return Stringify(v)
}

func exprConvert(v any, to string) (any, error) {
	switch to {
	case "double", "int":
		var f float64
		if s, ok := v.(string); ok {
			p, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
			if err != nil {
				return nil, nil
			}
			f = p
		} else if n, ok := store.ToFloat(v); ok {
			f = n
		} else {
			return nil, nil
		}
		if to == "int" {
			return float64(int64(f)), nil
		}
		return f, nil
	case "string":
		return exprString(v), nil
	case "bool":
		return truthy(v), nil
	case "exists":
		return v != nil, nil
	}
	return nil, fmt.Errorf("unknown $convert target %q", to)
}
