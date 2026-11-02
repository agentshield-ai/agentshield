package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/egress"
)

func TestEgressFlags(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	prev := egress.Default()
	t.Cleanup(func() { egress.SetDefault(prev) })
	ctx := context.Background()
	str := func(s string) *string { return &s }
	b := func(v bool) *bool { return &v }

	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("OLLAMA_BASE_URL", "")

	// Defaults: loopback and metadata refused.
	mode, err := (&egressFlags{allow: str(""), allowPrivate: b(false)}).apply(log)
	if err != nil || mode != "public-only" {
		t.Fatalf("mode %q err %v", mode, err)
	}
	for _, u := range []string{"http://127.0.0.1:11434/", "http://169.254.169.254/", "http://10.0.0.1/"} {
		if err := egress.Default().CheckURL(ctx, u); !egress.IsBlocked(err) {
			t.Errorf("%s: %v", u, err)
		}
	}

	// --egress-allow with host:port and CIDR entries.
	if _, err := (&egressFlags{allow: str("127.0.0.1:11434, 10.0.0.0/8"), allowPrivate: b(false)}).apply(log); err != nil {
		t.Fatal(err)
	}
	for u, ok := range map[string]bool{"http://127.0.0.1:11434/": true, "http://127.0.0.1:8000/": false, "http://10.2.3.4/": true, "http://192.168.0.1/": false} {
		if err := egress.Default().CheckURL(ctx, u); (err == nil) != ok {
			t.Errorf("%s: %v (want allowed=%v)", u, err, ok)
		}
	}

	// --egress-allow-private keeps metadata blocked.
	if mode, _ := (&egressFlags{allow: str(""), allowPrivate: b(true)}).apply(log); mode != "private-allowed" {
		t.Fatalf("mode %q", mode)
	}
	if err := egress.Default().CheckURL(ctx, "http://192.168.0.1/"); err != nil {
		t.Error(err)
	}
	if err := egress.Default().CheckURL(ctx, "http://169.254.169.254/"); !egress.IsBlocked(err) {
		t.Errorf("metadata: %v", err)
	}

	// Provider base URLs from the server's environment are operator config.
	t.Setenv("OLLAMA_BASE_URL", "http://127.0.0.1:11434/v1")
	if _, err := (&egressFlags{allow: str(""), allowPrivate: b(false)}).apply(log); err != nil {
		t.Fatal(err)
	}
	if err := egress.Default().CheckURL(ctx, "http://127.0.0.1:11434/v1/chat/completions"); err != nil {
		t.Errorf("env base URL: %v", err)
	}
	if err := egress.Default().CheckURL(ctx, "http://127.0.0.1:11435/"); !egress.IsBlocked(err) {
		t.Errorf("other port: %v", err)
	}

	// Bad entries are rejected at startup.
	if _, err := (&egressFlags{allow: str("http://x"), allowPrivate: b(false)}).apply(log); err == nil || !strings.Contains(err.Error(), "egress-allow") {
		t.Fatalf("bad entry: %v", err)
	}
}
