package main

import (
	"bytes"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/server"
)

// D2: the server bounds request headers, bodies and idle connections.
func TestHTTPServerTimeouts(t *testing.T) {
	s := newHTTPServer("127.0.0.1:0", http.NotFoundHandler())
	if s.ReadHeaderTimeout == 0 || s.ReadTimeout == 0 || s.IdleTimeout == 0 || s.MaxHeaderBytes == 0 {
		t.Fatalf("timeouts not set: %+v", s)
	}
	// A slow body is cut off at ReadTimeout (shortened here).
	s.ReadTimeout = 300 * time.Millisecond
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = r.Body.Read(make([]byte, 10))
		_, _ = r.Body.Read(make([]byte, 10)) // blocks until the deadline
	})
	go func() { _ = s.Serve(ln) }()
	defer s.Close()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write([]byte("POST / HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\nA"))
	start := time.Now()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _ = c.Read(make([]byte, 100))
	buf := make([]byte, 100)
	for {
		if _, err := c.Read(buf); err != nil {
			break
		}
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("slow body held the connection for %v", d)
	}
}

// A2/U4: the startup warning covers host names, and the host allowlist
// gets the listen address's name.
func TestOpenBindWarning(t *testing.T) {
	for _, tc := range []struct {
		host string
		want bool
	}{{"", true}, {"0.0.0.0", true}, {"::", true}, {"10.0.0.5", true}, {"127.0.0.1", false}, {"::1", false}, {"localhost", false},
		{"no-such-host.invalid", true}} {
		if got, _ := bindExposed(tc.host); got != tc.want {
			t.Errorf("bindExposed(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	warnOpenBind("no-such-host.invalid:6345", false, log, false)
	if !strings.Contains(buf.String(), "AUTHENTICATION IS DISABLED") {
		t.Fatalf("no warning for a host name: %s", buf.String())
	}
	buf.Reset()
	warnOpenBind("0.0.0.0:6345", true, log, false)
	if buf.Len() != 0 {
		t.Fatalf("warning with auth on: %s", buf.String())
	}
	f := &netFlags{allowedHosts: strPtr("weft.example.com"), trustedProxies: strPtr("10.0.0.0/8, 192.168.1.1")}
	var cfg server.Config
	if err := f.apply(&cfg, "weft.internal:6345"); err != nil {
		t.Fatal(err)
	}
	if !cfg.EnforceHosts || len(cfg.AllowedHosts) != 2 || cfg.AllowedHosts[1] != "weft.internal" || len(cfg.TrustedProxies) != 2 {
		t.Fatalf("cfg = %+v", cfg)
	}
	bad := &netFlags{allowedHosts: strPtr(""), trustedProxies: strPtr("not-an-ip")}
	if err := bad.apply(&cfg, "127.0.0.1:1"); err == nil {
		t.Fatal("bad --trusted-proxies accepted")
	}
}

func strPtr(s string) *string { return &s }

// The connection caps and the response budget come from their flags;
// negative values are refused.
func TestNetFlagsLimits(t *testing.T) {
	n := func(v int) *int { return &v }
	f := &netFlags{allowedHosts: strPtr(""), trustedProxies: strPtr(""), maxConns: n(10), maxConnsPerIP: n(3), responseBudgetMB: n(64)}
	var cfg server.Config
	if err := f.apply(&cfg, "127.0.0.1:6345"); err != nil || cfg.MaxRequestsPerClient != 3 || cfg.ResponseBudget != 64<<20 {
		t.Fatalf("cfg = %+v, %v", cfg, err)
	}
	f.maxConnsPerIP = n(-1)
	if err := f.apply(&cfg, "127.0.0.1:6345"); err == nil {
		t.Fatal("negative cap accepted")
	}
}
