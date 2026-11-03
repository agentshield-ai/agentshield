package server

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func exportRaw(t *testing.T, e *testEnv, body map[string]any) (*http.Response, []byte) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(e.srv.URL+"/calls/export", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp, buf.Bytes()
}

func readCSV(t *testing.T, b []byte) (map[string]int, [][]string) {
	t.Helper()
	recs, err := csv.NewReader(bytes.NewReader(b)).ReadAll()
	if err != nil {
		t.Fatalf("bad csv: %v\n%s", err, b)
	}
	idx := map[string]int{}
	for i, h := range recs[0] {
		idx[h] = i
	}
	return idx, recs[1:]
}

func TestCallsExportCSVFlatten(t *testing.T) {
	e := newEnv(t)
	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	seedMetricCalls(t, e, base)
	// A call with nested inputs, a list, a key containing a dot and a formula-looking string.
	e.post("/call/start", map[string]any{"start": map[string]any{
		"project_id": pid, "id": "nested", "op_name": "agent", "started_at": base.Add(2 * time.Hour).Format(time.RFC3339Nano),
		"inputs":     map[string]any{"q": map[string]any{"text": "hi, \"there\"\nbye", "lang": "en"}, "tags": []any{"a", "b"}},
		"attributes": map[string]any{"gen_ai.system": "openai"},
	}})
	e.post("/call/end", map[string]any{"end": map[string]any{"project_id": pid, "id": "nested",
		"ended_at": base.Add(2*time.Hour + time.Second).Format(time.RFC3339Nano), "output": "=HYPERLINK(\"x\")"}})

	resp, body := exportRaw(t, e, map[string]any{"project_id": pid, "format": "csv", "sort_by": []any{map[string]any{"field": "started_at", "direction": "asc"}}})
	if resp.StatusCode != 200 {
		t.Fatalf("export: %d %s", resp.StatusCode, body)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/csv") || !strings.Contains(resp.Header.Get("Content-Disposition"), ".csv") {
		t.Fatalf("headers: %v", resp.Header)
	}
	idx, rows := readCSV(t, body)
	if len(rows) != 5 {
		t.Fatalf("want 5 rows, got %d", len(rows))
	}
	for _, col := range []string{"id", "status", "latency_ms", "total_tokens", "cost_usd", "inputs.n", "inputs.q.text", "inputs.q.lang", "inputs.tags", "output", "output.len", "attributes.env", "attributes.gen_ai.system"} {
		if _, ok := idx[col]; !ok {
			t.Errorf("missing column %q in %v", col, rows)
		}
	}
	if idx["id"] != 0 || idx["inputs.n"] < idx["run_id"] {
		t.Errorf("base columns should come first: %v", idx)
	}
	first, last := rows[0], rows[4]
	if first[idx["id"]] != "m0" || first[idx["latency_ms"]] != "100" || first[idx["total_tokens"]] != "15" || first[idx["output.len"]] != "10" || first[idx["attributes.env"]] != "prod" {
		t.Errorf("first row = %v", first)
	}
	if rows[1][idx["status"]] != "error" || rows[1][idx["exception"]] != "boom" {
		t.Errorf("error row = %v", rows[1])
	}
	if last[idx["inputs.q.text"]] != "hi, \"there\"\nbye" || last[idx["inputs.tags"]] != `["a","b"]` || last[idx["attributes.gen_ai.system"]] != "openai" {
		t.Errorf("nested row = %v", last)
	}
	if last[idx["output"]] != `'=HYPERLINK("x")` {
		t.Errorf("formula not neutralised: %q", last[idx["output"]])
	}

	// Explicit columns, filtered.
	_, body = exportRaw(t, e, map[string]any{"project_id": pid, "format": "csv", "filter": map[string]any{"op_names": []any{"answer"}},
		"columns": []any{"id", "inputs.n", "attributes.gen_ai.system", "summary.usage", "feedback.x"}})
	idx, rows = readCSV(t, body)
	if len(idx) != 5 || len(rows) != 3 {
		t.Fatalf("explicit columns: %v %v", idx, rows)
	}
	if !strings.Contains(rows[0][idx["summary.usage"]], "prompt_tokens") {
		t.Errorf("structured column should be JSON: %v", rows[0])
	}

	// Unflattened CSV keeps inputs/output as JSON.
	_, body = exportRaw(t, e, map[string]any{"project_id": pid, "format": "csv", "flatten": false})
	idx, rows = readCSV(t, body)
	if _, ok := idx["inputs.n"]; ok || len(rows) != 5 {
		t.Fatalf("unflattened header = %v", idx)
	}
	if !strings.HasPrefix(rows[0][idx["inputs"]], "{") {
		t.Errorf("inputs should be JSON: %v", rows[0])
	}
}

func TestCallsExportStreamingFormats(t *testing.T) {
	e := newEnv(t)
	// More than one page so the pager runs several times.
	n := exportPageSize*2 + 37
	batch := make([]any, 0, 2*n)
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("c%05d", i)
		st := base.Add(time.Duration(i) * time.Second)
		batch = append(batch,
			map[string]any{"mode": "start", "req": map[string]any{"start": map[string]any{"project_id": pid, "id": id, "trace_id": id, "op_name": "f",
				"started_at": st.Format(time.RFC3339Nano), "inputs": map[string]any{"i": i}}}},
			map[string]any{"mode": "end", "req": map[string]any{"end": map[string]any{"project_id": pid, "id": id,
				"ended_at": st.Add(time.Millisecond).Format(time.RFC3339Nano), "output": i * 2}}})
	}
	e.post("/call/upsert_batch", map[string]any{"batch": batch})

	resp, body := exportRaw(t, e, map[string]any{"project_id": pid, "format": "jsonl"})
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/jsonl" {
		t.Fatalf("jsonl: %d %v", resp.StatusCode, resp.Header)
	}
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	seen := map[string]bool{}
	for sc.Scan() {
		var c map[string]any
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			t.Fatalf("bad line %q", sc.Text())
		}
		id := c["id"].(string)
		if seen[id] {
			t.Fatalf("duplicate %s", id)
		}
		seen[id] = true
		if c["summary"] == nil || c["inputs"] == nil {
			t.Fatalf("full call expected: %v", c)
		}
	}
	if len(seen) != n {
		t.Fatalf("jsonl rows = %d, want %d", len(seen), n)
	}

	// JSON array with limit/offset and flattening.
	_, body = exportRaw(t, e, map[string]any{"project_id": pid, "format": "json", "flatten": true, "limit": 700, "offset": 5,
		"sort_by": []any{map[string]any{"field": "started_at", "direction": "asc"}}})
	var arr []map[string]any
	if err := json.Unmarshal(body, &arr); err != nil {
		t.Fatalf("json: %v %s", err, body[:min(len(body), 200)])
	}
	if len(arr) != 700 || arr[0]["id"] != "c00005" || arr[0]["inputs.i"].(float64) != 5 || arr[0]["output"].(float64) != 10 {
		t.Fatalf("json export: len=%d first=%v", len(arr), arr[0])
	}

	// Empty result is a valid empty array; bad requests fail before streaming.
	_, body = exportRaw(t, e, map[string]any{"project_id": pid, "format": "json", "filter": map[string]any{"op_names": []any{"none"}}})
	if strings.TrimSpace(string(body)) != "[\n]" && strings.TrimSpace(string(body)) != "[]" {
		var x []any
		if err := json.Unmarshal(body, &x); err != nil || len(x) != 0 {
			t.Fatalf("empty json = %q", body)
		}
	}
	if resp, body := exportRaw(t, e, map[string]any{"project_id": pid, "format": "xml"}); resp.StatusCode != 400 {
		t.Fatalf("bad format: %d %s", resp.StatusCode, body)
	}
	if resp, body := exportRaw(t, e, map[string]any{"project_id": pid, "query": map[string]any{"$expr": map[string]any{"$eq": []any{map[string]any{"$getField": "bogus"}, 1}}}}); resp.StatusCode != 400 {
		t.Fatalf("bad query: %d %s", resp.StatusCode, body)
	}
}
