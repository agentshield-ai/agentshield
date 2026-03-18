// Package telemetry provides OpenTelemetry integration for AgentShield.
package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/agentshield-ai/agentshield/internal/config"
)

// ShutdownFunc is called to flush and shut down the telemetry provider.
type ShutdownFunc func(ctx context.Context) error

// Init initialises the OpenTelemetry TracerProvider. When telemetry is disabled
// it returns a no-op provider. The returned ShutdownFunc must be called on
// process exit to flush pending spans.
func Init(ctx context.Context, cfg *config.TelemetryConfig, version string) (trace.TracerProvider, ShutdownFunc, error) {
	if !cfg.Enabled {
		noop := tracenoop.NewTracerProvider()
		return noop, func(_ context.Context) error { return nil }, nil
	}

	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(stripScheme(cfg.Endpoint)),
	}
	if cfg.Insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}

	exporter, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("creating OTLP trace exporter: %w", err)
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(version),
		),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("creating OTel resource: %w", err)
	}

	sampler := sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRate))

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	)
	otel.SetTracerProvider(tp)

	shutdown := func(ctx context.Context) error {
		return tp.Shutdown(ctx)
	}
	return tp, shutdown, nil
}

// stripScheme removes http:// or https:// prefix since the OTel HTTP exporter
// takes a bare host:port and uses WithInsecure() to control TLS.
func stripScheme(endpoint string) string {
	for _, prefix := range []string{"https://", "http://"} {
		if len(endpoint) > len(prefix) && endpoint[:len(prefix)] == prefix {
			return endpoint[len(prefix):]
		}
	}
	return endpoint
}
