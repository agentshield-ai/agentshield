package telemetry

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

	"go.opentelemetry.io/otel/attribute"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/agentshield-ai/agentshield/internal/config"
)

// collector is a fake OTLP collector: it records the span names it stores
// and can be down (connections dropped), slow, or hung until released.
type collector struct {
	mu       sync.Mutex
	got      map[string]bool
	down     atomic.Bool
	latency  atomic.Int64 // nanoseconds added to every answer
	hang     chan struct{}
	inflight atomic.Int32
	maxConc  atomic.Int32
	requests atomic.Int64
}

func newCollector(t *testing.T) (*collector, *httptest.Server) {
	c := &collector{got: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.requests.Add(1)
		n := c.inflight.Add(1)
		defer c.inflight.Add(-1)
		for {
			m := c.maxConc.Load()
			if n <= m || c.maxConc.CompareAndSwap(m, n) {
				break
			}
		}
		if c.down.Load() {
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
					return
				}
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		c.mu.Lock()
		hang := c.hang
		c.mu.Unlock()
		if hang != nil {
			select {
			case <-hang:
			case <-r.Context().Done():
				return
			}
		}
		if d := c.latency.Load(); d > 0 {
			time.Sleep(time.Duration(d))
		}
		body, _ := io.ReadAll(r.Body)
		var req coltracepb.ExportTraceServiceRequest
		if err := proto.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		c.mu.Lock()
		for _, rs := range req.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				for _, sp := range ss.Spans {
					c.got[sp.Name] = true
				}
			}
		}
		c.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return c, srv
}

func (c *collector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.got)
}

func (c *collector) setHang(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if on && c.hang == nil {
		c.hang = make(chan struct{})
	}
	if !on && c.hang != nil {
		close(c.hang)
		c.hang = nil
	}
}

// checkBalance fails unless the counters add up exactly.
func checkBalance(t *testing.T, st *ExportStats) {
	t.Helper()
	if st == nil {
		t.Fatal("no stats")
	}
	if sum := st.Exported + st.Failed + st.Dropped + st.UndeliveredAtShutdown + st.Pending; st.Ended != sum {
		t.Fatalf("counters do not add up: ended %d != exported+failed+dropped+undelivered+pending %d (%+v)", st.Ended, sum, *st)
	}
}

func captureWarn(t *testing.T) func() string {
	var buf bytes.Buffer
	var mu sync.Mutex
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lockedWriter{w: &buf, mu: &mu}, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
}

// When the export queue is full a span is dropped, and that is counted and
// logged at warn level, never silent; the counters add up throughout and
// nothing stays pending once the collector is back.
func TestQueue_OverflowIsCountedAndWarned(t *testing.T) {
	logs := captureWarn(t)
	c, srv := newCollector(t)
	c.setHang(true)
	cfg := &config.TelemetryConfig{Enabled: true, Endpoint: srv.URL + "/v1/traces", ServiceName: "t", SampleRate: 1, Insecure: true,
		MaxRetrySec: 60, QueueSize: 50, ExportConcurrency: 2}
	tp, shutdown, err := Init(context.Background(), cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	tr := tp.Tracer("agentshield")
	const n = 500
	for i := 0; i < n; i++ {
		_, span := tr.Start(context.Background(), fmt.Sprintf("evaluate-%d", i))
		span.End()
		if i%50 == 0 {
			checkBalance(t, Stats())
			time.Sleep(10 * time.Millisecond)
		}
	}
	st := Stats()
	checkBalance(t, st)
	if st.Dropped == 0 || st.Ended != n || st.Pending > 50+2*MaxExportBatchSize {
		t.Fatalf("stats = %+v", *st)
	}
	if out := logs(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "span dropped, the export queue is full") || !strings.Contains(out, "dropped_total=") {
		t.Fatalf("no warn log of the dropped spans: %q", out)
	}
	c.setHang(false)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	st = Stats()
	checkBalance(t, st)
	if st.Pending != 0 || st.Exported+st.Dropped != n || st.Failed != 0 || st.UndeliveredAtShutdown != 0 || c.count() != int(st.Exported) {
		t.Fatalf("collector got %d; stats = %+v", c.count(), *st)
	}
}

// The queue is bounded in bytes too: large spans are dropped (and counted)
// once queue_max_mb is reached, whatever queue_size allows.
func TestQueue_ByteBudgetBoundsTheQueue(t *testing.T) {
	c, srv := newCollector(t)
	c.setHang(true)
	cfg := &config.TelemetryConfig{Enabled: true, Endpoint: srv.URL + "/v1/traces", ServiceName: "t", SampleRate: 1, Insecure: true,
		MaxRetrySec: 60, QueueMaxMB: 1, ExportConcurrency: 1}
	tp, shutdown, err := Init(context.Background(), cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", MaxAttributeValueLength)
	tr := tp.Tracer("agentshield")
	const n = 100 // about 130 KiB each: 13 MiB in all
	for i := 0; i < n; i++ {
		_, span := tr.Start(context.Background(), fmt.Sprintf("evaluate-%d", i))
		for k := 0; k < MaxSpanAttributes; k++ {
			span.SetAttributes(attribute.String(fmt.Sprintf("a%d", k), big))
		}
		span.End()
	}
	st := Stats()
	checkBalance(t, st)
	// At most 1 MiB queued plus one batch (8 MiB) in flight.
	if st.Dropped < n-(1<<20+MaxExportBatchBytes)/(130<<10) || st.Pending == 0 {
		t.Fatalf("stats = %+v", *st)
	}
	c.setHang(false)
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	st = Stats()
	checkBalance(t, st)
	if st.Pending != 0 || st.Exported+st.Dropped != n || c.count() != int(st.Exported) {
		t.Fatalf("collector got %d; stats = %+v", c.count(), *st)
	}
}

// The verifier's outage, scaled down: a 240 s Weft outage at 12 verdicts a
// second (two spans each) is 5760 spans ended while nothing can be
// exported. With the default queue none is lost: all are exported once the
// collector is back (well within max_retry_sec). The SDK's batch processor
// (2048 spans) silently dropped 3700 of them.
func TestQueue_OutageAtVerifierRateLosesNothing(t *testing.T) {
	c, srv := newCollector(t)
	c.down.Store(true)
	cfg := &config.TelemetryConfig{Enabled: true, Endpoint: srv.URL + "/v1/traces", ServiceName: "t", SampleRate: 1, Insecure: true, MaxRetrySec: 60}
	tp, shutdown, err := Init(context.Background(), cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	tr := tp.Tracer("agentshield")
	const n = 12 * 2 * 240
	start := time.Now()
	for i := 0; i < n; i++ {
		_, span := tr.Start(context.Background(), fmt.Sprintf("evaluate-%d", i))
		span.SetAttributes(attribute.String("session.id", "s"), attribute.String("event.id", fmt.Sprint(i)))
		span.End()
		if i%500 == 0 {
			checkBalance(t, Stats())
			time.Sleep(5 * time.Millisecond)
		}
	}
	time.Sleep(time.Until(start.Add(2 * time.Second)))
	st := Stats()
	checkBalance(t, st)
	if st.Dropped != 0 || st.Exported != 0 || st.Pending != n {
		t.Fatalf("during the outage: stats = %+v", *st)
	}
	c.down.Store(false)
	deadline := time.Now().Add(30 * time.Second)
	for c.count() < n && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	st = Stats()
	checkBalance(t, st)
	if c.count() != n || st.Exported != n || st.Dropped != 0 || st.Failed != 0 || st.Pending != 0 {
		t.Fatalf("after the outage the collector got %d of %d; stats = %+v", c.count(), n, *st)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// With 300 ms per export request, exports keep up with 160 spans a second
// (the verifier saw the serial 16-span exports manage ~53 a second): batches
// are larger and several are in flight at once.
func TestQueue_KeepsUpWithSlowCollector(t *testing.T) {
	c, srv := newCollector(t)
	c.latency.Store(int64(300 * time.Millisecond))
	cfg := &config.TelemetryConfig{Enabled: true, Endpoint: srv.URL + "/v1/traces", ServiceName: "t", SampleRate: 1, Insecure: true, MaxRetrySec: 60}
	tp, shutdown, err := Init(context.Background(), cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	tr := tp.Tracer("agentshield")
	const rate, secs = 160, 4
	start := time.Now()
	for i := 0; i < rate*secs; i++ {
		_, span := tr.Start(context.Background(), fmt.Sprintf("evaluate-%d", i))
		span.End()
		time.Sleep(time.Until(start.Add(time.Duration(i+1) * time.Second / rate)))
	}
	// Spans wait at most exportDelay for their batch, then one round trip.
	deadline := time.Now().Add(exportDelay + 1500*time.Millisecond)
	for c.count() < rate*secs && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	st := Stats()
	checkBalance(t, st)
	if c.count() != rate*secs || st.Exported != rate*secs {
		t.Fatalf("collector got %d of %d spans %v after the last one ended; stats %+v", c.count(), rate*secs, time.Since(start)-secs*time.Second, *st)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	// A backlog is exported by several requests at once.
	c2, srv2 := newCollector(t)
	c2.latency.Store(int64(300 * time.Millisecond))
	cfg.Endpoint = srv2.URL + "/v1/traces"
	tp, shutdown, err = Init(context.Background(), cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	tr = tp.Tracer("agentshield")
	const backlog = 4 * MaxExportBatchSize
	for i := 0; i < backlog; i++ {
		_, span := tr.Start(context.Background(), fmt.Sprintf("evaluate-%d", i))
		span.End()
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := Stats(); c2.count() != backlog || st.Exported != backlog || c2.maxConc.Load() < 2 {
		t.Fatalf("collector got %d of %d, at most %d requests at once; stats %+v", c2.count(), backlog, c2.maxConc.Load(), *st)
	}
}

// At shutdown, spans that cannot be exported by the deadline are counted as
// undelivered (and logged); the counters add up and nothing stays pending.
func TestQueue_ShutdownDeadlineCountsTheRest(t *testing.T) {
	logs := captureWarn(t)
	c, srv := newCollector(t)
	c.setHang(true)
	defer c.setHang(false)
	cfg := &config.TelemetryConfig{Enabled: true, Endpoint: srv.URL + "/v1/traces", ServiceName: "t", SampleRate: 1, Insecure: true,
		MaxRetrySec: 60, ExportConcurrency: 2}
	tp, shutdown, err := Init(context.Background(), cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	const n = 3 * MaxExportBatchSize
	for i := 0; i < n; i++ {
		_, span := tp.Tracer("agentshield").Start(context.Background(), fmt.Sprintf("evaluate-%d", i))
		span.End()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_ = shutdown(ctx)
	if el := time.Since(start); el > 4*time.Second {
		t.Fatalf("shutdown took %v", el)
	}
	st := Stats()
	checkBalance(t, st)
	if st.UndeliveredAtShutdown != n || st.Pending != 0 || st.Failed != 0 {
		t.Fatalf("stats = %+v", *st)
	}
	if out := logs(); !strings.Contains(out, "not exported at shutdown") {
		t.Fatalf("no warn log: %q", out)
	}
}
