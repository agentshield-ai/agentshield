package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// D7: one session row whose attributes SQLite cannot parse (nested deeper
// than its JSON limit) does not fail later ranked-source ingest of the
// session.
func TestSourcesSurviveMalformedRow(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	deep := `{"a":` + strings.Repeat("[", 3000) + strings.Repeat("]", 3000) + `}`
	if _, err := s.DB().Exec(`INSERT INTO calls(project_id, id, op_name, trace_id, thread_id, started_at, attributes, has_start)
		VALUES('acme/cc', 'deep', 'op', 'deep', 'S', ?, ?, 1)`, time.Now().UnixMicro(), deep); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute).UTC()
	for _, src := range []string{SourceClaudeCodeLogs, SourceClaudeCodeTraces, SourceClaudeCodeLogs, SourceClaudeCodePlugin} {
		if _, err := s.UpsertComplete(ctx, ccSpans(src, "S", src, "toolu_1", at)); err != nil {
			t.Fatalf("%s ingest after a malformed row: %v", src, err)
		}
	}
}

// D8: the source Go reads is the one SQL reads (attributes.weft.source);
// a flat or OTel weft.source attribute is not a source.
func TestSpanSourceField(t *testing.T) {
	for _, tc := range []struct {
		attrs map[string]any
		want  string
	}{
		{map[string]any{"weft": map[string]any{"source": SourceClaudeCodePlugin}}, SourceClaudeCodePlugin},
		{map[string]any{"weft.source": SourceClaudeCodePlugin}, ""},
		{map[string]any{"otel": map[string]any{"attributes": map[string]any{"weft.source": SourceClaudeCodePlugin}}}, ""},
		{map[string]any{"otel": map[string]any{"attributes": map[string]any{"gen_ai.agent.name": "gemini-cli", "gen_ai.operation.name": "llm_call"}}}, SourceGeminiTraces},
	} {
		if got := spanSource(tc.attrs); got != tc.want {
			t.Errorf("spanSource(%v) = %q, want %q", tc.attrs, got, tc.want)
		}
	}
}

// A6/D8: a ranked source claim outside WithSourceClaims is neutralised;
// outranked spans are stored hidden rather than dropped, and the
// replacement hook runs.
func TestSourceClaimsAndHiddenNotDropped(t *testing.T) {
	s := newTestStore(t)
	at := time.Now().Add(-time.Minute).UTC()
	var reps []SourceReplaced
	ctx := WithSourceReplacedHook(context.Background(), func(r SourceReplaced) { reps = append(reps, r) })
	if _, err := s.UpsertComplete(ctx, ccSpans(SourceClaudeCodeLogs, "S", "logs", "toolu_1", at)); err != nil {
		t.Fatal(err)
	}
	// A plugin claim on a path that may only claim trace sources: no effect.
	traceCtx := WithSourceClaims(ctx, RankedSources("-otel-traces")...)
	if _, err := s.UpsertComplete(traceCtx, ccSpans(SourceClaudeCodePlugin, "S", "decoy", "toolu_1", at)); err != nil {
		t.Fatal(err)
	}
	if got := liveIDs(t, s, "S"); !got["logs-turn"] || !got["decoy-turn"] {
		t.Fatalf("unclaimed plugin source hid spans: %v", got)
	}
	c, err := s.GetCall(ctx, "acme/cc", "decoy-turn", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if w, _ := c.Attributes["weft"].(map[string]any); w["source"] != nil || w["claimed_source"] != SourceClaudeCodePlugin {
		t.Fatalf("decoy weft attributes = %v", c.Attributes["weft"])
	}
	// A permitted claim takes over; lower spans are hidden, later ones stored hidden.
	plugCtx := WithSourceClaims(ctx, SourceClaudeCodePlugin)
	if _, err := s.UpsertComplete(plugCtx, ccSpans(SourceClaudeCodePlugin, "S", "plugin", "toolu_1", at)); err != nil {
		t.Fatal(err)
	}
	if got := liveIDs(t, s, "S"); got["logs-turn"] || !got["plugin-turn"] {
		t.Fatalf("after the plugin: %v", got)
	}
	if len(reps) != 1 || reps[0].To != SourceClaudeCodePlugin || reps[0].From[SourceClaudeCodeLogs] != 3 {
		t.Fatalf("replacements = %+v", reps)
	}
	ctx2, stats := WithIngestStats(ctx)
	if _, err := s.UpsertComplete(ctx2, ccSpans(SourceClaudeCodeLogs, "S", "later", "toolu_2", at)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.r.QueryRow(`SELECT COUNT(*) FROM calls WHERE project_id = 'acme/cc' AND id LIKE 'later-%' AND deleted_at IS NOT NULL
		AND json_extract(attributes, '$.weft.hidden_by_source') IS NOT NULL`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("later lower-ranked spans stored hidden: %d %v", n, err)
	}
	if stats.SpansDropped != 3 {
		t.Fatalf("hidden at ingest counted %d", stats.SpansDropped)
	}
	// A sender cannot hide its own spans' marker into others, nor pre-set it.
	sp := ccSpans(SourceClaudeCodePlugin, "S2", "self", "toolu_3", at)
	sp[0].Start.Attributes["weft"].(map[string]any)[hiddenByKey] = "x"
	if _, err := s.UpsertComplete(plugCtx, sp); err != nil {
		t.Fatal(err)
	}
	if got := liveIDs(t, s, "S2"); !got["self-turn"] {
		t.Fatalf("sender-set hidden marker hid a span: %v", got)
	}
}
