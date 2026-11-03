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
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/agentshield-ai/agentshield/internal/config"
)

// A burst of spans with huge attribute values must not make an export request
// larger than the collector accepts: the whole batch would be lost with it.
// The span limits cut values, and the batch size bounds the request.
func TestInit_SpanLimitsKeepExportsUnderCollectorLimit(t *testing.T) {
	const collectorLimit = 16 << 20
	var mu sync.Mutex
	spans, rejected, largest := 0, 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, collectorLimit+1))
		mu.Lock()
		defer mu.Unlock()
		largest = max(largest, len(body))
		if len(body) > collectorLimit {
			rejected++
			http.Error(w, "request body too large", http.StatusBadRequest)
			return
		}
		var req coltracepb.ExportTraceServiceRequest
		if err := proto.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for _, rs := range req.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				for _, sp := range ss.Spans {
					for _, a := range sp.Attributes {
						if v := a.GetValue().GetStringValue(); len(v) > MaxAttributeValueLength {
							t.Errorf("attribute %s is %d bytes", a.Key, len(v))
						}
					}
					spans++
				}
			}
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &config.TelemetryConfig{Enabled: true, Endpoint: srv.URL + "/v1/traces", ServiceName: "agentshield-test", SampleRate: 1, Insecure: true}
	tp, shutdown, err := Init(context.Background(), cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("<", 300<<10)
	const n = 200
	tr := tp.Tracer("agentshield")
	for i := 0; i < n; i++ {
		_, span := tr.Start(context.Background(), "evaluate")
		span.SetAttributes(attribute.String("source", big), attribute.String("event.id", "e"))
		span.End()
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if spans != n || rejected != 0 {
		t.Fatalf("collector got %d of %d spans, %d requests rejected (largest %d bytes)", spans, n, rejected, largest)
	}
}

func TestSpanLimits_HonoursShorterEnvLimit(t *testing.T) {
	t.Setenv("OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT", "100")
	if l := SpanLimits(); l.AttributeValueLengthLimit != 100 {
		t.Fatalf("AttributeValueLengthLimit = %d, want 100", l.AttributeValueLengthLimit)
	}
	t.Setenv("OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT", "-1")
	if l := SpanLimits(); l.AttributeValueLengthLimit != MaxAttributeValueLength {
		t.Fatalf("AttributeValueLengthLimit = %d, want %d", l.AttributeValueLengthLimit, MaxAttributeValueLength)
	}
}

// A collector outage (connections dropped, then 500 answers) must not lose
// the spans exported meanwhile: connection failures and 5xx answers are
// retried until MaxRetrySec, and nothing is counted as failed.
func TestInit_RetriesExportsThroughAnOutage(t *testing.T) {
	var mu sync.Mutex
	got := map[string]bool{}
	start := time.Now()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch el := time.Since(start); {
		case el < 1500*time.Millisecond:
			// Down: the connection is dropped without an answer.
			if hj, ok := w.(http.Hijacker); ok {
				if c, _, err := hj.Hijack(); err == nil {
					_ = c.Close()
					return
				}
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		case el < 3*time.Second:
			http.Error(w, "database is locked", http.StatusInternalServerError)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req coltracepb.ExportTraceServiceRequest
		if err := proto.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		for _, rs := range req.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				for _, sp := range ss.Spans {
					got[sp.Name] = true
				}
			}
		}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	cfg := &config.TelemetryConfig{Enabled: true, Endpoint: srv.URL + "/v1/traces", ServiceName: "t", SampleRate: 1, Insecure: true, MaxRetrySec: 60}
	tp, shutdown, err := Init(context.Background(), cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	tr := tp.Tracer("agentshield")
	const n = 3 * MaxExportBatchSize
	for i := 0; i < n; i++ {
		_, span := tr.Start(context.Background(), fmt.Sprintf("evaluate-%d", i))
		span.End()
		time.Sleep(20 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if st := Stats(); len(got) != n || st.Failed != 0 || st.Exported != n || st.UndeliveredAtShutdown != 0 {
		t.Fatalf("collector got %d of %d spans; stats %+v", len(got), n, st)
	}
}

// An export that fails for good (the collector refuses it) is counted and
// logged at warn level, not only reported at a level the logs do not show.
func TestInit_FailedExportIsCountedAndLoggedAtWarn(t *testing.T) {
	var buf bytes.Buffer
	var bufMu sync.Mutex
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lockedWriter{w: &buf, mu: &bufMu}, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "project_id header is required", http.StatusBadRequest)
	}))
	defer srv.Close()
	cfg := &config.TelemetryConfig{Enabled: true, Endpoint: srv.URL + "/v1/traces", ServiceName: "t", SampleRate: 1, Insecure: true, MaxRetrySec: 60}
	tp, shutdown, err := Init(context.Background(), cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	before := otelErrors.Load()
	for i := 0; i < 5; i++ {
		_, span := tp.Tracer("agentshield").Start(context.Background(), "evaluate")
		span.End()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = shutdown(ctx)
	st := Stats()
	if st.Failed != 5 || st.Exported != 0 || st.Pending != 0 || st.Errors <= before {
		t.Fatalf("stats = %+v", st)
	}
	bufMu.Lock()
	defer bufMu.Unlock()
	if out := buf.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "failed_total=5") {
		t.Fatalf("no warn log of the lost spans: %q", out)
	}
}

type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// One span the collector cannot store (a 500 for any export holding it)
// costs only that span: the batch is split until it is isolated, and it is
// dropped once the collector has taken the others.
func TestInit_IsolatesASpanTheCollectorAnswersWithServerErrors(t *testing.T) {
	var mu sync.Mutex
	got := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("poison")) {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		var req coltracepb.ExportTraceServiceRequest
		if err := proto.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		for _, rs := range req.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				for _, sp := range ss.Spans {
					got[sp.Name] = true
				}
			}
		}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	cfg := &config.TelemetryConfig{Enabled: true, Endpoint: srv.URL + "/v1/traces", ServiceName: "t", SampleRate: 1, Insecure: true, MaxRetrySec: 60}
	tp, shutdown, err := Init(context.Background(), cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	const n = MaxExportBatchSize
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("evaluate-%d", i)
		if i == 3 {
			name = "poison"
		}
		_, span := tp.Tracer("agentshield").Start(context.Background(), name)
		span.End()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = shutdown(ctx)
	mu.Lock()
	defer mu.Unlock()
	if st := Stats(); len(got) != n-1 || got["poison"] || st.Exported != n-1 || st.Failed != 1 {
		t.Fatalf("collector got %d of %d spans; stats %+v", len(got), n-1, st)
	}
}
