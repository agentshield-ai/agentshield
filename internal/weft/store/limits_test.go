package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// nestedEq is {"$eq": [{"$eq": [... {"$getField": "op_name"} ..., "x"]}, 1]}
// nested n deep on the left (the reviewer's q<N>.json).
func nestedEq(n int) any {
	var cur any = map[string]any{"$getField": "op_name"}
	for i := 0; i < n; i++ {
		var rhs any = jsonNum(1)
		if i == 0 {
			rhs = "x"
		}
		cur = map[string]any{"$eq": []any{cur, rhs}}
	}
	return map[string]any{"$expr": cur}
}

func jsonNum(i int) any { return json.Number(fmt.Sprint(i)) }

// Each operand is compiled once: a nested comparison compiles in linear
// time and SQL (it took 2^depth before).
func TestExprNestedCompareLinear(t *testing.T) {
	for _, n := range []int{10, 40, 62} {
		start := time.Now()
		sql, args, err := compileQuery(nestedEq(n), callField)
		if err != nil {
			t.Fatalf("depth %d: %v", n, err)
		}
		if d := time.Since(start); d > 50*time.Millisecond {
			t.Fatalf("depth %d compiled in %v", n, d)
		}
		if len(sql) > 40*n+100 || len(args) != n {
			t.Fatalf("depth %d: SQL %d bytes, %d args; want linear", n, len(sql), len(args))
		}
	}
	// Both sides nested: the query itself doubles per level, so the
	// operation budget refuses it, fast.
	var both any = map[string]any{"$getField": "op_name"}
	for i := 0; i < 14; i++ {
		both = map[string]any{"$eq": []any{both, both}}
	}
	start := time.Now()
	_, _, err := compileQuery(map[string]any{"$expr": both}, callField)
	var inv *InvalidError
	if !errors.As(err, &inv) || !strings.Contains(inv.Msg, "too large") {
		t.Fatalf("both-sides tree: err = %v", err)
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("both-sides tree refused after %v", d)
	}
	// Beyond the depth limit.
	if _, _, err := compileQuery(nestedEq(70), callField); err == nil {
		t.Fatal("70-deep query compiled")
	}
}

// $convert to bool writes its input once, and counts towards the depth.
func TestExprConvertBoolLinear(t *testing.T) {
	conv := func(n int) any {
		var cur any = map[string]any{"$getField": "op_name"}
		for i := 0; i < n; i++ {
			cur = map[string]any{"$convert": map[string]any{"input": cur, "to": "bool"}}
		}
		return map[string]any{"$expr": map[string]any{"$eq": []any{cur, jsonNum(1)}}}
	}
	sql, _, err := compileQuery(conv(62), callField)
	if err != nil {
		t.Fatal(err)
	}
	if len(sql) > 62*80+100 {
		t.Fatalf("62 nested $convert: %d bytes of SQL", len(sql))
	}
	if _, _, err := compileQuery(conv(64), callField); err == nil || !strings.Contains(err.Error(), "nested too deeply") {
		t.Fatalf("64 nested $convert under $eq: err = %v", err)
	}
	// Semantics: NULL and false-like values are 0, others 1.
	s := newTestStore(t)
	ctx := context.Background()
	for _, v := range []string{"", "false", "yes"} {
		var got int
		q, args, err := compileQuery(map[string]any{"$expr": map[string]any{"$eq": []any{
			map[string]any{"$convert": map[string]any{"input": map[string]any{"$literal": v}, "to": "bool"}}, jsonNum(1)}}}, callField)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.r.QueryRowContext(ctx, "SELECT "+q, args...).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if want := map[string]int{"": 0, "false": 0, "yes": 1}[v]; got != want {
			t.Fatalf("bool(%q) = %d, want %d", v, got, want)
		}
	}
}

func TestExprBudgets(t *testing.T) {
	ops := make([]any, 0, 1200)
	for i := 0; i < 1200; i++ {
		ops = append(ops, map[string]any{"$eq": []any{map[string]any{"$getField": "op_name"}, fmt.Sprint(i)}})
	}
	if _, _, err := compileQuery(map[string]any{"$expr": map[string]any{"$or": ops}}, callField); err == nil {
		t.Fatal("1200 comparisons compiled")
	}
	items := make([]any, 9000)
	for i := range items {
		items[i] = fmt.Sprint(i)
	}
	if _, _, err := compileQuery(map[string]any{"$expr": map[string]any{"$in": []any{map[string]any{"$getField": "id"}, items}}}, callField); err != nil {
		t.Fatalf("$in of 9000: %v", err)
	}
	items = append(items, items...)
	if _, _, err := compileQuery(map[string]any{"$expr": map[string]any{"$in": []any{map[string]any{"$getField": "id"}, items}}}, callField); err == nil {
		t.Fatal("$in of 18000 compiled")
	}
}

// A Monitor whose query cannot run is refused when it is saved.
func TestMonitorQueryValidatedOnSave(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	mon := func(q any) map[string]any {
		return map[string]any{"_class_name": "Monitor", "_bases": []any{"Object"}, "name": "m", "active": true, "query": q}
	}
	if _, _, err := s.CreateObject(ctx, proj, "bad", mon(nestedEq(70)), "", nil); err == nil {
		t.Fatal("monitor with a 70-deep query saved")
	}
	if _, _, err := s.CreateObject(ctx, proj, "good", mon(nestedEq(3)), "", nil); err != nil {
		t.Fatal(err)
	}
}

// A spans filter id list over MaxFilterIDs is a 400; long lists below it
// are bound as one JSON array, not one variable each.
func TestFilterIDListCaps(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.StartCall(ctx, start("a", "", "op")); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 40000)
	for i := range ids {
		ids[i] = fmt.Sprint("id-", i)
	}
	_, err := s.ListCalls(ctx, CallsQuery{ProjectID: proj, Filter: &CallsFilter{CallIDs: ids}})
	var inv *InvalidError
	if !errors.As(err, &inv) {
		t.Fatalf("40000 ids: err = %v, want InvalidError", err)
	}
	ids = append(ids[:9999], "a")
	calls, err := s.ListCalls(ctx, CallsQuery{ProjectID: proj, Filter: &CallsFilter{CallIDs: ids, TraceIDs: ids}})
	if err != nil || len(calls) != 0 { // "a" is not a trace id
		t.Fatalf("10000 ids: %d calls, %v", len(calls), err)
	}
	calls, err = s.ListCalls(ctx, CallsQuery{ProjectID: proj, Filter: &CallsFilter{CallIDs: ids}})
	if err != nil || len(calls) != 1 {
		t.Fatalf("10000 ids: %d calls, %v", len(calls), err)
	}
}

// A span nested deeper than SQLite's JSON functions read is stored with
// the deep part as text, so filters over the project keep working.
func TestDeepJSONCappedOnIngest(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var deep any = "leaf"
	for i := 0; i < 1100; i++ {
		deep = map[string]any{"a": deep}
	}
	st := start("deep", "", "op")
	st.Attributes = map[string]any{"deep": deep, "k": "v"}
	st.Inputs = map[string]any{"deep": deep}
	if err := s.StartCall(ctx, st); err != nil {
		t.Fatal(err)
	}
	en := end("deep", map[string]any{}, nil)
	en.Output = deep
	if err := s.EndCall(ctx, en); err != nil {
		t.Fatal(err)
	}
	if err := s.StartCall(ctx, start("plain", "", "op")); err != nil {
		t.Fatal(err)
	}
	var valid int
	if err := s.r.QueryRowContext(ctx, `SELECT json_valid(attributes) AND json_valid(inputs) AND json_valid(output) FROM calls WHERE id = 'deep'`).Scan(&valid); err != nil || valid != 1 {
		t.Fatalf("stored JSON valid = %d, %v", valid, err)
	}
	for _, q := range []string{
		`{"$expr": {"$eq": [{"$getField": "attributes.k"}, "v"]}}`,
		`{"$expr": {"$eq": [{"$getField": "inputs.deep.a.a"}, "x"]}}`,
		`{"$expr": {"$eq": [{"$getField": "output"}, "x"]}}`,
	} {
		if _, err := s.ListCalls(ctx, CallsQuery{ProjectID: proj, Query: jsonVal(t, q)}); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, _, err := s.QueryAgents(ctx, AgentsQueryReq{ProjectID: proj}); err != nil {
		t.Fatalf("agents: %v", err)
	}
	// A row stored before the cap: filters skip it instead of failing.
	raw := strings.Repeat(`{"a":`, 1100) + "1" + strings.Repeat("}", 1100)
	if _, err := s.w.ExecContext(ctx, `UPDATE calls SET attributes = ? WHERE id = 'plain'`, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListCalls(ctx, CallsQuery{ProjectID: proj, Query: jsonVal(t, `{"$expr": {"$eq": [{"$getField": "attributes.k"}, "v"]}}`)}); err != nil {
		t.Fatalf("old deep row: %v", err)
	}
	if _, _, err := s.QueryAgents(ctx, AgentsQueryReq{ProjectID: proj}); err != nil {
		t.Fatalf("agents with an old deep row: %v", err)
	}
}

// A bucket prefix names a folder: teams/a does not cover teams/ab/...
func TestReferenceBucketPrefixBoundary(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	pa := "teams/a"
	if _, err := s.CreateReferenceBucket(ctx, &ReferenceBucket{ProjectID: &pa, Provider: "s3", Bucket: "shared-bucket", Prefix: "teams/a"}, true); err != nil {
		t.Fatal(err)
	}
	if b, err := s.ReferenceBucketFor(ctx, pa, BucketS3, "shared-bucket", "teams/a/x.png"); err != nil || b.Prefix != "teams/a/" {
		t.Fatalf("teams/a/x.png: %v, %v", b, err)
	}
	if _, err := s.ReferenceBucketFor(ctx, pa, BucketS3, "shared-bucket", "teams/ab/x.png"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("teams/ab/x.png: err = %v, want not found", err)
	}
	// A row registered before prefixes were normalised reads the same way.
	if _, err := s.w.ExecContext(ctx, `INSERT INTO reference_buckets(id, entity, project_id, provider, bucket, prefix, created_at) VALUES('old','teams','teams/c','s3','shared-bucket','teams/c',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReferenceBucketFor(ctx, "teams/c", BucketS3, "shared-bucket", "teams/cd/x.png"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old row, teams/cd/x.png: err = %v", err)
	}
	if _, err := s.ReferenceBucketFor(ctx, "teams/c", BucketS3, "shared-bucket", "teams/c/x.png"); err != nil {
		t.Fatalf("old row, teams/c/x.png: %v", err)
	}
	for _, c := range []struct {
		prefix, key string
		want        bool
	}{{"", "x", true}, {"a", "a", true}, {"a/", "a/b", true}, {"a", "ab", false}, {"a/", "ab/c", false}} {
		if got := BucketPrefixCovers(c.prefix, c.key); got != c.want {
			t.Errorf("BucketPrefixCovers(%q, %q) = %v", c.prefix, c.key, got)
		}
	}
}

// New projects may not have ids that break out of headers, file names or
// object-store keys; existing ones keep working.
func TestNewProjectIDValidation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, pid := range []string{`a/b"c`, "a/b;c", "a/<b>", "./p", "e/..", "a/b\x7f"} {
		st := start("x", "", "op")
		st.ProjectID = pid
		err := s.StartCall(ctx, st)
		var inv *InvalidError
		if !errors.As(err, &inv) {
			t.Errorf("new project %q: err = %v", pid, err)
		}
	}
	// One that exists already (created before the rule) keeps working.
	if _, err := s.w.ExecContext(ctx, `INSERT INTO projects(project_id, created_at, last_activity) VALUES('old/p;q', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	st := start("x", "", "op")
	st.ProjectID = "old/p;q"
	if err := s.StartCall(ctx, st); err != nil {
		t.Fatalf("existing project: %v", err)
	}
	if _, err := s.GetCall(ctx, "old/p;q", "x", false, false, false); err != nil {
		t.Fatalf("existing project read: %v", err)
	}
}

// Large batches commit in chunks; a re-sent span is reported as ended once.
func TestUpsertChunksAndFirstEnd(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	defer func(n int) { upsertChunk = n }(upsertChunk)
	upsertChunk = 7
	var calls []CompleteCall
	for i := 0; i < 25; i++ {
		id := fmt.Sprint("c", i)
		en := end(id, map[string]any{}, nil)
		calls = append(calls, CompleteCall{Start: start(id, "", "op"), End: &en})
	}
	ended, err := s.UpsertComplete(ctx, calls)
	if err != nil || len(ended) != 25 {
		t.Fatalf("ended %d, %v", len(ended), err)
	}
	n, _, err := s.CountCalls(ctx, proj, nil, nil, "")
	if err != nil || n != 25 {
		t.Fatalf("stored %d, %v", n, err)
	}
	again := make([]CompleteCall, len(calls))
	for i, c := range calls {
		en := end(c.Start.ID, map[string]any{}, nil)
		again[i] = CompleteCall{Start: start(c.Start.ID, "", "op"), End: &en}
	}
	if ended, err = s.UpsertComplete(ctx, again); err != nil || len(ended) != 0 {
		t.Fatalf("re-sent: %d ended, %v", len(ended), err)
	}
}

// An event-log rebuild that builds a span exactly as stored skips it: it
// is neither written nor reported as ended again.
func TestLogRebuildSkipsUnchangedSpans(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	builder := func(g LogGroup) []CompleteCall {
		sid := g.SessionID
		turn := "turn-" + g.GroupID
		at := time.UnixMicro(g.Events[0].TimeUS).UTC().Format(time.RFC3339Nano)
		endAt := time.UnixMicro(g.Events[len(g.Events)-1].TimeUS).UTC().Format(time.RFC3339Nano)
		out := []CompleteCall{{Start: CallStart{ID: turn, TraceID: turn, OpName: "turn", ThreadID: &sid, StartedAt: at},
			End: &CallEnd{EndedAt: endAt, Output: len(g.Events)}}}
		for _, e := range g.Events {
			p := turn
			out = append(out, CompleteCall{Start: CallStart{ID: "ev-" + e.Key, TraceID: turn, ParentID: &p, OpName: e.Name, ThreadID: &sid, StartedAt: at},
				End: &CallEnd{EndedAt: at}})
		}
		return out
	}
	src := LogSource{Name: "test", SpanSource: "test-logs", Build: builder}
	t0 := time.Now().Add(-time.Hour).UnixMicro()
	ev := func(i int) LogEvent {
		return LogEvent{Key: fmt.Sprint("k", i), SessionID: "S", GroupID: "G", Name: "e", TimeUS: t0 + int64(i)*1e6, Attrs: map[string]any{}}
	}
	ended, _, err := s.IngestLogEvents(ctx, "acme/logs", src, []LogEvent{ev(0), ev(1)})
	if err != nil || len(ended) != 3 {
		t.Fatalf("first batch: %d ended, %v", len(ended), err)
	}
	// One more event: the turn changes (its end and output), the two event
	// spans do not; only the new span and the turn are written.
	ctx2, stats := WithIngestStats(ctx)
	ended, _, err = s.IngestLogEvents(ctx2, "acme/logs", src, []LogEvent{ev(2)})
	if err != nil || len(ended) != 1 || ended[0].ID != "ev-k2" {
		t.Fatalf("second batch: ended %v, %v", ended, err)
	}
	if stats.SpansUnchanged != 2 || stats.SpansStored != 2 {
		t.Fatalf("second batch: %d unchanged, %d stored", stats.SpansUnchanged, stats.SpansStored)
	}
	// Re-sent events: nothing rebuilt.
	if ended, _, err = s.IngestLogEvents(ctx, "acme/logs", src, []LogEvent{ev(1)}); err != nil || len(ended) != 0 {
		t.Fatalf("re-sent: %v, %v", ended, err)
	}
	var out sql.NullString
	if err := s.r.QueryRowContext(ctx, `SELECT output FROM calls WHERE project_id = 'acme/logs' AND id = 'turn-G'`).Scan(&out); err != nil || out.String != "3" {
		t.Fatalf("turn output %q, %v", out.String, err)
	}
}

// A group stops growing at maxLogGroupEvents events.
func TestLogGroupEventCap(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	defer func(n int) { maxLogGroupEvents = n }(maxLogGroupEvents)
	maxLogGroupEvents = 5
	t0 := time.Now().Add(-time.Hour).UnixMicro()
	var evs []LogEvent
	for i := 0; i < 8; i++ {
		evs = append(evs, LogEvent{Key: fmt.Sprint("k", i), SessionID: "S", GroupID: "G", Name: "e", TimeUS: t0 + int64(i), Attrs: map[string]any{}})
	}
	ctx2, stats := WithIngestStats(ctx)
	if _, added, err := s.IngestLogEvents(ctx2, "acme/logs", groupingSource, evs); err != nil || added != 5 {
		t.Fatalf("added %d, %v", added, err)
	}
	if stats.EventsDropped != 3 {
		t.Fatalf("dropped %d", stats.EventsDropped)
	}
}

// Event logs are purged by receive time too, so events a sender dates in
// the future do not stay forever.
func TestLogEventsPurgedByReceiveTime(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	future := time.Now().Add(100 * 365 * 24 * time.Hour).UnixMicro()
	if _, _, err := s.IngestLogEvents(ctx, "acme/logs", groupingSource, []LogEvent{{Key: "f", SessionID: "S", GroupID: "G", Name: "e", TimeUS: future, Attrs: map[string]any{}}}); err != nil {
		t.Fatal(err)
	}
	var n int64
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var err error
		n, err = purgeLogEventsBefore(ctx, tx, "acme/logs", time.Now().Add(time.Minute).UnixMicro())
		return err
	})
	if err != nil || n != 1 {
		t.Fatalf("purged %d, %v", n, err)
	}
}
