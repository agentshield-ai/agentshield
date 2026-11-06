package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

func newBudgetEnv(t *testing.T, cfg Config) (*testEnv, *Server) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "weft.db"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(cfg, st)
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		hs.Close()
		s.Close()
		_ = st.Close()
	})
	return &testEnv{t: t, srv: hs}, s
}

// fillSpans stores n spans of about size bytes each in project p.
func fillSpans(e *testEnv, p string, n, size int) {
	e.t.Helper()
	pad := strings.Repeat("x", size)
	var items []any
	for i := 0; i < n; i++ {
		items = append(items, map[string]any{"span": map[string]any{"id": fmt.Sprintf("big%04d", i), "name": "op",
			"started_at": "2026-01-01T00:00:00Z", "ended_at": "2026-01-01T00:00:01Z", "inputs": map[string]any{"q": pad}}})
	}
	e.post("/api/v1/spans/batch", map[string]any{"project_id": p, "items": items})
}

// A /spans/query response over the cap is refused before its spans are
// loaded: the refusal costs no memory to speak of.
func TestSpansQueryRefusedBeforeLoading(t *testing.T) {
	defer func(n int64) { maxListResponseBytes = n }(maxListResponseBytes)
	maxListResponseBytes = 2 << 20
	e, _ := newBudgetEnv(t, Config{})
	fillSpans(e, "big/proj", 16, 256<<10) // 4 MiB stored
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	code, out := e.raw("/api/v1/spans/query", map[string]any{"project_id": "big/proj", "limit": 16}, nil)
	runtime.ReadMemStats(&after)
	if code != 400 || !strings.Contains(string(out), "larger than 2 MiB") {
		t.Fatalf("over the cap: %d %s", code, out)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 1<<20 {
		t.Fatalf("refusing the query allocated %d KiB", alloc>>10)
	}
	// Under the cap it is answered.
	if got := e.post("/api/v1/spans/query", map[string]any{"project_id": "big/proj", "limit": 4}); len(got["items"].([]any)) != 4 {
		t.Fatalf("under the cap: %v", got["total_count"])
	}
}

// Read responses are built within a server-wide budget, which every
// request returns when it ends.
func TestResponseBudget(t *testing.T) {
	e, s := newBudgetEnv(t, Config{ResponseBudget: 1 << 20})
	fillSpans(e, "big/proj", 4, 200<<10) // 800 KiB stored: twice that in memory
	code, out := e.raw("/api/v1/spans/query", map[string]any{"project_id": "big/proj", "limit": 4}, nil)
	if code != 400 || !strings.Contains(string(out), "response budget") {
		t.Fatalf("over the budget: %d %s", code, out)
	}
	if got := e.post("/api/v1/spans/query", map[string]any{"project_id": "big/proj", "limit": 1}); len(got["items"].([]any)) != 1 {
		t.Fatalf("within the budget: %v", got)
	}
	// Streams release each page: a stream larger than the budget runs.
	if _, lines := e.stream("/api/v1/spans/stream", map[string]any{"project_id": "big/proj"}); len(lines) != 4 {
		t.Fatalf("stream: %d lines", len(lines))
	}
	if used, _ := s.respBudget.InUse(); used != 0 {
		t.Fatalf("%d bytes of the response budget still held", used)
	}
}

// Table streams read a page at a time, so a table larger than the budget
// streams; a table query over the budget is refused.
func TestTableStreamWithinBudget(t *testing.T) {
	e, s := newBudgetEnv(t, Config{ResponseBudget: 1 << 20})
	pad := strings.Repeat("y", 1000)
	var rows []any
	for i := 0; i < 2500; i++ { // ~2.5 MB of rows
		rows = append(rows, map[string]any{"i": i, "pad": pad})
	}
	tbl := e.post("/api/v1/tables/create", map[string]any{"project_id": pid, "rows": rows})
	if _, lines := e.stream("/api/v1/tables/stream", map[string]any{"project_id": pid, "digest": tbl["digest"]}); len(lines) != 2500 {
		t.Fatalf("stream: %d rows", len(lines))
	}
	code, out := e.raw("/api/v1/tables/query", map[string]any{"project_id": pid, "digest": tbl["digest"], "limit": 2500}, nil)
	if code != 400 || !strings.Contains(string(out), "response budget") {
		t.Fatalf("table query over the budget: %d %s", code, out)
	}
	if used, _ := s.respBudget.InUse(); used != 0 {
		t.Fatalf("%d bytes of the response budget still held", used)
	}
}

// Lists are written one item at a time, byte for byte as encoding/json
// writes them.
func TestListStreamJSONMatchesEncoder(t *testing.T) {
	type item struct {
		A string         `json:"a"`
		B map[string]any `json:"b"`
	}
	for _, p := range []page[item]{
		newList([]item{{A: "<x> &  ", B: map[string]any{"z": 1, "a": []any{"q"}}}, {A: "two"}}, 7),
		newList([]item(nil), 0),
		{Items: nil, TotalCount: 3},
	} {
		var want bytes.Buffer
		enc := json.NewEncoder(&want)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(p)
		var got bytes.Buffer
		if err := p.streamJSON(&got); err != nil {
			t.Fatal(err)
		}
		if got.String() != want.String() {
			t.Fatalf("streamed %q, want %q", got.String(), want.String())
		}
	}
}
