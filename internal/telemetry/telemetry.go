// Package telemetry provides OpenTelemetry integration for AgentShield.
package telemetry

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
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

	hostPort, path := splitEndpoint(cfg.Endpoint)
	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(hostPort),
	}
	if path != "" {
		opts = append(opts, otlptracehttp.WithURLPath(path))
	}
	if cfg.Insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}
	if h := exportHeaders(cfg); len(h) > 0 {
		opts = append(opts, otlptracehttp.WithHeaders(h))
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

// InitMeter creates an OTel MeterProvider with an OTLP/HTTP metric exporter.
// Returns nil when telemetry is disabled.
func InitMeter(ctx context.Context, cfg *config.TelemetryConfig) (*sdkmetric.MeterProvider, error) {
	if !cfg.Enabled || cfg.DisableMetrics {
		return nil, nil
	}

	hostPort, _ := splitEndpoint(cfg.Endpoint)
	opts := []otlpmetrichttp.Option{
		otlpmetrichttp.WithEndpoint(hostPort),
	}
	if cfg.Insecure {
		opts = append(opts, otlpmetrichttp.WithInsecure())
	}
	if h := exportHeaders(cfg); len(h) > 0 {
		opts = append(opts, otlpmetrichttp.WithHeaders(h))
	}

	exporter, err := otlpmetrichttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("creating OTLP metric exporter: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter)),
	)
	return mp, nil
}

// splitEndpoint returns the host:port of an endpoint and its URL path, if it
// has one other than "/" (e.g. Weft's /otel/v1/traces).
func splitEndpoint(endpoint string) (hostPort, path string) {
	rest := stripScheme(endpoint)
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		hostPort, path = rest[:i], rest[i:]
		if path == "/" {
			path = ""
		}
		return hostPort, path
	}
	return rest, ""
}

// exportHeaders are the configured headers plus the bearer token from
// api_key_env. Nil when neither is set, so OTEL_EXPORTER_OTLP_HEADERS still
// applies.
func exportHeaders(cfg *config.TelemetryConfig) map[string]string {
	var h map[string]string
	if len(cfg.Headers) > 0 || cfg.APIKeyEnv != "" {
		h = make(map[string]string, len(cfg.Headers)+1)
	}
	for k, v := range cfg.Headers {
		h[k] = v
	}
	if cfg.APIKeyEnv != "" {
		if key := os.Getenv(cfg.APIKeyEnv); key != "" {
			h["Authorization"] = "Bearer " + key
		}
	}
	return h
}

// stripScheme removes http:// or https:// prefix since the OTel HTTP exporter
// takes a bare host:port and uses WithInsecure() to control TLS.
func stripScheme(endpoint string) string {
	for _, prefix := range []string{"https://", "http://"} {
		if strings.HasPrefix(endpoint, prefix) {
			return endpoint[len(prefix):]
		}
	}
	return endpoint
}
