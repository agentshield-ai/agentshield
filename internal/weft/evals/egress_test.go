package evals

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/egress"
	"github.com/agentshield-ai/agentshield/internal/weft/llm"
)

// The OAuth token URL is checked separately from the scorer URL: an allowed
// scorer cannot be used to make Weft fetch a token from an internal address.
func TestEgressGuardsTokenURLAndPredict(t *testing.T) {
	var hits atomic.Int32
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"access_token":"x","token_type":"Bearer","expires_in":60,"output":"secret"}`))
	}))
	defer internal.Close()
	scorer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"score":1}`)) }))
	defer scorer.Close()
	su, _ := url.Parse(scorer.URL)
	prev := egress.SetDefault(egress.MustNew(egress.Config{Allow: []string{"127.0.0.1:" + su.Port()}}))
	defer egress.SetDefault(prev)

	e := &Engine{LLM: llm.New(llm.StaticSecrets(map[string]string{"SEC": "s"}), nil)}
	val := map[string]any{"url": scorer.URL, "auth_config": map[string]any{"token_url": internal.URL + "/token", "client_id": "c", "client_secret_secret": "SEC"}}
	_, err := scoreRemote(e, context.Background(), "e/p", val, map[string]any{"q": 1}, "x", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "destination not allowed") || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("token URL: %v", err)
	}
	// Remote predict endpoints (predict_url models) are guarded too.
	_, _, err = e.remotePredict(context.Background(), "e/p", &predictor{url: internal.URL + "/predict", timeout: 5e9}, "ref", map[string]any{"q": 1})
	if err == nil || !strings.Contains(err.Error(), "destination not allowed") {
		t.Fatalf("predict: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("internal endpoint reached %d times", hits.Load())
	}
}
