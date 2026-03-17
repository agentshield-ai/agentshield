// Package telemetry provides OpenTelemetry integration for AgentShield.
//
// This file pins OTel SDK dependencies so that go mod tidy does not remove
// them before the consuming code lands in later tasks.
package telemetry

import (
	_ "github.com/riandyrn/otelchi"
	_ "go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	_ "go.opentelemetry.io/otel"
	_ "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	_ "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	_ "go.opentelemetry.io/otel/sdk/metric"
	_ "go.opentelemetry.io/otel/sdk/resource"
	_ "go.opentelemetry.io/otel/sdk/trace"
	_ "go.opentelemetry.io/otel/semconv/v1.26.0"
	_ "go.opentelemetry.io/otel/trace"
)
