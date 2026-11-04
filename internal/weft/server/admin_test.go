package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// do sends a request with any method and an explicit key ("" for none).
func (e *testEnv) admDo(method, path, key string, body any) (int, []byte) {
	e.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (e *testEnv) admJSON(method, path, key string, body any) map[string]any {
	e.t.Helper()
	code, out := e.admDo(method, path, key, body)
	if code != 200 {
		e.t.Fatalf("%s %s -> %d: %s", method, path, code, out)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		e.t.Fatalf("%s %s: bad JSON %s", method, path, out)
	}
	return m
}

func jsonl(t *testing.T, b []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("bad JSONL line %s", line)
		}
		out = append(out, m)
	}
	return out
}

// startEnd is a span's start and end as /api/v1/spans/batch items.
func startEnd(project, id, trace string, parent *string, started time.Time, inputs, output any) []any {
	st := map[string]any{"project_id": project, "id": id, "trace_id": trace, "name": "op", "started_at": started.UTC().Format(time.RFC3339Nano), "inputs": inputs}
	if parent != nil {
		st["parent_id"] = *parent
	}
	return []any{
		map[string]any{"start": st},
		map[string]any{"end": map[string]any{"project_id": project, "id": id,
			"ended_at": started.Add(time.Second).UTC().Format(time.RFC3339Nano), "output": output}},
	}
}

func createKey(e *testEnv, key string, body map[string]any) (string, map[string]any) {
	e.t.Helper()
	res := e.admJSON("POST", "/api_keys/create", key, body)
	return res["secret"].(string), res["key"].(map[string]any)
}

func TestAPIKeysScopes(t *testing.T) {
	e := newEnv(t, "root")
	other := "acme/other"
	scoped, k := createKey(e, "root", map[string]any{"name": "ci", "user_id": "ci-bot", "projects": []any{pid}})
	if len(scoped) != 40 || k["prefix"] != scoped[:6] || k["all_projects"] != false {
		t.Fatalf("unexpected key %v / %q", k, scoped)
	}
	ro, _ := createKey(e, "root", map[string]any{"name": "dash", "read_only": true})
	admin, _ := createKey(e, "root", map[string]any{"name": "ops", "admin": true})

	// List never exposes secrets or hashes.
	_, raw := e.admDo("GET", "/api_keys/list", "root", nil)
	if bytes.Contains(raw, []byte(scoped)) || bytes.Contains(raw, []byte(store.HashAPIKey(scoped))) {
		t.Fatal("key list leaks a secret or hash")
	}

	// Scoped key: writes to its project and sets the user.
	e.admJSON("POST", "/api/v1/spans/batch", scoped, map[string]any{"items": startEnd(pid, "c1", "c1", nil, time.Now(), map[string]any{}, 1)})
	c := e.admJSON("GET", "/api/v1/spans/c1?project_id="+pid, scoped, nil)
	if c["user_id"] != "ci-bot" {
		t.Fatalf("user not set from key: %v", c["user_id"])
	}
	forbidden := []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/v1/spans/batch", map[string]any{"items": startEnd(other, "c2", "c2", nil, time.Now(), map[string]any{}, 1)}},
		{"POST", "/api/v1/spans/batch", map[string]any{"project_id": other, "items": []any{map[string]any{"start": map[string]any{"name": "x"}}}}},
		{"POST", "/api/v1/spans/start", map[string]any{"project_id": other, "name": "x"}},
		{"POST", "/api/v1/spans/query", map[string]any{"project_id": other}},
		{"GET", "/api/v1/spans/c1?project_id=" + other, nil},
		{"POST", "/api/v1/objects/save", map[string]any{"project_id": other, "name": "o", "value": 1}},
		{"GET", "/api/v1/objects/o?project_id=" + other, nil},
		{"POST", "/api/v1/refs/read", map[string]any{"refs": []any{"weft:///acme/other/object/o:abc"}}},
		{"GET", "/api/v1/runtimes?project_id=" + other, nil},
		{"POST", "/api/v1/feedback/batch", map[string]any{"items": []any{map[string]any{"project_id": other}}}},
		{"POST", "/api/v1/feedback/batch", map[string]any{"project_id": other, "items": []any{}}},
		{"POST", "/api/v1/registry/link", map[string]any{"ref": "weft:///" + pid + "/object/o:abc", "project_id": other, "collection": "c"}},
		{"GET", "/api/v1/evaluations/x/status?project_id=" + other, nil},
		{"POST", "/api_keys/create", map[string]any{"name": "x"}},
		{"POST", "/secrets/set", map[string]any{"name": "X", "value": "y"}},
		{"POST", "/project/delete", map[string]any{"project_id": pid}},
		{"POST", "/otel/v1/traces", map[string]any{}},
		{"POST", "/api/v1/spans/query", map[string]any{}},
	}
	for _, f := range forbidden {
		if code, out := e.admDo(f.method, f.path, scoped, f.body); code != 403 {
			t.Errorf("scoped %s %s -> %d %s, want 403", f.method, f.path, code, out)
		}
	}
	ps := e.admJSON("GET", "/projects", scoped, nil)["projects"].([]any)
	if len(ps) != 1 || ps[0].(map[string]any)["project_id"] != pid {
		t.Fatalf("scoped /projects = %v", ps)
	}
	pr := e.admJSON("GET", "/auth/principal", scoped, nil)
	if pr["admin"] != false || pr["user_id"] != "ci-bot" || len(pr["projects"].([]any)) != 1 {
		t.Fatalf("principal = %v", pr)
	}

	// Read-only key reads any project but cannot write.
	e.admJSON("POST", "/api/v1/spans/query", ro, map[string]any{"project_id": other})
	if code, out := e.admDo("POST", "/api/v1/spans/stream", ro, map[string]any{"project_id": other}); code != 200 {
		t.Fatalf("read-only stream -> %d %s", code, out)
	}
	e.admJSON("POST", "/api/v1/objects/tags", ro, map[string]any{"project_id": other})
	e.admJSON("POST", "/project/ttl_settings/read", ro, map[string]any{"project_id": pid})
	for _, w := range []struct{ method, path string }{{"POST", "/api/v1/spans/start"}, {"POST", "/api/v1/objects/o/aliases"},
		{"POST", "/api/v1/objects/o/versions/abc/tags"}, {"POST", "/api/v1/completions/stream"}, {"PUT", "/api/v1/runtimes/rt"},
		{"POST", "/api/v1/feedback/delete"}, {"POST", "/api/v1/spans/delete"}, {"POST", "/api/v1/prices/create"}} {
		if code, _ := e.admDo(w.method, w.path, ro, map[string]any{"project_id": pid, "name": "x"}); code != 403 {
			t.Fatalf("read-only %s %s -> %d", w.method, w.path, code)
		}
	}

	// Managed admin key can manage keys; static keys keep working.
	e.admJSON("POST", "/project/ttl_settings/update", admin, map[string]any{"project_id": pid, "retention_days": 30})
	e.admJSON("GET", "/api_keys/list", admin, nil)

	// Revocation takes effect immediately.
	e.admJSON("POST", "/api_keys/revoke", admin, map[string]any{"id": k["id"]})
	if code, _ := e.admDo("POST", "/api/v1/spans/query", scoped, map[string]any{"project_id": pid}); code != 401 {
		t.Fatalf("revoked key -> %d, want 401", code)
	}
	if code, _ := e.admDo("GET", "/projects", "wrong", nil); code != 401 {
		t.Fatalf("unknown key -> %d", code)
	}
	all := e.admJSON("GET", "/api_keys/list?include_revoked=true", "root", nil)["keys"].([]any)
	if len(all) != 3 {
		t.Fatalf("keys = %d", len(all))
	}
	for _, x := range all {
		m := x.(map[string]any)
		if m["id"] == k["id"] && (m["revoked_at"] == nil || m["last_used_at"] == nil) {
			t.Fatalf("revoked key should have revoked_at and last_used_at: %v", m)
		}
	}
}

func TestAPIKeysOpenServer(t *testing.T) {
	e := newEnv(t)
	if code, _ := e.admDo("POST", "/api_keys/create", "", map[string]any{"name": "first"}); code != 400 {
		t.Fatalf("first non-admin key -> %d, want 400", code)
	}
	admin, ak := createKey(e, "", map[string]any{"name": "owner", "admin": true})
	// Auth is now on.
	if code, _ := e.admDo("GET", "/projects", "", nil); code != 401 {
		t.Fatalf("auth should be required once a key exists, got %d", code)
	}
	if info := e.admJSON("GET", "/api/v1/server", "", nil); info["auth_required"] != true {
		t.Fatalf("server = %v", info)
	}
	ci, _ := createKey(e, admin, map[string]any{"name": "ci"})
	e.admJSON("GET", "/projects", ci, nil)
	if code, _ := e.admDo("POST", "/api_keys/revoke", admin, map[string]any{"id": ak["id"]}); code != 400 {
		t.Fatalf("revoking the last admin key with others active -> %d", code)
	}
	if code, _ := e.admDo("POST", "/api_keys/create", "", map[string]any{"name": "x", "admin": true, "projects": []any{pid}}); code != 401 {
		t.Fatalf("anonymous create after auth on -> %d", code)
	}
}

func TestTTLPurge(t *testing.T) {
	e, srv := newEnvWithServer(t)
	old := time.Now().AddDate(0, 0, -10)
	batch := append(startEnd(pid, "old", "old", nil, old, map[string]any{}, 1), startEnd(pid, "new", "new", nil, time.Now(), map[string]any{}, 1)...)
	e.post("/api/v1/spans/batch", map[string]any{"items": batch})
	e.feedback(map[string]any{"ref": spanRef("new"), "feedback_type": "weft.note", "payload": map[string]any{"note": "x"}})

	if r := e.post("/project/ttl_settings/read", map[string]any{"project_id": pid}); r["retention_days"] != nil {
		t.Fatalf("default ttl = %v", r)
	}
	for _, bad := range []any{0, -1} {
		if code, _ := e.raw("/project/ttl_settings/update", map[string]any{"project_id": pid, "retention_days": bad}, nil); code != 400 {
			t.Fatalf("retention_days %v -> %d", bad, code)
		}
	}
	e.post("/project/ttl_settings/update", map[string]any{"project_id": pid, "retention_days": 7})
	if r := e.post("/project/ttl_settings/read", map[string]any{"project_id": pid}); r["retention_days"] != float64(7) {
		t.Fatalf("ttl = %v", r)
	}
	res, err := srv.RunRetention(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res[pid].Calls != 1 {
		t.Fatalf("purged %v", res)
	}
	calls := e.spans(map[string]any{})
	if len(calls) != 1 || calls[0].(map[string]any)["id"] != "new" {
		t.Fatalf("calls after purge = %v", calls)
	}
	if fb := e.post("/api/v1/feedback/query", map[string]any{"project_id": pid})["items"].([]any); len(fb) != 1 {
		t.Fatalf("recent feedback should be kept: %v", fb)
	}
	e.post("/project/ttl_settings/update", map[string]any{"project_id": pid, "retention_days": nil})
	if r := e.post("/project/ttl_settings/read", map[string]any{"project_id": pid}); r["retention_days"] != nil {
		t.Fatalf("ttl after disable = %v", r)
	}
}

func TestIngestSampling(t *testing.T) {
	// Determinism: the decision depends only on the trace id.
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("trace-%d", i)
		if store.SampleTrace(id, 0.3) != store.SampleTrace(id, 0.3) {
			t.Fatal("sampling is not deterministic")
		}
	}
	e := newEnv(t)
	if r := e.post("/project/ingest_sampling_settings/read", map[string]any{"project_id": pid}); r["sample_rate"] != float64(1) || r["dry_run"] != false {
		t.Fatalf("default sampling = %v", r)
	}
	if code, _ := e.raw("/project/ingest_sampling_settings/update", map[string]any{"project_id": pid, "sample_rate": 1.5}, nil); code != 400 {
		t.Fatalf("bad rate -> %d", code)
	}
	e.post("/project/ingest_sampling_settings/update", map[string]any{"project_id": pid, "sample_rate": 0.5})
	const n = 300
	want := 0
	var batch []any
	for i := 0; i < n; i++ {
		trace := fmt.Sprintf("t%04d", i)
		root := trace
		batch = append(batch, startEnd(pid, root, trace, nil, time.Now(), map[string]any{}, 1)...)
		batch = append(batch, startEnd(pid, trace+"-child", trace, &root, time.Now(), map[string]any{}, 2)...)
		if store.SampleTrace(trace, 0.5) {
			want++
		}
	}
	e.post("/api/v1/spans/batch", map[string]any{"items": batch})
	// An end that arrives later for a dropped call stays dropped.
	var late []any
	for i := 0; i < n; i++ {
		late = append(late, map[string]any{"end": map[string]any{"project_id": pid, "id": fmt.Sprintf("t%04d", i),
			"ended_at": time.Now().UTC().Format(time.RFC3339Nano), "output": 3}})
	}
	e.post("/api/v1/spans/batch", map[string]any{"items": late})
	res := e.post("/api/v1/spans/query", map[string]any{"project_id": pid, "limit": 10000})
	calls := res["items"].([]any)
	byTrace := map[string]int{}
	for _, c := range calls {
		byTrace[c.(map[string]any)["trace_id"].(string)]++
	}
	if len(byTrace) != want || want < n/2-60 || want > n/2+60 {
		t.Fatalf("kept %d traces, want %d (about %d)", len(byTrace), want, n/2)
	}
	for tr, k := range byTrace {
		if k != 2 || !store.SampleTrace(tr, 0.5) {
			t.Fatalf("trace %s kept %d calls (root and child must share the decision)", tr, k)
		}
	}
	if res["total_count"] != float64(2*want) {
		t.Fatalf("total_count = %v", res["total_count"])
	}

	// Dry run keeps everything and marks would-be drops.
	p2 := "acme/dry"
	e.post("/project/ingest_sampling_settings/update", map[string]any{"project_id": p2, "sample_rate": 0, "dry_run": true})
	e.post("/api/v1/spans/batch", map[string]any{"items": startEnd(p2, "d1", "d1", nil, time.Now(), map[string]any{}, 1)})
	c := e.getJSON("/api/v1/spans/d1?project_id=" + p2)
	if c == nil || c["attributes"].(map[string]any)["weft_ingest_sampling"] == nil {
		t.Fatalf("dry run call = %v", c)
	}
}

func TestSensitiveDataRedaction(t *testing.T) {
	e := newEnv(t)
	if r := e.admJSON("GET", "/project/sensitive_data_settings?project_id="+pid, "", nil); r["policy"] != "off" {
		t.Fatalf("default policy = %v", r)
	}
	if code, _ := e.raw("/project/sensitive_data_settings/update", map[string]any{"project_id": pid, "policy": "pii-v1",
		"custom_patterns": []any{map[string]any{"name": "bad", "pattern": "("}}}, nil); code != 400 {
		t.Fatalf("invalid regex -> %d", code)
	}
	if code, _ := e.raw("/project/sensitive_data_settings/update", map[string]any{"project_id": pid, "policy": "pii-v1", "entities": []any{"NOPE"}}, nil); code != 400 {
		t.Fatalf("unknown entity -> %d", code)
	}
	e.post("/project/sensitive_data_settings/update", map[string]any{"project_id": pid, "policy": "pii-v1",
		"custom_patterns": []any{map[string]any{"name": "ticket", "pattern": `ACME-\d+`}}})
	text := "mail bob@example.com or call +1 415-555-0132; card 4111 1111 1111 1111; ssn 123-45-6789; ip 10.0.0.1; " +
		"iban GB82 WEST 1234 5698 7654 32; key sk-proj-abcdefghijklmnopqrstuv123; ref ACME-4242; jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"
	ref := "weft:///" + pid + "/object/x:abc"
	e.post("/api/v1/spans/batch", map[string]any{"items": startEnd(pid, "r1", "r1", nil, time.Now(),
		map[string]any{"q": text, "nested": []any{map[string]any{"email": "a@b.io"}}, "ref": ref, "count": 4111111111111111}, map[string]any{"answer": "reach me at bob@example.com"})})
	c := e.span("r1")
	q := c["inputs"].(map[string]any)["q"].(string)
	for _, secret := range []string{"bob@example.com", "415-555-0132", "4111 1111 1111 1111", "123-45-6789", "10.0.0.1", "GB82 WEST", "sk-proj-", "ACME-4242", "eyJhbGci"} {
		if strings.Contains(q, secret) {
			t.Errorf("%q not redacted: %s", secret, q)
		}
	}
	for _, ph := range []string{"<EMAIL_ADDRESS>", "<PHONE_NUMBER>", "<CREDIT_CARD>", "<US_SSN>", "<IP_ADDRESS>", "<IBAN_CODE>", "<API_KEY>", "<TICKET>"} {
		if !strings.Contains(q, ph) {
			t.Errorf("missing %s in %s", ph, q)
		}
	}
	in := c["inputs"].(map[string]any)
	if in["ref"] != ref || in["nested"].([]any)[0].(map[string]any)["email"] != "<EMAIL_ADDRESS>" {
		t.Errorf("nested/ref handling wrong: %v", in)
	}
	if out := c["output"].(map[string]any)["answer"]; out != "reach me at <EMAIL_ADDRESS>" {
		t.Errorf("output not redacted: %v", out)
	}
	// OTLP spans go through the same hook.
	body := []byte(`{"resourceSpans":[{"resource":{},"scopeSpans":[{"spans":[{"traceId":"5b8efff798038103d269b633813fc60c","spanId":"aaaaaaaaaaaaaaaa","name":"chat",
	  "startTimeUnixNano":"1700000000000000000","endTimeUnixNano":"1700000001000000000",
	  "attributes":[{"key":"input.value","value":{"stringValue":"my email is carol@example.org"}},{"key":"user.phone","value":{"stringValue":"(212) 555-0199"}}]}]}]}]}`)
	if code, out := e.raw("/otel/v1/traces", body, map[string]string{"project_id": pid}); code != 200 {
		t.Fatalf("otel -> %d %s", code, out)
	}
	sp := e.span("aaaaaaaaaaaaaaaa")
	raw, _ := json.Marshal(sp)
	if bytes.Contains(raw, []byte("carol@example.org")) || bytes.Contains(raw, []byte("555-0199")) {
		t.Fatalf("OTLP span not redacted: %s", raw)
	}
	// Entity selection and preview.
	pv := e.post("/project/sensitive_data_settings/preview", map[string]any{"policy": "pii-v1", "entities": []any{"EMAIL_ADDRESS"}, "text": "x@y.com 10.0.0.1"})
	if pv["text"] != "<EMAIL_ADDRESS> 10.0.0.1" {
		t.Fatalf("preview = %v", pv)
	}
	e.post("/project/sensitive_data_settings/update", map[string]any{"project_id": pid, "policy": "off"})
	e.post("/api/v1/spans/batch", map[string]any{"items": startEnd(pid, "r2", "r2", nil, time.Now(), map[string]any{"q": "bob@example.com"}, nil)})
	if c := e.span("r2"); c["inputs"].(map[string]any)["q"] != "bob@example.com" {
		t.Fatal("redaction should stop when the policy is off")
	}
}

func TestProjectDelete(t *testing.T) {
	e := newEnv(t)
	keep := "acme/keep"
	for _, p := range []string{pid, keep} {
		e.post("/api/v1/spans/batch", map[string]any{"items": startEnd(p, "c", "c", nil, time.Now(), map[string]any{}, 1)})
		e.post("/api/v1/objects/save", map[string]any{"project_id": p, "name": "o", "value": map[string]any{"a": 1}})
		e.post("/api/v1/tables/create", map[string]any{"project_id": p, "rows": []any{map[string]any{"x": 1}}})
		e.feedback(map[string]any{"project_id": p, "ref": "weft:///" + p + "/span/c", "feedback_type": "weft.note", "payload": map[string]any{"note": "n"}})
		e.post("/project/ttl_settings/update", map[string]any{"project_id": p, "retention_days": 3})
	}
	if code, _ := e.raw("/project/delete", map[string]any{"project_id": pid, "confirm": "acme/other"}, nil); code != 400 {
		t.Fatalf("mismatched confirm -> %d", code)
	}
	res := e.post("/project/delete", map[string]any{"project_id": pid, "confirm": pid})
	if res["deleted"].(map[string]any)["calls"] != float64(1) {
		t.Fatalf("delete = %v", res)
	}
	st := e.getJSON("/api/v1/projects/stats?project_id=" + pid)
	for _, k := range []string{"span_count", "object_count", "table_count", "feedback_count"} {
		if st[k] != float64(0) {
			t.Fatalf("%s left after delete: %v", k, st)
		}
	}
	if r := e.post("/project/ttl_settings/read", map[string]any{"project_id": pid}); r["retention_days"] != nil {
		t.Fatalf("settings should be deleted: %v", r)
	}
	st = e.getJSON("/api/v1/projects/stats?project_id=" + keep)
	if st["span_count"] != float64(1) || st["object_count"] != float64(1) || st["feedback_count"] != float64(1) {
		t.Fatalf("other project touched: %v", st)
	}
	for _, p := range e.post("/projects", nil)["projects"].([]any) {
		if p.(map[string]any)["project_id"] == pid {
			t.Fatal("deleted project still listed")
		}
	}
}

// newEnvWithServer is newEnv that also returns the Server (for RunRetention).
func newEnvWithServer(t *testing.T) (*testEnv, *Server) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "weft.db"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(nativeOnly(Config{}), st)
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		hs.Close()
		s.Close()
		_ = st.Close()
	})
	return &testEnv{t: t, srv: hs}, s
}
