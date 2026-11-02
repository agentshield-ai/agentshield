package store

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "weft.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func jsonVal(t *testing.T, s string) any {
	t.Helper()
	v, err := refs.Decode([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func ptr(s string) *string { return &s }

func usage(model string, p, c int) map[string]any {
	return map[string]any{"usage": map[string]any{model: map[string]any{"requests": json.Number("1"),
		"prompt_tokens": json.Number(itoa(p)), "completion_tokens": json.Number(itoa(c)), "total_tokens": json.Number(itoa(p + c))}}}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

const proj = "ent/proj"

func start(id, parent, op string) CallStart {
	st := CallStart{ProjectID: proj, ID: id, TraceID: "t1", OpName: op, StartedAt: "2026-01-01T00:00:00Z", Inputs: map[string]any{"q": id}}
	if parent != "" {
		st.ParentID = ptr(parent)
	}
	return st
}

func end(id string, summary map[string]any, exc *string) CallEnd {
	return CallEnd{ProjectID: proj, ID: id, EndedAt: "2026-01-01T00:00:01.5Z", Output: "out-" + id, Summary: summary, Exception: exc}
}

func TestRollupInOrder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.StartCall(ctx, start("root", "", "agent")))
	must(s.StartCall(ctx, start("child", "root", "llm")))
	must(s.StartCall(ctx, start("grand", "child", "llm")))
	must(s.EndCall(ctx, end("grand", usage("gpt-4o", 10, 5), ptr("boom"))))
	must(s.EndCall(ctx, end("child", usage("gpt-4o", 100, 50), nil)))
	must(s.EndCall(ctx, end("root", map[string]any{}, nil)))

	root, err := s.GetCall(ctx, proj, "root", true, false, false)
	must(err)
	u := root.Summary["usage"].(map[string]any)["gpt-4o"].(map[string]any)
	if u["total_tokens"] != int64(165) || u["requests"] != int64(2) {
		t.Fatalf("root usage = %v", u)
	}
	sc := root.Summary["status_counts"].(map[string]any)
	if sc["success"] != int64(1) || sc["error"] != int64(1) {
		t.Fatalf("root status_counts = %v", sc)
	}
	if st := root.Summary["weave"].(map[string]any)["status"]; st != "descendant_error" {
		t.Fatalf("root status = %v", st)
	}
	if lat := root.Summary["weave"].(map[string]any)["latency_ms"]; lat != int64(1500) {
		t.Fatalf("latency = %v", lat)
	}
	costs := root.Summary["weave"].(map[string]any)["costs"].(map[string]any)["gpt-4o"].(map[string]any)
	if costs["prompt_tokens_total_cost"].(float64) <= 0 {
		t.Fatalf("expected cost, got %v", costs)
	}
}

func TestRollupOutOfOrder(t *testing.T) {
	// OTLP order: leaves first, root last.
	s := newTestStore(t)
	ctx := context.Background()
	mk := func(id, parent string, p int) CompleteCall {
		en := end(id, usage("claude-sonnet-4", p, p), nil)
		return CompleteCall{Start: start(id, parent, "span-"+id), End: &en}
	}
	if _, err := s.UpsertComplete(ctx, []CompleteCall{mk("c", "b", 1)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertComplete(ctx, []CompleteCall{mk("d", "b", 2)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertComplete(ctx, []CompleteCall{mk("a", "", 4)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertComplete(ctx, []CompleteCall{mk("b", "a", 8)}); err != nil {
		t.Fatal(err)
	}
	// re-sending a span must not double count
	if _, err := s.UpsertComplete(ctx, []CompleteCall{mk("c", "b", 1)}); err != nil {
		t.Fatal(err)
	}
	a, err := s.GetCall(ctx, proj, "a", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	u := a.Summary["usage"].(map[string]any)["claude-sonnet-4"].(map[string]any)
	if u["prompt_tokens"] != int64(15) {
		t.Fatalf("a prompt tokens = %v, want 15", u["prompt_tokens"])
	}
	if sc := a.Summary["status_counts"].(map[string]any); sc["success"] != int64(3) {
		t.Fatalf("a status counts = %v", sc)
	}
}

func TestEndBeforeStart(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.StartCall(ctx, start("p", "", "parent")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertBatch(ctx, []BatchItem{
		{Mode: "end", End: ptrEnd(end("k", usage("gpt-4o-mini", 3, 3), nil))},
		{Mode: "start", Start: ptrStart(start("k", "p", "kid"))},
	}); err != nil {
		t.Fatal(err)
	}
	p, err := s.GetCall(ctx, proj, "p", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.Summary["usage"] == nil {
		t.Fatal("parent should have rolled-up usage")
	}
	k, err := s.GetCall(ctx, proj, "k", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if k.Output != "out-k" || k.OpName != "kid" {
		t.Fatalf("merged call wrong: %+v", k)
	}
}

func ptrEnd(e CallEnd) *CallEnd       { return &e }
func ptrStart(s CallStart) *CallStart { return &s }

func TestQueryExpressions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for i, id := range []string{"x1", "x2", "x3"} {
		st := start(id, "", "op")
		st.Inputs = map[string]any{"n": json.Number(itoa(i)), "text": "Hello " + id}
		if err := s.StartCall(ctx, st); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EndCall(ctx, end("x2", nil, ptr("bad"))); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		q    string
		want int
	}{
		{`{"$expr":{"$gt":[{"$getField":"inputs.n"},{"$literal":0}]}}`, 2},
		{`{"$expr":{"$eq":[{"$getField":"summary.weave.status"},{"$literal":"error"}]}}`, 1},
		{`{"$expr":{"$contains":{"input":{"$getField":"inputs.text"},"substr":{"$literal":"hello x3"},"case_insensitive":true}}}`, 1},
		{`{"$expr":{"$not":[{"$eq":[{"$getField":"id"},{"$literal":"x1"}]}]}}`, 2},
		{`{"$expr":{"$in":[{"$getField":"id"},[{"$literal":"x1"},{"$literal":"x3"}]]}}`, 2},
		{`{"$expr":{"$and":[{"$gte":[{"$getField":"started_at"},{"$literal":"2025-12-31T00:00:00Z"}]},{"$eq":[{"$getField":"ended_at"},{"$literal":null}]}]}}`, 2},
	}
	for _, c := range cases {
		calls, err := s.ListCalls(ctx, CallsQuery{ProjectID: proj, Query: jsonVal(t, c.q)})
		if err != nil {
			t.Fatalf("%s: %v", c.q, err)
		}
		if len(calls) != c.want {
			t.Errorf("%s: got %d calls, want %d", c.q, len(calls), c.want)
		}
	}
	// sort by input value descending
	calls, err := s.ListCalls(ctx, CallsQuery{ProjectID: proj, SortBy: []SortBy{{Field: "inputs.n", Direction: "desc"}}})
	if err != nil || len(calls) != 3 || calls[0].ID != "x3" {
		t.Fatalf("sort failed: %v %v", err, calls)
	}
	// invalid operator rejected
	if _, err := s.ListCalls(ctx, CallsQuery{ProjectID: proj, Query: jsonVal(t, `{"$expr":{"$regex":[1,2]}}`)}); err == nil {
		t.Fatal("expected error for unknown operator")
	}
	// search
	n, _, err := s.CountCalls(ctx, proj, nil, nil, "hello x2")
	if err != nil || n != 1 {
		t.Fatalf("search count = %d, %v", n, err)
	}
}

func TestFeedbackFieldQuery(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, id := range []string{"f1", "f2"} {
		if err := s.StartCall(ctx, start(id, "", "op")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CreateFeedback(ctx, FeedbackCreate{ProjectID: proj, WeaveRef: refs.CallRef(proj, "f1"), FeedbackType: "wandb.reaction.1",
		Payload: map[string]any{"emoji": "👍"}}); err != nil {
		t.Fatal(err)
	}
	calls, err := s.ListCalls(ctx, CallsQuery{ProjectID: proj, IncludeFeedback: true,
		Query: jsonVal(t, `{"$expr":{"$eq":[{"$getField":"feedback.wandb.reaction.1.emoji"},{"$literal":"👍"}]}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].ID != "f1" || len(calls[0].Feedback) != 1 {
		t.Fatalf("feedback query = %+v", calls)
	}
}

func TestObjectsAndRefs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	tdig, rows, err := s.CreateTable(ctx, proj, []any{map[string]any{"q": "a"}, map[string]any{"q": "b"}})
	if err != nil {
		t.Fatal(err)
	}
	ds := map[string]any{"_type": "Dataset", "_class_name": "Dataset", "_bases": []any{"Object", "BaseModel"}, "name": "ds",
		"rows": refs.TableRef(proj, tdig)}
	d1, v1, err := s.CreateObject(ctx, proj, "ds", ds, "", nil)
	if err != nil || v1 != 0 {
		t.Fatal(err, v1)
	}
	// same content -> same version
	d1b, v1b, _ := s.CreateObject(ctx, proj, "ds", ds, "", nil)
	if d1b != d1 || v1b != 0 {
		t.Fatal("identical publish should dedupe")
	}
	ds["description"] = "v2"
	d2, v2, _ := s.CreateObject(ctx, proj, "ds", ds, "", nil)
	if v2 != 1 || d2 == d1 {
		t.Fatal("expected version 1")
	}
	latest, err := s.ReadObject(ctx, proj, "ds", "latest")
	if err != nil || latest.Digest != d2 || !latest.IsLatest || *latest.BaseObjectClass != "Dataset" {
		t.Fatalf("latest wrong: %+v %v", latest, err)
	}
	if err := s.SetAlias(ctx, proj, "ds", "v0", "prod"); err != nil {
		t.Fatal(err)
	}
	prod, err := s.ReadObject(ctx, proj, "ds", "prod")
	if err != nil || prod.Digest != d1 {
		t.Fatalf("alias read: %v %v", prod, err)
	}
	row, err := s.ReadRef(ctx, refs.ObjectRef(proj, "ds", d1, false)+"/attr/rows/id/"+rows[1])
	if err != nil {
		t.Fatal(err)
	}
	if row.(map[string]any)["q"] != "b" {
		t.Fatalf("row = %v", row)
	}
	all, err := s.ReadRef(ctx, refs.ObjectRef(proj, "ds", d1, false)+"/attr/rows")
	if err != nil || len(all.([]any)) != 2 {
		t.Fatalf("rows = %v %v", all, err)
	}
	objs, err := s.QueryObjects(ctx, ObjsQuery{ProjectID: proj, Filter: &ObjsFilter{LatestOnly: true, BaseObjectClasses: []string{"Dataset"}}})
	if err != nil || len(objs) != 1 || *objs[0].VersionCount != 2 {
		t.Fatalf("objs = %v %v", objs, err)
	}
	nd, upd, err := s.UpdateTable(ctx, proj, tdig, []TableUpdate{{Append: &struct{ Row json.RawMessage }{json.RawMessage(`{"q":"c"}`)}}})
	if err != nil || len(upd) != 1 {
		t.Fatal(err)
	}
	n, _ := s.TableCount(ctx, proj, nd)
	if n != 3 {
		t.Fatalf("count = %d", n)
	}
}

func TestDeleteCascades(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_ = s.StartCall(ctx, start("r", "", "op"))
	_ = s.StartCall(ctx, start("c1", "r", "op"))
	_ = s.StartCall(ctx, start("c2", "c1", "op"))
	if err := s.DeleteCalls(ctx, proj, []string{"r"}); err != nil {
		t.Fatal(err)
	}
	n, _, _ := s.CountCalls(ctx, proj, nil, nil, "")
	if n != 0 {
		t.Fatalf("expected all deleted, %d left", n)
	}
}

func TestThreadsMixedTurnIDs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	th := "conv"
	r := start("turn1", "", "agent")
	r.ThreadID, r.TurnID = &th, ptr("turn1")
	child := start("kid", "turn1", "llm")
	child.ThreadID = &th // no turn_id, parent in same thread
	other := start("turn2", "", "agent")
	other.ThreadID = &th // no turn_id, no parent
	for _, st := range []CallStart{r, child, other} {
		if err := s.StartCall(ctx, st); err != nil {
			t.Fatal(err)
		}
	}
	ts, err := s.QueryThreads(ctx, ThreadsQuery{ProjectID: proj})
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 1 || ts[0].TurnCount != 2 || ts[0].CallCount != 3 {
		t.Fatalf("threads = %+v", ts[0])
	}
}

func TestDefaultPriceLookup(t *testing.T) {
	cases := map[string][2]float64{
		"claude-opus-5-5":            {4, 20},
		"claude-opus-5":              {5, 25},
		"claude-sonnet-5-5":          {2, 10},
		"claude-haiku-5-5":           {0.1, 0.5},
		"claude-fable-5-1":           {10, 50},
		"anthropic/claude-opus-4-8":  {5, 25},
		"claude-sonnet-4-5-20250929": {3, 15},
		"gpt-4o-mini-2024-07-18":     {0.15, 0.6},
	}
	for model, want := range cases {
		p, ok := LookupDefaultPrice(model)
		if !ok {
			t.Errorf("%s: no price", model)
			continue
		}
		if got := [2]float64{p.Prompt / perM, p.Completion / perM}; math.Abs(got[0]-want[0]) > 1e-9 || math.Abs(got[1]-want[1]) > 1e-9 {
			t.Errorf("%s (matched %s): got %v per M, want %v", model, p.LLMID, got, want)
		}
	}
}

func TestSecretsEncryptedAtRest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "weft.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.SetSecret(ctx, "OPENAI_API_KEY", "sk-test-123"); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := s.r.QueryRowContext(ctx, `SELECT value FROM secrets WHERE name = 'OPENAI_API_KEY'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw == "sk-test-123" || !strings.HasPrefix(raw, encPrefix) {
		t.Fatalf("secret stored in plaintext: %q", raw)
	}
	// a legacy plaintext row is re-encrypted on the next open
	if _, err := s.w.ExecContext(ctx, `INSERT INTO secrets(name, value, updated_at) VALUES('LEGACY', 'plain', 0)`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	for name, want := range map[string]string{"OPENAI_API_KEY": "sk-test-123", "LEGACY": "plain"} {
		got, ok, err := s2.GetSecret(ctx, name)
		if err != nil || !ok || got != want {
			t.Fatalf("%s = %q %v %v", name, got, ok, err)
		}
	}
	if err := s2.r.QueryRowContext(ctx, `SELECT value FROM secrets WHERE name = 'LEGACY'`).Scan(&raw); err != nil || !strings.HasPrefix(raw, encPrefix) {
		t.Fatalf("legacy secret not re-encrypted: %q", raw)
	}
	// a different key cannot read the secrets
	other, _ := LoadSecretKey("another-passphrase", "")
	if err := s2.SetSecretKey(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s2.GetSecret(ctx, "OPENAI_API_KEY"); err == nil {
		t.Fatal("decryption with the wrong key should fail")
	}
	if st, err := os.Stat(SecretKeyFile(path)); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("key file perms: %v %v", st, err)
	}
}
