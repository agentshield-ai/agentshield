package weftpush

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/models"
)

// captureWarnings routes the pusher's log to a buffer.
func captureWarnings(p *Pusher) func() string {
	var mu sync.Mutex
	var buf bytes.Buffer
	p.log = slog.New(slog.NewTextHandler(writerFunc(func(b []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(b)
	}), &slog.HandlerOptions{Level: slog.LevelWarn}))
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(b []byte) (int, error) { return f(b) }

// checkBalance asserts the accounting identity: every accepted record is
// sent, dropped, failed, rejected, undelivered at shutdown or pending.
func checkBalance(t *testing.T, st Stats) {
	t.Helper()
	if st.Accepted != st.Sent+st.Dropped+st.Failed+st.Rejected+st.UndeliveredAtShutdown+st.Pending {
		t.Fatalf("stats do not balance: %+v", st)
	}
}

// Records still queued (or in flight) when the shutdown flush deadline
// passes are counted as undelivered and logged at warn level, never lost
// uncounted.
func TestPusher_ShutdownCountsUndeliveredRecords(t *testing.T) {
	f := newFakeWeft()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			f.ServeHTTP(w, r) // the first batch goes through
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done() // then Weft hangs
	}))
	defer srv.Close()
	p := testPusher(t, srv.URL, "", 1000)
	logs := captureWarnings(p)
	for i := 0; i < 185; i++ {
		p.Verdict(verdict(fmt.Sprintf("e-%d", i), models.ActionBlock, blockRule()))
	}
	p.Start(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	p.Close(ctx)
	st := p.Stats()
	checkBalance(t, st)
	if st.Accepted != 185 || st.Sent != 100 || st.UndeliveredAtShutdown != 85 || st.Pending != 0 || st.Failed != 0 {
		t.Fatalf("stats = %+v", st)
	}
	if out := logs(); !strings.Contains(out, "not delivered at shutdown") || !strings.Contains(out, "records=85") {
		t.Fatalf("no warn log of the undelivered records: %q", out)
	}
}

// A Weft outage shorter than MaxRetry loses nothing: the batch in flight is
// kept and retried with backoff (through dropped connections, then 503s),
// records offered meanwhile queue up, and everything is delivered once Weft
// is back.
func TestPusher_RidesOutAnOutage(t *testing.T) {
	f := newFakeWeft()
	start := time.Now()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch el := time.Since(start); {
		case el < 800*time.Millisecond:
			hj, _ := w.(http.Hijacker)
			c, _, _ := hj.Hijack()
			_ = c.Close()
		case el < 1500*time.Millisecond:
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			f.ServeHTTP(w, r)
		}
	}))
	defer srv.Close()
	p := testPusher(t, srv.URL, "", 1000)
	p.Backoff = func(int) time.Duration { return 50 * time.Millisecond }
	p.MaxRetry = time.Minute
	p.Start(context.Background())
	for i := 0; i < 140; i++ {
		p.Verdict(verdict(fmt.Sprintf("o-%d", i), models.ActionBlock, blockRule()))
		time.Sleep(10 * time.Millisecond)
	}
	if st := p.Stats(); st.FailingSince == "" && time.Since(start) < 1500*time.Millisecond {
		t.Errorf("stats do not show the outage: %+v", st)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p.Close(ctx)
	st := p.Stats()
	checkBalance(t, st)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.stored) != 140 || st.Sent != 140 || st.Failed != 0 || st.FailingSince != "" {
		t.Fatalf("stored %d of 140; stats %+v", len(f.stored), st)
	}
}

// Past MaxRetry the pusher gives up on a batch: counted as failed and
// logged at warn level. A full queue drops new records (drop-newest),
// counted and logged.
func TestPusher_GivesUpAfterMaxRetryAndCountsQueueDrops(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	p := testPusher(t, srv.URL, "", 5)
	p.Backoff = func(int) time.Duration { return 20 * time.Millisecond }
	p.MaxRetry = 300 * time.Millisecond
	logs := captureWarnings(p)
	p.Start(context.Background())
	p.Verdict(verdict("first", models.ActionBlock, blockRule()))
	time.Sleep(50 * time.Millisecond) // the worker holds "first" and retries
	for i := 0; i < 20; i++ {
		p.Verdict(verdict(fmt.Sprintf("q-%d", i), models.ActionBlock, blockRule()))
	}
	waitFor(t, func() bool { st := p.Stats(); return st.Pending == 0 })
	p.Close(context.Background())
	st := p.Stats()
	checkBalance(t, st)
	if st.Accepted != 21 || st.Dropped != 15 || st.Failed != 6 || st.Sent != 0 {
		t.Fatalf("stats = %+v", st)
	}
	out := logs()
	for _, want := range []string{"giving up on records", "the queue is full"} {
		if !strings.Contains(out, want) {
			t.Errorf("no warn log %q: %q", want, out)
		}
	}
}

// One record Weft cannot store (a 500 for any request holding it) costs
// only that record: after a few 500s the batch is split until the record is
// isolated, and it is dropped once Weft has stored the rest.
func TestPusher_IsolatesARecordWeftAnswersWithServerErrors(t *testing.T) {
	f := newFakeWeft()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("poison")) {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		f.ServeHTTP(w, r)
	}))
	defer srv.Close()
	p := testPusher(t, srv.URL, "", 100)
	logs := captureWarnings(p)
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("ok-%d", i)
		if i == 17 {
			id = "poison-17"
		}
		p.Verdict(verdict(id, models.ActionBlock, blockRule()))
	}
	p.Start(context.Background())
	p.Close(context.Background())
	st := p.Stats()
	checkBalance(t, st)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.stored) != 29 || st.Sent != 29 || st.Failed != 1 {
		t.Fatalf("stored %d of 29; stats %+v", len(f.stored), st)
	}
	if out := logs(); !strings.Contains(out, "poison-17") {
		t.Errorf("dropped record not logged: %q", out)
	}
}

// A Weft that answers every request with 500 for a while is an outage, not a
// poison record: nothing is dropped, and everything arrives once it
// recovers.
func TestPusher_ServerErrorsOnEveryRequestAreAnOutage(t *testing.T) {
	f := newFakeWeft()
	var down atomic.Bool
	down.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "database is locked", http.StatusInternalServerError)
			return
		}
		f.ServeHTTP(w, r)
	}))
	defer srv.Close()
	p := testPusher(t, srv.URL, "", 100)
	p.Backoff = func(int) time.Duration { return 20 * time.Millisecond }
	p.MaxRetry = time.Minute
	for i := 0; i < 20; i++ {
		p.Verdict(verdict(fmt.Sprintf("s-%d", i), models.ActionBlock, blockRule()))
	}
	p.Start(context.Background())
	time.Sleep(time.Second)
	down.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p.Close(ctx)
	st := p.Stats()
	checkBalance(t, st)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.stored) != 20 || st.Sent != 20 || st.Failed != 0 {
		t.Fatalf("stored %d of 20; stats %+v", len(f.stored), st)
	}
}

// The largest detection the engine can produce (models.MaxRules rules with
// the longest ids, titles and tags, all of them characters JSON escapes)
// fits the record limit, so the push never drops tags or rules the evaluate
// span keeps.
func TestPusher_WorstCaseDetectionFitsRecordLimit(t *testing.T) {
	p, err := New(config.WeftConfig{Endpoint: "http://127.0.0.1:1", Project: "team/app"})
	if err != nil {
		t.Fatal(err)
	}
	// U+2028 is 3 bytes, escaped to 6; the x ends keep cleaning from
	// trimming it as space.
	esc := func(n int) string { return "x" + strings.Repeat("\u2028", (n-2)/3) + "x" }
	d := &Detection{EventID: strings.Repeat("\"", models.MaxIDLength), SessionID: strings.Repeat("\"", models.MaxIDLength),
		ToolCallID: strings.Repeat("\"", models.MaxIDLength), ToolName: esc(maxNameBytes), AgentName: esc(maxNameBytes),
		EventSource: esc(models.MaxEventSourceLength), Action: "block", Mode: "enforce", Severity: "critical", Source: "engine",
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano)}
	for i := 0; i < models.MaxRules; i++ {
		tags := make([]string, 0, models.MaxRuleTags)
		for k := 0; k < models.MaxRuleTags; k++ {
			tags = append(tags, esc(models.MaxRuleTagBytes/8))
		}
		d.Rules = append(d.Rules, Rule{ID: fmt.Sprintf("%03d", i) + strings.Repeat("\"", models.MaxRuleIDLength-3),
			Title: esc(models.MaxRuleTitleLength), Level: "critical", Tags: tags})
	}
	r, err := p.encode(item{det: d})
	if err != nil {
		t.Fatal(err)
	}
	if p.truncated.Load() != 0 || len(r.raw) > p.MaxRecordBytes {
		t.Fatalf("worst-case detection is %d bytes (limit %d), truncated %d", len(r.raw), p.MaxRecordBytes, p.truncated.Load())
	}
	t.Logf("worst-case detection: %d bytes", len(r.raw))
}
