package weftpush

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/models"
)

// fakeWeft accepts a request unless reject says otherwise for one of its
// detections (status 400 for the whole request, as Weft before per-record
// errors did) or the body exceeds limit (413).
type fakeWeft struct {
	mu       sync.Mutex
	limit    int
	reject   func(d Detection) bool
	stored   map[string]Detection
	bodies   []int
	rawBody  [][]byte
	requests int
}

func (f *fakeWeft) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	f.bodies = append(f.bodies, len(body))
	f.rawBody = append(f.rawBody, body)
	if f.limit > 0 && len(body) > f.limit {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	var p Payload
	if err := json.Unmarshal(body, &p); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	for _, d := range p.Detections {
		if f.reject != nil && f.reject(d) {
			http.Error(w, `{"reason":"detections[0]: rule tag contains control characters"}`, http.StatusBadRequest)
			return
		}
	}
	for _, d := range p.Detections {
		f.stored[d.EventID] = d
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"accepted":1}`))
}

func newFakeWeft() *fakeWeft { return &fakeWeft{stored: map[string]Detection{}} }

func blockRule() engine.RuleResult {
	return engine.RuleResult{RuleID: "r1", RuleName: "Pipe to shell", Severity: engine.SeverityCritical, Matched: true}
}

// A record Weft refuses (400) must not take the rest of its batch with it:
// the batch is split until the bad record is isolated, and only it is dropped.
func TestPusher_BisectsRejectedBatch(t *testing.T) {
	f := newFakeWeft()
	f.reject = func(d Detection) bool { return strings.HasPrefix(d.EventID, "bad") }
	srv := httptest.NewServer(f)
	defer srv.Close()
	p := testPusher(t, srv.URL, "", 100)
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("ok-%d", i)
		if i == 7 || i == 22 {
			id = fmt.Sprintf("bad-%d", i)
		}
		p.Verdict(verdict(id, models.ActionBlock, blockRule()))
	}
	p.Start(context.Background())
	p.Close(context.Background())
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.stored) != 28 {
		t.Fatalf("stored %d of 28 good records", len(f.stored))
	}
	if st := p.Stats(); st.Sent != 28 || st.Rejected != 2 || st.Failed != 0 || st.Accepted != 30 || st.Pending != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

// Oversized values: the record is bounded (cut with a marker), not escaped
// for HTML, and a request over Weft's body limit (413) is split, so every
// verdict reaches Weft.
func TestPusher_BoundsRecordsAndBatches(t *testing.T) {
	f := newFakeWeft()
	f.limit = 64 << 10
	srv := httptest.NewServer(f)
	defer srv.Close()
	p := testPusher(t, srv.URL, "", 100)
	p.MaxBatchBytes = 1 << 20 // larger than the fake's limit: 413s are split
	longTags := make([]string, 100)
	for i := range longTags {
		longTags[i] = "attack.\t" + strings.Repeat("z", 400)
	}
	longID := strings.Repeat("i", 400)
	for i := 0; i < 30; i++ {
		req, resp := verdict(fmt.Sprintf("e-%d", i), models.ActionBlock,
			engine.RuleResult{RuleID: longID, RuleName: strings.Repeat("<", 5000), Severity: engine.SeverityHigh, Tags: longTags, Matched: true})
		req.Source = strings.Repeat("<", 9000)
		p.Verdict(req, resp)
	}
	p.Start(context.Background())
	p.Close(context.Background())
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.stored) != 30 {
		t.Fatalf("stored %d of 30", len(f.stored))
	}
	d := f.stored["e-3"]
	if len(d.EventSource) > models.MaxEventSourceLength || !strings.HasSuffix(d.EventSource, models.TruncateMarker) {
		t.Errorf("event_source not bounded: %d bytes %q", len(d.EventSource), d.EventSource[len(d.EventSource)-20:])
	}
	r := d.Rules[0]
	if r.ID != models.ShortID(longID, models.MaxRuleIDLength) || len(r.ID) > 256 {
		t.Errorf("rule id = %q", r.ID)
	}
	if len(r.Title) > models.MaxRuleTitleLength || len(r.Tags) > models.MaxRuleTags {
		t.Errorf("title %d bytes, %d tags", len(r.Title), len(r.Tags))
	}
	for _, tag := range r.Tags {
		if strings.ContainsRune(tag, '\t') || len(tag) > models.MaxRuleTagLength {
			t.Fatalf("tag not cleaned: %q", tag)
		}
	}
	for _, b := range f.rawBody {
		if bytes.Contains(b, []byte("\\u003c")) {
			t.Fatal("pusher escapes HTML characters")
		}
	}
	if st := p.Stats(); st.Sent != 30 || st.Failed != 0 || st.Truncated != 30 {
		t.Fatalf("stats = %+v", st)
	}
}

// A record over MaxRecordBytes is shrunk to fit; the batch byte cap holds.
func TestPusher_RecordAndBatchByteCaps(t *testing.T) {
	f := newFakeWeft()
	srv := httptest.NewServer(f)
	defer srv.Close()
	p := testPusher(t, srv.URL, "", 100)
	p.MaxRecordBytes = 8 << 10
	p.MaxBatchBytes = 32 << 10
	var alerts []engine.RuleResult
	for i := 0; i < 150; i++ {
		alerts = append(alerts, engine.RuleResult{RuleID: fmt.Sprintf("rule-%03d", i), RuleName: strings.Repeat("t", 900),
			Severity: engine.SeverityMedium, Tags: []string{strings.Repeat("x", 200)}, Matched: true})
	}
	alerts = append(alerts, engine.RuleResult{RuleID: "crit", Severity: engine.SeverityCritical, Matched: true})
	for i := 0; i < 20; i++ {
		p.Verdict(verdict(fmt.Sprintf("e-%d", i), models.ActionBlock, alerts...))
	}
	p.Start(context.Background())
	p.Close(context.Background())
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.stored) != 20 {
		t.Fatalf("stored %d of 20", len(f.stored))
	}
	for _, n := range f.bodies {
		if n > p.MaxBatchBytes+256 {
			t.Errorf("request of %d bytes over the %d cap", n, p.MaxBatchBytes)
		}
	}
	d := f.stored["e-0"]
	if d.Severity != "critical" || len(d.Rules) == 0 || d.Rules[0].ID != "crit" {
		t.Errorf("shrunk record lost its most severe rule: severity %s, %d rules", d.Severity, len(d.Rules))
	}
}

// Weft's per-record errors (a 200 listing rejected records) are counted.
func TestPusher_CountsPerRecordRejections(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"accepted":2,"rejected":1,"errors":[{"kind":"detection","index":1,"event_id":"b","error":"bad"}]}`))
	}))
	defer srv.Close()
	p := testPusher(t, srv.URL, "", 100)
	for _, id := range []string{"a", "b", "c"} {
		p.Verdict(verdict(id, models.ActionBlock, blockRule()))
	}
	p.Start(context.Background())
	p.Close(context.Background())
	if st := p.Stats(); st.Sent != 2 || st.Rejected != 1 || st.Failed != 0 || st.Accepted != 3 {
		t.Fatalf("stats = %+v", st)
	}
}
