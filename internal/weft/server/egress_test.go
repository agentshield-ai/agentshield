package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/egress"
)

// strictEgress installs the default (production) egress policy for one test,
// optionally with allow entries, and restores the package's test allowlist.
func strictEgress(t *testing.T, allow ...string) {
	t.Helper()
	prev := egress.SetDefault(egress.MustNew(egress.Config{Allow: allow}))
	t.Cleanup(func() { egress.SetDefault(prev) })
}

// internalListener stands in for an internal admin endpoint and counts hits.
func internalListener(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, `{"internal_secret":"s3cr3t","score":1}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// TestRemoteScorerEgressBlocked reproduces audit spot A12: a RemoteScorer
// whose url is a loopback address, run through /calls/score, must not reach
// the address, and the reply must not become feedback.
func TestRemoteScorerEgressBlocked(t *testing.T) {
	e := newEnv(t)
	endCall(e, "c1", map[string]any{"q": 1}, "hello", "")
	srv, hits := internalListener(t)
	strictEgress(t)

	ref := publish(e, "remote", scorerVal("RemoteScorer", map[string]any{"name": "remote", "url": srv.URL + "/internal-admin"}))
	r := scoreResult(t, e, "c1", ref)
	if hits.Load() != 0 {
		t.Fatalf("the loopback listener was reached %d times", hits.Load())
	}
	msg := fmt.Sprint(r["error"])
	if r["error"] == nil || !strings.Contains(msg, "destination not allowed") {
		t.Fatalf("score result = %v", r)
	}
	if strings.Contains(msg, "dial") || strings.Contains(msg, "s3cr3t") {
		t.Fatalf("error leaks details: %q", msg)
	}
	fb := e.post("/feedback/query", map[string]any{"project_id": pid})["result"].([]any)
	for _, f := range fb {
		b, _ := json.Marshal(f)
		if strings.Contains(string(b), "s3cr3t") {
			t.Fatalf("internal reply stored as feedback: %s", b)
		}
	}

	// A hostname that resolves to loopback is refused the same way.
	u, _ := url.Parse(srv.URL)
	ref = publish(e, "remote-host", scorerVal("RemoteScorer", map[string]any{"name": "remote-host", "url": "http://localhost:" + u.Port() + "/x"}))
	if r := scoreResult(t, e, "c1", ref); r["error"] == nil || hits.Load() != 0 {
		t.Fatalf("localhost: %v (hits %d)", r, hits.Load())
	}

	// The operator can allow the endpoint explicitly.
	strictEgress(t, "127.0.0.1:"+u.Port())
	ref = publish(e, "remote", scorerVal("RemoteScorer", map[string]any{"name": "remote", "url": srv.URL + "/internal-admin"}))
	if out := scoreOne(t, e, "c1", ref); out["score"] != float64(1) || hits.Load() != 1 {
		t.Fatalf("allowed: %v (hits %d)", out, hits.Load())
	}
}

// TestRemoteScorerNoRedirects: a remote scorer's redirect is not
// followed, so an allowed endpoint cannot bounce the request elsewhere.
func TestRemoteScorerNoRedirects(t *testing.T) {
	e := newEnv(t)
	endCall(e, "c1", map[string]any{"q": 1}, "hello", "")
	internal, hits := internalListener(t)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL+"/admin", http.StatusTemporaryRedirect)
	}))
	defer front.Close()
	fu, _ := url.Parse(front.URL)
	strictEgress(t, "127.0.0.1:"+fu.Port())
	ref := publish(e, "bounce", scorerVal("RemoteScorer", map[string]any{"name": "bounce", "url": front.URL + "/score"}))
	if r := scoreResult(t, e, "c1", ref); r["error"] == nil || hits.Load() != 0 {
		t.Fatalf("redirect: %v (hits %d)", r, hits.Load())
	}
}

// TestProviderBaseURLEgressBlocked: a project's custom provider pointing at
// an internal address cannot be used to reach it.
func TestProviderBaseURLEgressBlocked(t *testing.T) {
	e := newEnv(t)
	srv, hits := internalListener(t)
	strictEgress(t)
	publish(e, "intprov", map[string]any{"_type": "Provider", "_class_name": "Provider", "base_url": srv.URL + "/v1", "api_key_name": "NOPE"})
	e.post("/secrets/set", map[string]any{"name": "NOPE", "value": "k"})
	status, raw := e.raw("/completions/create", map[string]any{"project_id": pid,
		"inputs": map[string]any{"model": "intprov/m", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}}, nil)
	body := string(raw)
	if hits.Load() != 0 {
		t.Fatalf("provider base URL reached (%d), status %d: %s", hits.Load(), status, body)
	}
	if !strings.Contains(body, "destination not allowed") || strings.Contains(body, "dial tcp") {
		t.Fatalf("status %d body %s", status, body)
	}
}
