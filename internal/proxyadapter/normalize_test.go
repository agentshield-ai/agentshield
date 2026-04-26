package proxyadapter

import (
	"errors"
	"testing"
)

func TestNormalize_RequiresFields(t *testing.T) {
	tests := []struct {
		name string
		obs  Observation
	}{
		{"missing request_id", Observation{Method: "GET", URL: "https://x.example/"}},
		{"missing method", Observation{RequestID: "r1", URL: "https://x.example/"}},
		{"missing url", Observation{RequestID: "r1", Method: "GET"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Normalize(tt.obs, "crabtrap")
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestNormalize_ProxyMetadataPropagates(t *testing.T) {
	obs := Observation{
		RequestID:  "req-42",
		Method:     "POST",
		URL:        "https://discord.com/api/webhooks/abc",
		SessionID:  "agent-session-7",
		Status:     403,
		Decision:   "block",
		DecidedBy:  "rule",
		DurationMs: 12,
		User:       "alice",
	}
	req, err := Normalize(obs, "crabtrap")
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}

	if req.EventID != "req-42" {
		t.Errorf("EventID = %q", req.EventID)
	}
	if req.SessionID != "agent-session-7" {
		t.Errorf("SessionID = %q", req.SessionID)
	}
	if req.Tool != "http_egress" {
		t.Errorf("Tool = %q", req.Tool)
	}
	if req.Source != "crabtrap" {
		t.Errorf("Source = %q", req.Source)
	}

	wantFields := map[string]string{
		"event_type":        "http_egress",
		"tool":              "http_egress",
		"command":           "POST https://discord.com/api/webhooks/abc",
		"source":            "crabtrap",
		"proxy.source":      "crabtrap",
		"proxy.verdict":     "block",
		"proxy.decided_by":  "rule",
		"proxy.status":      "403",
		"proxy.duration_ms": "12",
		"proxy.user":        "alice",
	}
	for k, want := range wantFields {
		if got := req.Fields[k]; got != want {
			t.Errorf("fields[%q] = %q, want %q", k, got, want)
		}
	}

	if req.Args["url"] != "https://discord.com/api/webhooks/abc" {
		t.Errorf("Args[url] = %q", req.Args["url"])
	}
	if req.Args["method"] != "POST" {
		t.Errorf("Args[method] = %q", req.Args["method"])
	}
}

func TestNormalize_SessionFallbackToSrcIP(t *testing.T) {
	obs := Observation{
		RequestID: "req-1",
		Method:    "GET",
		URL:       "https://x.example/",
		SrcIP:     "10.1.2.3",
	}
	req, err := Normalize(obs, "crabtrap")
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	want := "proxy-crabtrap-10.1.2.3"
	if req.SessionID != want {
		t.Errorf("SessionID = %q, want %q", req.SessionID, want)
	}
}

func TestNormalize_NoSessionWhenNothingProvided(t *testing.T) {
	obs := Observation{
		RequestID: "req-1",
		Method:    "GET",
		URL:       "https://x.example/",
	}
	req, err := Normalize(obs, "crabtrap")
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if req.SessionID != "" {
		t.Errorf("SessionID = %q, want empty (no session_id, no src_ip)", req.SessionID)
	}
}

func TestNormalize_OmitsEmptyOptionalProxyFields(t *testing.T) {
	obs := Observation{
		RequestID: "req-1",
		Method:    "GET",
		URL:       "https://x.example/",
	}
	req, err := Normalize(obs, "crabtrap")
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	for _, k := range []string{"proxy.verdict", "proxy.decided_by", "proxy.status", "proxy.duration_ms", "proxy.user"} {
		if _, present := req.Fields[k]; present {
			t.Errorf("fields[%q] should be absent when source observation has no value", k)
		}
	}
}
