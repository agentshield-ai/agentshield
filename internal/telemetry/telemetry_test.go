package telemetry

import (
	"context"
	"testing"

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

	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}
}
