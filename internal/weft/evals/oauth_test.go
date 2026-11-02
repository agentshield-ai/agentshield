package evals

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/llm"
	"golang.org/x/oauth2"
)

// TestRemoteScorerOAuth checks the client-credentials flow: the token is
// fetched with the stored secret, cached, sent to the scorer and fetched
// again once it expires.
func TestRemoteScorerOAuth(t *testing.T) {
	var issued, scored atomic.Int32
	expiresIn := 3600
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id, sec, ok := r.BasicAuth()
		if !ok {
			id, sec = r.Form.Get("client_id"), r.Form.Get("client_secret")
		}
		if r.Form.Get("grant_type") != "client_credentials" || id != "weft" || sec != "s3cret" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_client"})
			return
		}
		if r.Form.Get("scope") != "a b" || r.Form.Get("audience") != "https://scorer" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_scope"})
			return
		}
		n := issued.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-" + string(rune('0'+n)), "token_type": "Bearer", "expires_in": expiresIn})
	}))
	defer tokenSrv.Close()
	var lastAuth atomic.Value
	scorer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scored.Add(1)
		lastAuth.Store(r.Header.Get("Authorization"))
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer tok-") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"score": 1})
	}))
	defer scorer.Close()

	secrets := map[string]string{"SCORER_SECRET": "s3cret", "BAD": "nope"}
	e := &Engine{LLM: llm.New(func(_ context.Context, name string) (string, bool) { v, ok := secrets[name]; return v, ok }, nil)}
	val := func(secret string) map[string]any {
		return map[string]any{"url": scorer.URL, "auth_config": map[string]any{"token_url": tokenSrv.URL, "client_id": "weft",
			"client_secret_secret": secret, "scopes": []any{"b", "a"}, "audience": "https://scorer"}}
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		out, err := scoreRemote(e, ctx, "e/p", val("SCORER_SECRET"), map[string]any{"q": 1}, "x", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if m, _ := out.(map[string]any); m == nil {
			t.Fatalf("score = %v", out)
		}
	}
	if issued.Load() != 1 || scored.Load() != 3 || lastAuth.Load() != "Bearer tok-1" {
		t.Fatalf("issued=%d scored=%d auth=%v; want one cached token", issued.Load(), scored.Load(), lastAuth.Load())
	}

	// A token that expires within the refresh margin is fetched again.
	oauthMu.Lock()
	oauthSources = map[string]oauth2.TokenSource{}
	oauthMu.Unlock()
	expiresIn = 1
	for i := 0; i < 2; i++ {
		if _, err := scoreRemote(e, ctx, "e/p", val("SCORER_SECRET"), nil, "x", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if issued.Load() != 3 {
		t.Fatalf("issued=%d, want a fresh token per call for short-lived tokens", issued.Load())
	}

	// Wrong client secret: a clear error that does not echo the secret.
	_, err := scoreRemote(e, ctx, "e/p", val("BAD"), nil, "x", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid_client") || strings.Contains(err.Error(), "nope") {
		t.Fatalf("bad secret err = %v", err)
	}
	if _, err := scoreRemote(e, ctx, "e/p", val("MISSING"), nil, "x", nil, nil); err == nil || !strings.Contains(err.Error(), "not set") {
		t.Fatalf("missing secret err = %v", err)
	}
	bad := val("SCORER_SECRET")
	bad["auth_config"].(map[string]any)["token_url"] = "file:///etc/passwd"
	if _, err := scoreRemote(e, ctx, "e/p", bad, nil, "x", nil, nil); err == nil {
		t.Fatal("non-http token_url accepted")
	}
}

// TestRemotePredictOAuth: server-side evaluation of a predict_url model uses
// the same client-credentials auth_config, with the token cached across rows.
func TestRemotePredictOAuth(t *testing.T) {
	var issued atomic.Int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		id, sec, ok := r.BasicAuth()
		if !ok {
			id, sec = r.Form.Get("client_id"), r.Form.Get("client_secret")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("grant_type") != "client_credentials" || id != "weft" || sec != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_client"})
			return
		}
		issued.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "predict-tok", "token_type": "Bearer", "expires_in": 3600})
	}))
	defer tokenSrv.Close()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer predict-tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"output": "ok"})
	}))
	defer model.Close()
	secrets := map[string]string{"PREDICT_SECRET": "s3cret", "WRONG": "x"}
	e := &Engine{LLM: llm.New(func(_ context.Context, name string) (string, bool) { v, ok := secrets[name]; return v, ok }, nil)}
	modelVal := func(secret string) map[string]any {
		return map[string]any{"_class_name": "MyModel", "predict_url": model.URL, "auth_config": map[string]any{
			"token_url": tokenSrv.URL, "client_id": "weft", "client_secret_secret": secret, "scopes": "predict"}}
	}
	ctx := context.Background()
	p, err := e.predictorFor(ctx, "e/p", modelVal("PREDICT_SECRET"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		out, _, err := e.remotePredict(ctx, "e/p", p, "weft:///e/p/object/MyModel:abc", map[string]any{"q": i})
		if err != nil {
			t.Fatal(err)
		}
		if out != "ok" {
			t.Fatalf("predict output = %v", out)
		}
	}
	if issued.Load() != 1 {
		t.Fatalf("issued %d tokens, want 1 cached token", issued.Load())
	}
	p, _ = e.predictorFor(ctx, "e/p", modelVal("WRONG"))
	if _, _, err := e.remotePredict(ctx, "e/p", p, "", nil); err == nil || !strings.Contains(err.Error(), "invalid_client") {
		t.Fatalf("bad client secret err = %v", err)
	}
}
