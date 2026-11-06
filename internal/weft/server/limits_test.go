package server

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"runtime"
	"strings"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

func gz(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = zw.Write(b)
	_ = zw.Close()
	return buf.Bytes()
}

func heapInUse() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapInuse
}

func postOTLP(t *testing.T, e *testEnv, body []byte, gzipped bool) (int, string) {
	t.Helper()
	h := map[string]string{"Content-Type": "application/x-protobuf", "project_id": pid}
	if gzipped {
		h["Content-Encoding"] = "gzip"
	}
	code, out := e.raw("/otel/v1/traces", body, h)
	return code, string(out)
}

// A 15 KB gzip of 8M empty ResourceSpans grew the heap by ~1 GB; it is now
// refused (compression ratio) before decoding, and so is the same body
// sent uncompressed (element count), with the heap bounded.
func TestOTLPDecompressionAndElementLimits(t *testing.T) {
	e := newEnv(t)
	empty := bytes.Repeat([]byte{0x0a, 0x00}, 8_000_000) // 16 MB of empty ResourceSpans
	bomb := gz(t, empty)
	before := heapInUse()
	code, out := postOTLP(t, e, bomb, true)
	if code != http.StatusRequestEntityTooLarge || !strings.Contains(out, "compressed more than") {
		t.Fatalf("gzip bomb: %d %s", code, out)
	}
	code, out = postOTLP(t, e, empty, false)
	if code != http.StatusRequestEntityTooLarge || !strings.Contains(out, "elements") {
		t.Fatalf("8M elements: %d %s", code, out)
	}
	if grew := int64(heapInUse()) - int64(before); grew > 100<<20 {
		t.Fatalf("heap grew %d MB", grew>>20)
	}
	// Over the decompressed cap.
	code, out = postOTLP(t, e, gz(t, bytes.Repeat([]byte{0x0a, 0x00}, 9_000_000)), true)
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("18 MB body: %d %s", code, out)
	}
	// Logs share the limits.
	if code, out := e.raw("/otel/v1/logs", bomb, map[string]string{"Content-Type": "application/x-protobuf", "Content-Encoding": "gzip"}); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("logs gzip bomb: %d %s", code, out)
	}
	// An ordinary export still goes through.
	ok := []byte(`{"resourceSpans":[{"resource":{},"scopeSpans":[{"spans":[{"traceId":"5b8efff798038103d269b633813fc60c","spanId":"aaaaaaaaaaaaaaaa","name":"agent","startTimeUnixNano":"1700000000000000000","endTimeUnixNano":"1700000002000000000"}]}]}]}`)
	if code, out := e.raw("/otel/v1/traces", gz(t, ok), map[string]string{"Content-Encoding": "gzip", "project_id": pid}); code != 200 {
		t.Fatalf("ordinary export: %d %s", code, out)
	}
}

// One OTLP export carries at most MaxExportSpans spans.
func TestOTLPSpanCountLimit(t *testing.T) {
	e := newEnv(t)
	var spans []string
	for i := 0; i < 10001; i++ {
		spans = append(spans, fmt.Sprintf(`{"traceId":"5b8efff798038103d269b633813fc60c","spanId":"%016x","name":"s","startTimeUnixNano":"1700000000000000000"}`, i+1))
	}
	body := []byte(`{"resourceSpans":[{"resource":{},"scopeSpans":[{"spans":[` + strings.Join(spans, ",") + `]}]}]}`)
	code, out := e.raw("/otel/v1/traces", body, map[string]string{"project_id": pid})
	if code != http.StatusRequestEntityTooLarge || !strings.Contains(string(out), "spans") {
		t.Fatalf("10001 spans: %d %s", code, out)
	}
}

// A span nested 1100 deep no longer breaks the project's listings.
func TestDeepSpanKeepsListingsWorking(t *testing.T) {
	e := newEnv(t)
	deep := strings.Repeat(`{"a":`, 1100) + `"leaf"` + strings.Repeat("}", 1100)
	body := []byte(`{"project_id":"` + pid + `","items":[{"span":{"id":"d1","name":"deep","started_at":"2026-01-01T00:00:00Z","ended_at":"2026-01-01T00:00:01Z",
		"attributes":{"deep":` + deep + `,"gen_ai.conversation.id":"s1","gen_ai.agent.name":"ag"},"inputs":{"deep":` + deep + `},"output":` + deep + `}}]}`)
	if code, out := e.raw("/api/v1/spans/batch", body, nil); code != 200 {
		t.Fatalf("batch: %d %s", code, out)
	}
	for _, path := range []string{"/api/v1/sessions?project_id=" + pid, "/api/v1/agents?project_id=" + pid, "/api/v1/sessions/attention?project_id=" + pid} {
		if code, _, out := e.send(http.MethodGet, path, nil); code != 200 {
			t.Errorf("%s: %d %s", path, code, out)
		}
	}
	for _, body := range []any{
		map[string]any{"project_id": pid, "query": map[string]any{"$expr": map[string]any{"$eq": []any{map[string]any{"$getField": "attributes.gen_ai.agent.name"}, "ag"}}}},
		map[string]any{"project_id": pid, "query": map[string]any{"$expr": map[string]any{"$eq": []any{map[string]any{"$getField": "inputs.deep.a"}, "x"}}}},
	} {
		if code, out := e.raw("/api/v1/spans/query", body, nil); code != 200 {
			t.Errorf("spans/query: %d %s", code, out)
		}
	}
}

// 40k ids in a filter are a 400, not a 500 from SQLite.
func TestSpansQueryIDListCap(t *testing.T) {
	e := newEnv(t)
	ids := make([]string, 40000)
	for i := range ids {
		ids[i] = fmt.Sprint("id-", i)
	}
	code, out := e.raw("/api/v1/spans/query", map[string]any{"project_id": pid, "filter": map[string]any{"ids": ids}}, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("40000 ids: %d %s", code, out)
	}
}

// Download names are slugs, quoted by mime.FormatMediaType: a project id
// with a quote or semicolon (one created before such ids were refused)
// cannot add header parameters.
func TestContentDispositionSafe(t *testing.T) {
	bad := `acme/x";filename*=UTF-8''evil.html;`
	for _, cd := range []string{attachment(exportFilename(bad, "csv")), attachment(`a"b;c.csv`)} {
		disp, params, err := mime.ParseMediaType(cd)
		if err != nil || disp != "attachment" || len(params) != 1 || params["filename"] == "" {
			t.Fatalf("Content-Disposition %q: %v %v", cd, params, err)
		}
	}
	if name := exportFilename(bad, "csv"); strings.ContainsAny(name, `"; '*=`) || !strings.HasPrefix(name, "acme-x--filename--UTF-8--evil-html--calls-") {
		t.Fatalf("file name %q", name)
	}
	e := newEnv(t)
	b, _ := json.Marshal(map[string]any{"project_id": "acme/x y,z", "format": "csv"})
	resp, err := http.Post(e.srv.URL+"/api/v1/spans/export", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	_, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition"))
	if resp.StatusCode != 200 || err != nil || !strings.HasPrefix(params["filename"], "acme-x-y-z-calls-") {
		t.Fatalf("%d %q", resp.StatusCode, resp.Header.Get("Content-Disposition"))
	}
}

// New projects may not have ids with quotes, semicolons, angle brackets,
// control characters or "."/".." parts.
func TestNewProjectIDRefused(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{`acme/a"b`, "acme/a;b", "acme/<b>", "../p", "acme/.."} {
		code, out := e.raw("/api/v1/spans/batch", map[string]any{"project_id": p, "items": []any{map[string]any{"span": map[string]any{
			"id": "x", "name": "n", "started_at": "2026-01-01T00:00:00Z", "ended_at": "2026-01-01T00:00:01Z"}}}}, nil)
		if code != http.StatusBadRequest {
			t.Errorf("%q: %d %s", p, code, out)
		}
	}
	_ = store.MaxFilterIDs
}
