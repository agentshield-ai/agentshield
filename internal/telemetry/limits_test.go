package telemetry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

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
