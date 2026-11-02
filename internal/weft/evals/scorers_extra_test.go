package evals

import (
	"reflect"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/llm"
)

func TestValidXML(t *testing.T) {
	for in, want := range map[string]bool{
		"<a/>": true, "<a><b>x</b></a>": true, `<?xml version="1.0"?><r/>`: true,
		"<a>": false, "<a></b>": false, "<a/><b/>": false, "plain": false, "": false, "<a/> trailing": false,
	} {
		if got := validXML(in); got != want {
			t.Errorf("validXML(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestValidateSchema(t *testing.T) {
	schema := map[string]any{
		"type": "object", "required": []any{"id"}, "additionalProperties": false,
		"properties": map[string]any{
			"id":   map[string]any{"type": "integer", "minimum": 1.0},
			"tags": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []any{"a", "b"}}, "maxItems": 2.0},
			"kind": map[string]any{"anyOf": []any{map[string]any{"const": "x"}, map[string]any{"type": "null"}}},
		},
	}
	ok := map[string]any{"id": 3.0, "tags": []any{"a"}, "kind": "x"}
	if errs := validateSchema(schema, ok, "$", nil); len(errs) != 0 {
		t.Fatalf("valid doc rejected: %v", errs)
	}
	bad := map[string]any{"id": 0.5, "tags": []any{"c", "a", "b"}, "kind": "y", "extra": true}
	errs := validateSchema(schema, bad, "$", nil)
	if len(errs) != 5 {
		t.Fatalf("expected 5 errors, got %d: %v", len(errs), errs)
	}
}

func TestDetectPII(t *testing.T) {
	got := DetectPII("card 4111 1111 1111 1111, ssn 123-45-6789, ip 10.0.0.1, x@y.io")
	want := []string{"email", "ssn", "credit_card", "ip_address"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DetectPII = %v, want %v", got, want)
	}
	if got := DetectPII("order 1234 5678 9012 3456 shipped"); len(got) != 0 {
		t.Fatalf("non-Luhn number flagged: %v", got)
	}
}

func TestMockEmbeddingSimilarity(t *testing.T) {
	a, b, c := llm.MockEmbedding("the cat sat"), llm.MockEmbedding("The cat sat!"), llm.MockEmbedding("quarterly revenue report")
	if s := llm.CosineSimilarity(a, b); s < 0.999 {
		t.Fatalf("same words should match, got %v", s)
	}
	if s := llm.CosineSimilarity(a, c); s > 0.5 {
		t.Fatalf("different text too similar: %v", s)
	}
}

func TestIsEmptyOutput(t *testing.T) {
	chat := func(content any, tools []any) map[string]any {
		msg := map[string]any{"role": "assistant", "content": content}
		if tools != nil {
			msg["tool_calls"] = tools
		}
		return map[string]any{"choices": []any{map[string]any{"message": msg}}}
	}
	for i, c := range []struct {
		v    any
		want bool
	}{{nil, true}, {"  ", true}, {map[string]any{}, true}, {"x", false}, {chat("", nil), true}, {chat("", []any{map[string]any{}}), false}, {chat("hi", nil), false}} {
		if got := isEmptyOutput(c.v); got != c.want {
			t.Errorf("case %d: got %v want %v", i, got, c.want)
		}
	}
}

func TestEvalExpr(t *testing.T) {
	doc := map[string]any{"output": "Sorry, I can't", "inputs": map[string]any{"n": "7", "tags": []any{"a", "b"}}, "n": 2.0}
	lit := func(v any) any { return map[string]any{"$literal": v} }
	get := func(p string) any { return map[string]any{"$getField": p} }
	for i, c := range []struct {
		expr any
		want any
	}{
		{map[string]any{"$contains": map[string]any{"input": get("output"), "substr": lit("sorry"), "case_insensitive": true}}, true},
		{map[string]any{"$contains": map[string]any{"input": get("output"), "substr": lit("sorry")}}, false},
		{map[string]any{"$expr": map[string]any{"$gt": []any{map[string]any{"$convert": map[string]any{"input": get("inputs.n"), "to": "double"}}, lit(5.0)}}}, true},
		{map[string]any{"$and": []any{map[string]any{"$eq": []any{get("n"), lit(2.0)}}, map[string]any{"$in": []any{get("inputs.tags.1"), []any{lit("b")}}}}}, true},
		{map[string]any{"$not": []any{map[string]any{"$lt": []any{get("missing"), lit(1.0)}}}}, true},
		{get("inputs.n"), "7"},
	} {
		got, err := EvalExpr(c.expr, doc)
		if err != nil || got != c.want {
			t.Errorf("case %d: got %v (%v), want %v", i, got, err, c.want)
		}
	}
	if _, err := EvalExpr(map[string]any{"$bogus": 1}, doc); err == nil {
		t.Error("unknown operator accepted")
	}
}
