package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

// MetricsRecorder records OTel metrics for AgentShield operations.
type MetricsRecorder struct {
	evalCounter  otelmetric.Int64Counter
	alertCounter otelmetric.Int64Counter
	cacheHits    otelmetric.Int64Counter
	cacheMisses  otelmetric.Int64Counter
}

// NewMetricsRecorder creates a new MetricsRecorder using the given OTel Meter.
func NewMetricsRecorder(meter otelmetric.Meter) (*MetricsRecorder, error) {
	evalCounter, err := meter.Int64Counter("agentshield.evaluations.total",
		otelmetric.WithDescription("Total number of tool call evaluations"),
	)
	if err != nil {
		return nil, err
	}

	alertCounter, err := meter.Int64Counter("agentshield.alerts.total",
		otelmetric.WithDescription("Total number of alerts triggered"),
	)
	if err != nil {
		return nil, err
	}

	cacheHits, err := meter.Int64Counter("agentshield.cache.hits",
		otelmetric.WithDescription("Verdict cache hits"),
	)
	if err != nil {
		return nil, err
	}

	cacheMisses, err := meter.Int64Counter("agentshield.cache.misses",
		otelmetric.WithDescription("Verdict cache misses"),
	)
	if err != nil {
		return nil, err
	}

	return &MetricsRecorder{
		evalCounter:  evalCounter,
		alertCounter: alertCounter,
		cacheHits:    cacheHits,
		cacheMisses:  cacheMisses,
	}, nil
}

// RecordEvaluation records metrics for a single evaluation.
func (m *MetricsRecorder) RecordEvaluation(ctx context.Context, tool, action string, alertCount int, cached bool) {
	attrs := otelmetric.WithAttributes(
		attribute.String("tool.name", tool),
		attribute.String("verdict.action", action),
	)
	m.evalCounter.Add(ctx, 1, attrs)
	m.alertCounter.Add(ctx, int64(alertCount), attrs)

	if cached {
		m.cacheHits.Add(ctx, 1)
	} else {
		m.cacheMisses.Add(ctx, 1)
	}
}
