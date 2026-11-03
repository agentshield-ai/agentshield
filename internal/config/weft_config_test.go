package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadWithWeft(t *testing.T, section string) (*Config, error) {
	t.Helper()
	rulesDir := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	data := "auth:\n  token: \"test-token-that-is-at-least-32-characters-long\"\nrules:\n  dir: \"" + rulesDir + "\"\n" + section
	if err := os.WriteFile(cfgPath, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return LoadConfig(cfgPath)
}

func TestLoadConfig_WeftSection(t *testing.T) {
	cfg, err := loadWithWeft(t, `
weft:
  endpoint: "https://weft.example.com"
  project: "team/app"
  api_key_env: "WEFT_API_KEY"
`)
	if err != nil {
		t.Fatal(err)
	}
	w := cfg.Weft
	if !w.Enabled() || w.Project != "team/app" || w.APIKeyEnv != "WEFT_API_KEY" {
		t.Errorf("weft = %+v", w)
	}
	if w.Send != WeftSendNonAllow || w.QueueSize != DefaultWeftQueueSize || w.TimeoutSec != 10 || w.MaxRetrySec != DefaultMaxRetrySec {
		t.Errorf("defaults not applied: %+v", w)
	}

	cfg, err = loadWithWeft(t, "")
	if err != nil || cfg.Weft.Enabled() {
		t.Fatalf("absent weft section should disable push: %v %+v", err, cfg.Weft)
	}
}

func TestLoadConfig_WeftSectionValidation(t *testing.T) {
	cases := map[string]string{
		"no project":          "weft:\n  endpoint: https://w.example.com\n",
		"bad project":         "weft:\n  endpoint: https://w.example.com\n  project: app\n",
		"bad scheme":          "weft:\n  endpoint: ftp://w.example.com\n  project: a/b\n",
		"remote http":         "weft:\n  endpoint: http://w.example.com\n  project: a/b\n",
		"credentials in url":  "weft:\n  endpoint: https://u:p@w.example.com\n  project: a/b\n",
		"bad send":            "weft:\n  endpoint: https://w.example.com\n  project: a/b\n  send: some\n",
		"negative queue size": "weft:\n  endpoint: https://w.example.com\n  project: a/b\n  queue_size: -1\n",
	}
	for name, section := range cases {
		if _, err := loadWithWeft(t, section); err == nil || !strings.Contains(err.Error(), "weft.") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	ok := map[string]string{
		"loopback http":         "weft:\n  endpoint: http://127.0.0.1:6345\n  project: a/b\n  send: all\n",
		"localhost http":        "weft:\n  endpoint: http://localhost:6345\n  project: a/b\n",
		"remote http insecure":  "weft:\n  endpoint: http://weft.internal:6345\n  project: a/b\n  insecure: true\n",
		"telemetry with path":   "telemetry:\n  enabled: true\n  insecure: true\n  endpoint: http://127.0.0.1:6345/otel/v1/traces\n  headers:\n    project_id: a/b\n  disable_metrics: true\n",
		"explicit queue settle": "weft:\n  endpoint: https://w.example.com\n  project: a/b\n  queue_size: 5\n",
	}
	for name, section := range ok {
		if _, err := loadWithWeft(t, section); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestLoadConfig_TelemetryQueueKeys(t *testing.T) {
	const base = "telemetry:\n  enabled: true\n  endpoint: https://weft.example.com/otel/v1/traces\n"
	cfg, err := loadWithWeft(t, base)
	if err != nil {
		t.Fatal(err)
	}
	if tc := cfg.Telemetry; tc.QueueSize != DefaultTelemetryQueueSize || tc.QueueMaxMB != DefaultTelemetryQueueMaxMB ||
		tc.ExportConcurrency != DefaultTelemetryExportConcurrency || tc.MaxRetrySec != DefaultMaxRetrySec {
		t.Errorf("defaults not applied: %+v", tc)
	}
	// The default queue holds a 300 s outage at 15 verdicts a second, two
	// spans each.
	if DefaultTelemetryQueueSize < 300*15*2 {
		t.Errorf("default queue_size %d is too small", DefaultTelemetryQueueSize)
	}
	cfg, err = loadWithWeft(t, base+"  queue_size: 500\n  queue_max_mb: 8\n  export_concurrency: 2\n")
	if err != nil {
		t.Fatal(err)
	}
	if tc := cfg.Telemetry; tc.QueueSize != 500 || tc.QueueMaxMB != 8 || tc.ExportConcurrency != 2 {
		t.Errorf("telemetry = %+v", tc)
	}
	for _, bad := range []string{"  queue_size: -1\n", "  queue_max_mb: 99999\n", "  export_concurrency: 100\n"} {
		if _, err := loadWithWeft(t, base+bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
