package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// fakePresidio is an httptest Presidio Analyzer: it reports fixed words as
// entities, with code point offsets like the real service.
type fakePresidio struct {
	srv      *httptest.Server
	calls    atomic.Int64
	fail     atomic.Bool
	delay    atomic.Int64 // nanoseconds
	lastReq  atomic.Value // store.AnalyzeRequest
	entities map[string]string
}

func newFakePresidio(t *testing.T) *fakePresidio {
	f := &fakePresidio{entities: map[string]string{
		"Alice Smith": "PERSON", "Zoë Durand": "PERSON", "Paris": "LOCATION", "Acme Corp": "ORGANIZATION", "Buddhist": "NRP",
	}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if r.URL.Path != "/analyze" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if d := f.delay.Load(); d > 0 {
			time.Sleep(time.Duration(d))
		}
		if f.fail.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		var req store.AnalyzeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f.lastReq.Store(req)
		want := map[string]bool{}
		for _, e := range req.Entities {
			want[e] = true
		}
		out := []store.EntitySpan{}
		for word, typ := range f.entities {
			if len(want) > 0 && !want[typ] {
				continue
			}
			from := 0
			for {
				i := strings.Index(req.Text[from:], word)
				if i < 0 {
					break
				}
				b := from + i
				st := utf8.RuneCountInString(req.Text[:b])
				out = append(out, store.EntitySpan{EntityType: typ, Start: st, End: st + utf8.RuneCountInString(word), Score: 0.85})
				from = b + len(word)
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func TestSensitiveDataNERRedaction(t *testing.T) {
	fp := newFakePresidio(t)
	var st *store.Store
	e := newEnvWithStore(t, func(s *store.Store) {
		st = s
		s.SetPIIAnalyzer(store.NewPresidioAnalyzer(fp.srv.URL), 500*time.Millisecond)
	})
	r := e.admJSON("GET", "/project/sensitive_data_settings?project_id="+pid, "", nil)
	if r["ner_available"] != true || r["ner_active"] != false {
		t.Fatalf("read before enabling = %v", r)
	}
	names := map[string]bool{}
	for _, a := range r["available_entities"].([]any) {
		names[a.(map[string]any)["name"].(string)] = true
	}
	if !names["PERSON"] || !names["LOCATION"] || !names["ORGANIZATION"] || !names["EMAIL_ADDRESS"] {
		t.Fatalf("available entities = %v", names)
	}
	r = e.post("/project/sensitive_data_settings/update", map[string]any{"project_id": pid, "policy": "pii-v1"})
	if r["ner_active"] != true || len(r["ner_entities"].([]any)) != 3 {
		t.Fatalf("update = %v", r)
	}

	text := "Alice Smith (alice@example.com) met Zoë Durand in Paris at Acme Corp; both are Buddhist."
	e.post("/api/v1/spans/batch", map[string]any{"items": startEnd(pid, "n1", "n1", nil, time.Now(),
		map[string]any{"q": text, "nested": []any{"ask Zoë Durand", map[string]any{"who": "Alice Smith"}}, "n": 3},
		"Paris, said Alice Smith")})
	c := e.span("n1")
	q := c["inputs"].(map[string]any)["q"].(string)
	wantQ := "<PERSON> (<EMAIL_ADDRESS>) met <PERSON> in <LOCATION> at <ORGANIZATION>; both are Buddhist."
	if q != wantQ {
		t.Fatalf("inputs.q = %q, want %q", q, wantQ)
	}
	nested := c["inputs"].(map[string]any)["nested"].([]any)
	if nested[0] != "ask <PERSON>" || nested[1].(map[string]any)["who"] != "<PERSON>" {
		t.Fatalf("nested = %v", nested)
	}
	if c["output"] != "<LOCATION>, said <PERSON>" {
		t.Fatalf("string output = %v", c["output"])
	}
	if req, _ := fp.lastReq.Load().(store.AnalyzeRequest); req.Language != "en" || req.ScoreThreshold != 0.5 || len(req.Entities) != 3 {
		t.Fatalf("analyzer request = %+v", req)
	}

	// OTLP spans go through the same path.
	body := []byte(`{"resourceSpans":[{"resource":{},"scopeSpans":[{"spans":[{"traceId":"5b8efff798038103d269b633813fc60d","spanId":"bbbbbbbbbbbbbbbb","name":"chat",
	  "startTimeUnixNano":"1700000000000000000","endTimeUnixNano":"1700000001000000000",
	  "attributes":[{"key":"input.value","value":{"stringValue":"Book a table for Alice Smith in Paris"}}]}]}]}]}`)
	if code, out := e.raw("/otel/v1/traces", body, map[string]string{"project_id": pid}); code != 200 {
		t.Fatalf("otel -> %d %s", code, out)
	}
	raw, _ := json.Marshal(e.span("bbbbbbbbbbbbbbbb"))
	if strings.Contains(string(raw), "Alice Smith") || strings.Contains(string(raw), "Paris") {
		t.Fatalf("OTLP span not NER-redacted: %s", raw)
	}

	// Opt-in NRP and a stricter threshold via explicit entities and ner settings.
	e.post("/project/sensitive_data_settings/update", map[string]any{"project_id": pid, "policy": "pii-v1",
		"entities": []any{"PERSON", "NRP"}, "ner": map[string]any{"score_threshold": 0.8, "language": "en"}})
	e.post("/api/v1/spans/batch", map[string]any{"items": startEnd(pid, "n2", "n2", nil, time.Now(), map[string]any{"q": text}, nil)})
	c = e.span("n2")
	if q := c["inputs"].(map[string]any)["q"].(string); q != "<PERSON> (alice@example.com) met <PERSON> in Paris at Acme Corp; both are <NRP>." {
		t.Fatalf("entity selection: %q", q)
	}

	// Per-project opt-out keeps regex redaction only.
	off := false
	e.post("/project/sensitive_data_settings/update", map[string]any{"project_id": pid, "policy": "pii-v1", "ner": map[string]any{"enabled": off}})
	before := fp.calls.Load()
	e.post("/api/v1/spans/batch", map[string]any{"items": startEnd(pid, "n3", "n3", nil, time.Now(), map[string]any{"q": text}, nil)})
	c = e.span("n3")
	if q := c["inputs"].(map[string]any)["q"].(string); !strings.Contains(q, "Alice Smith") || !strings.Contains(q, "<EMAIL_ADDRESS>") {
		t.Fatalf("ner disabled: %q", q)
	}
	if fp.calls.Load() != before {
		t.Fatal("analyzer called for a project with NER disabled")
	}
	// Another project without a policy is never sent to the analyzer.
	e.post("/api/v1/spans/batch", map[string]any{"items": startEnd("acme/plain", "p1", "p1", nil, time.Now(), map[string]any{"q": text}, nil)})
	if fp.calls.Load() != before {
		t.Fatal("analyzer called for a project without a policy")
	}

	// Preview uses the analyzer too.
	pv := e.post("/project/sensitive_data_settings/preview", map[string]any{"policy": "pii-v1", "text": "Alice Smith, bob@x.io", "value": map[string]any{"city": "Paris"}})
	if pv["text"] != "<PERSON>, <EMAIL_ADDRESS>" || pv["value"].(map[string]any)["city"] != "<LOCATION>" || pv["ner_applied"] != true {
		t.Fatalf("preview = %v", pv)
	}

	// Analyzer failure: the call is stored with regex redaction, and the
	// analyzer is skipped during the back-off.
	e.post("/project/sensitive_data_settings/update", map[string]any{"project_id": pid, "policy": "pii-v1"})
	fp.fail.Store(true)
	start := time.Now()
	e.post("/api/v1/spans/batch", map[string]any{"items": startEnd(pid, "n4", "n4", nil, time.Now(), map[string]any{"q": text}, nil)})
	c = e.span("n4")
	if q := c["inputs"].(map[string]any)["q"].(string); !strings.Contains(q, "Alice Smith") || !strings.Contains(q, "<EMAIL_ADDRESS>") {
		t.Fatalf("fallback: %q", q)
	}
	failedCalls := fp.calls.Load()
	e.post("/api/v1/spans/batch", map[string]any{"items": startEnd(pid, "n5", "n5", nil, time.Now(), map[string]any{"q": text}, nil)})
	if fp.calls.Load() != failedCalls {
		t.Fatal("analyzer should be skipped during back-off")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("fallback too slow")
	}
	_ = st
}

func TestSensitiveDataNERTimeout(t *testing.T) {
	fp := newFakePresidio(t)
	fp.delay.Store(int64(2 * time.Second))
	e := newEnvWithStore(t, func(s *store.Store) {
		s.SetPIIAnalyzer(store.NewPresidioAnalyzer(fp.srv.URL+"/analyze"), 100*time.Millisecond)
	})
	e.post("/project/sensitive_data_settings/update", map[string]any{"project_id": pid, "policy": "pii-v1"})
	start := time.Now()
	e.post("/api/v1/spans/batch", map[string]any{"items": startEnd(pid, "t1", "t1", nil, time.Now(), map[string]any{"q": "Alice Smith at bob@example.com"}, nil)})
	if d := time.Since(start); d > time.Second {
		t.Fatalf("ingest waited %v for a slow analyzer", d)
	}
	c := e.span("t1")
	if q := c["inputs"].(map[string]any)["q"]; q != "Alice Smith at <EMAIL_ADDRESS>" {
		t.Fatalf("timeout fallback: %v", q)
	}
}

func TestSensitiveDataNoAnalyzer(t *testing.T) {
	e := newEnv(t)
	r := e.post("/project/sensitive_data_settings/update", map[string]any{"project_id": pid, "policy": "pii-v1", "entities": []any{"PERSON", "EMAIL_ADDRESS"}})
	if r["ner_available"] != false || r["ner_active"] != false {
		t.Fatalf("no analyzer: %v", r)
	}
	e.post("/api/v1/spans/batch", map[string]any{"items": startEnd(pid, "x1", "x1", nil, time.Now(), map[string]any{"q": "Alice Smith a@b.io"}, nil)})
	c := e.span("x1")
	if q := c["inputs"].(map[string]any)["q"]; q != "Alice Smith <EMAIL_ADDRESS>" {
		t.Fatalf("regex only: %v", q)
	}
}
