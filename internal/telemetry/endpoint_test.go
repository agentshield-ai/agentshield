package telemetry

import (
	"testing"

	"github.com/agentshield-ai/agentshield/internal/config"
)

func TestSplitEndpoint(t *testing.T) {
	cases := map[string][2]string{
		"https://otel.example.com:4318":           {"otel.example.com:4318", ""},
		"http://127.0.0.1:6345/otel/v1/traces":    {"127.0.0.1:6345", "/otel/v1/traces"},
		"127.0.0.1:4318":                          {"127.0.0.1:4318", ""},
		"https://collector.example.com/":          {"collector.example.com", ""},
		"https://weft.example.com/otel/v1/traces": {"weft.example.com", "/otel/v1/traces"},
	}
	for in, want := range cases {
		h, p := splitEndpoint(in)
		if h != want[0] || p != want[1] {
			t.Errorf("splitEndpoint(%q) = %q, %q; want %q, %q", in, h, p, want[0], want[1])
		}
	}
}

func TestExportHeaders(t *testing.T) {
	if h := exportHeaders(&config.TelemetryConfig{}); h != nil {
		t.Errorf("no headers configured should return nil, got %v", h)
	}
	t.Setenv("WEFT_KEY_FOR_TEST", "secret")
	h := exportHeaders(&config.TelemetryConfig{Headers: map[string]string{"project_id": "team/app"}, APIKeyEnv: "WEFT_KEY_FOR_TEST"})
	if h["project_id"] != "team/app" || h["Authorization"] != "Bearer secret" {
		t.Errorf("headers = %v", h)
	}
}
