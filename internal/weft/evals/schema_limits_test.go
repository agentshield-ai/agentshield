package evals

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// JSONSchemaScorer is bounded: patterns compile once per schema, schema size,
// depth and output size are capped, the work is counted and the context is
// honoured; above a cap the scorer returns an error.
func TestJSONSchemaScorerLimits(t *testing.T) {
	ctx := context.Background()
	score := func(ctx context.Context, schema map[string]any, output string) (any, error) {
		return scoreJSONSchema(nil, ctx, "e/p", map[string]any{"schema": schema}, nil, output, nil, nil)
	}
	items := make([]string, 2000)
	for i := range items {
		items[i] = `"abc"`
	}
	arr := "[" + strings.Join(items, ",") + "]"
	pat := map[string]any{"type": "array", "items": map[string]any{"type": "string", "pattern": "^a"}}

	// One compiled pattern for 2000 strings.
	sv := newSchemaValidator(ctx)
	v, _ := parseJSONOutput(arr)
	if errs := sv.validate(pat, v, "$", nil); len(errs) != 0 || sv.err != nil || len(sv.patterns) != 1 {
		t.Fatalf("errs %v err %v patterns %d", errs, sv.err, len(sv.patterns))
	}
	if out, err := score(ctx, pat, arr); err != nil || out != true {
		t.Fatalf("within limits: %v %v", out, err)
	}

	// Step budget (nested anyOf multiplies the work).
	prev := MaxSchemaSteps
	MaxSchemaSteps = 1000
	_, err := score(ctx, pat, arr)
	MaxSchemaSteps = prev
	if err == nil || !strings.Contains(err.Error(), "steps") {
		t.Fatalf("step budget: %v", err)
	}
	var nested any = map[string]any{"type": "string", "pattern": "^a"} // every item matches: no error cap
	for i := 0; i < 12; i++ {
		nested = map[string]any{"anyOf": []any{nested, nested}}
	}
	if _, err := score(ctx, map[string]any{"type": "array", "items": nested}, arr); err == nil || !strings.Contains(err.Error(), "steps") {
		t.Fatalf("exponential anyOf: %v", err)
	}

	// Cancellation.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	big := make([]string, 5000)
	for i := range big {
		big[i] = `"abc"`
	}
	if _, err := score(cctx, pat, "["+strings.Join(big, ",")+"]"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}

	// Schema size and depth, output size.
	var deep any = map[string]any{"type": "string"}
	for i := 0; i < MaxSchemaDepth+1; i++ {
		deep = map[string]any{"items": deep}
	}
	if _, err := score(ctx, deep.(map[string]any), `[]`); err == nil || !strings.Contains(err.Error(), "deeper") {
		t.Fatalf("deep schema: %v", err)
	}
	if _, err := score(ctx, map[string]any{"enum": []any{strings.Repeat("x", MaxSchemaBytes)}}, `"x"`); err == nil || !strings.Contains(err.Error(), "schema is larger") {
		t.Fatalf("large schema: %v", err)
	}
	if _, err := score(ctx, map[string]any{"type": "string"}, `"`+strings.Repeat("y", MaxSchemaValueBytes)+`"`); err == nil || !strings.Contains(err.Error(), "output is larger") {
		t.Fatalf("large output: %v", err)
	}
}
