package telemetry

import (
	"testing"

	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// The semantic-conventions package imported here must match the schema the
// OpenTelemetry SDK's default resource uses, or merging them fails and the
// exporter never starts. Bump the semconv import with the SDK.
func TestResourceSchemaMatchesSDK(t *testing.T) {
	ours := resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName("agentshield"))
	if _, err := resource.Merge(resource.Default(), ours); err != nil {
		t.Fatalf("semconv schema %s does not match the SDK's default resource: %v", semconv.SchemaURL, err)
	}
}
