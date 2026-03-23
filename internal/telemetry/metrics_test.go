package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestNewMetricsRecorder_CreatesCounters(t *testing.T) {
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	meter := mp.Meter("agentshield-test")

	rec, err := NewMetricsRecorder(meter)
	if err != nil {
		t.Fatalf("NewMetricsRecorder failed: %v", err)
	}
	if rec == nil {
		t.Fatal("expected non-nil MetricsRecorder")
	}
}

func TestMetricsRecorder_RecordEvaluation_CacheMiss(t *testing.T) {
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	meter := mp.Meter("agentshield-test")

	rec, err := NewMetricsRecorder(meter)
	if err != nil {
		t.Fatalf("NewMetricsRecorder failed: %v", err)
	}

	rec.RecordEvaluation(context.Background(), "bash", "block", 2, false)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect failed: %v", err)
	}

	if len(rm.ScopeMetrics) == 0 {
		t.Fatal("expected scope metrics")
	}

	found := map[string]bool{
		"agentshield.evaluations.total": false,
		"agentshield.alerts.total":      false,
		"agentshield.cache.misses":      false,
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if _, ok := found[m.Name]; ok {
				found[m.Name] = true
			}
		}
	}

	for name, present := range found {
		if !present {
			t.Errorf("expected metric %q to be recorded", name)
		}
	}
}

func TestMetricsRecorder_RecordEvaluation_CacheHit(t *testing.T) {
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	meter := mp.Meter("agentshield-test")

	rec, err := NewMetricsRecorder(meter)
	if err != nil {
		t.Fatalf("NewMetricsRecorder failed: %v", err)
	}

	rec.RecordEvaluation(context.Background(), "curl", "allow", 0, true)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect failed: %v", err)
	}

	if len(rm.ScopeMetrics) == 0 {
		t.Fatal("expected scope metrics")
	}

	foundHits := false
	foundMisses := false
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == "agentshield.cache.hits" {
				foundHits = true
			}
			if m.Name == "agentshield.cache.misses" {
				foundMisses = true
			}
		}
	}

	if !foundHits {
		t.Error("expected agentshield.cache.hits metric for cached evaluation")
	}
	// cache.misses should not be recorded when cached=true
	if foundMisses {
		t.Error("did not expect agentshield.cache.misses metric for cached evaluation")
	}
}

func TestMetricsRecorder_RecordEvaluation_MultipleEvaluations(t *testing.T) {
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	meter := mp.Meter("agentshield-test")

	rec, err := NewMetricsRecorder(meter)
	if err != nil {
		t.Fatalf("NewMetricsRecorder failed: %v", err)
	}

	rec.RecordEvaluation(context.Background(), "bash", "block", 2, false)
	rec.RecordEvaluation(context.Background(), "curl", "allow", 0, true)
	rec.RecordEvaluation(context.Background(), "bash", "allow", 1, false)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect failed: %v", err)
	}

	foundMetrics := make(map[string]bool)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			foundMetrics[m.Name] = true
		}
	}

	expected := []string{
		"agentshield.evaluations.total",
		"agentshield.alerts.total",
		"agentshield.cache.hits",
		"agentshield.cache.misses",
	}
	for _, name := range expected {
		if !foundMetrics[name] {
			t.Errorf("expected metric %q to be recorded", name)
		}
	}
}
