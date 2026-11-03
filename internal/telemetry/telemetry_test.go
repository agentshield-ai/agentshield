package telemetry

import (
	"context"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/config"
)

func TestInit_Disabled_ReturnsNoop(t *testing.T) {
	cfg := &config.TelemetryConfig{Enabled: false}
	tp, shutdown, err := Init(context.Background(), cfg, "1.0.0")
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if tp == nil {
		t.Fatal("expected non-nil TracerProvider even when disabled")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}
}

func TestInit_Enabled_ReturnsProvider(t *testing.T) {
	cfg := &config.TelemetryConfig{
		Enabled:     true,
		Endpoint:    "http://localhost:4318",
		ServiceName: "agentshield-test",
		SampleRate:  1.0,
		Insecure:    true,
	}
	tp, shutdown, err := Init(context.Background(), cfg, "test")
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if tp == nil {
		t.Fatal("expected non-nil TracerProvider")
	}
	// Create a tracer and span to verify it works
	tracer := tp.Tracer("test")
	_, span := tracer.Start(context.Background(), "test-span")
	span.End()

	// Nothing listens on the endpoint: the export retries (an outage) until
	// the shutdown deadline, and the span is counted as undelivered.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_ = shutdown(ctx)
	if st := Stats(); st == nil || st.Ended != 1 || st.UndeliveredAtShutdown+st.Exported != 1 {
		t.Fatalf("stats = %+v", st)
	}
}
