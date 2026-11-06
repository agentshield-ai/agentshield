package evals

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/llm"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// scopedEngine is an engine over a real store whose server has non-admin
// principals (server secrets are not open to projects' objects).
func scopedEngine(t *testing.T) (*Engine, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "weft.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	c := llm.New(st.ResolveSecret, nil)
	c.ServerSecretsOpen = func(context.Context) bool { return false }
	return &Engine{Store: st, LLM: c}, st
}

type authCapture struct {
	srv  *httptest.Server
	mu   sync.Mutex
	auth []string
}

func newAuthCapture(t *testing.T) *authCapture {
	c := &authCapture{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.auth = append(c.auth, r.Header.Get("Authorization")+"|"+r.Header.Get("X-Token"))
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"score": 1, "output": "ok", "access_token": "tok", "token_type": "bearer", "expires_in": 3600})
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *authCapture) seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string{}, c.auth...)
}

// E1: RemoteScorer headers/bearer secrets, predict and runtime secrets and
// OAuth client secrets resolve only secrets the project may use; a server
// secret, bucket credentials or another project's secret is refused before
// anything is sent.
func TestSecretSinksScoped(t *testing.T) {
	e, st := scopedEngine(t)
	ctx := context.Background()
	capture := newAuthCapture(t)
	put := func(w store.SecretWrite) {
		if err := st.PutSecret(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	put(store.SecretWrite{Name: "SERVER_TOKEN", Value: "server-token"})
	put(store.SecretWrite{Name: "BUCKET", Value: `{"access_key_id":"a","secret_access_key":"b"}`, Kind: store.SecretKindBucket})
	put(store.SecretWrite{Name: "OTHER_TOKEN", Value: "other-token", Scope: store.SecretScopeProject, ScopeID: "other/p"})
	put(store.SecretWrite{Name: "MY_TOKEN", Value: "my-token", Scope: store.SecretScopeProject, ScopeID: "e/p"})

	scorer := func(field, name string) map[string]any {
		v := map[string]any{"url": capture.srv.URL}
		switch field {
		case "headers_secret":
			v["headers_secret"] = name
		case "bearer":
			v["auth_config"] = map[string]any{"bearer_secret_name": name}
		case "oauth":
			v["auth_config"] = map[string]any{"token_url": capture.srv.URL + "/token", "client_id": "c", "client_secret_secret": name}
		}
		return v
	}
	for _, name := range []string{"SERVER_TOKEN", "BUCKET", "OTHER_TOKEN"} {
		for _, field := range []string{"headers_secret", "bearer", "oauth"} {
			if _, err := scoreRemote(e, ctx, "e/p", scorer(field, name), map[string]any{}, "x", nil, nil); err == nil {
				t.Fatalf("RemoteScorer %s %s: no error", field, name)
			}
		}
		p := &predictor{url: capture.srv.URL + "/predict", timeout: 5e9, secret: name}
		if _, _, err := e.remotePredict(ctx, "e/p", p, "ref", map[string]any{}); err == nil {
			t.Fatalf("predict %s: no error", name)
		}
	}
	if got := capture.seen(); len(got) != 0 {
		t.Fatalf("the capture double received %v", got)
	}

	// The project's own secret works for every sink.
	if _, err := scoreRemote(e, ctx, "e/p", scorer("bearer", "MY_TOKEN"), map[string]any{}, "x", nil, nil); err != nil {
		t.Fatal(err)
	}
	p := &predictor{url: capture.srv.URL + "/predict", timeout: 5e9, secret: "MY_TOKEN"}
	if _, _, err := e.remotePredict(ctx, "e/p", p, "ref", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if got := capture.seen(); len(got) != 2 || got[0] != "Bearer my-token|" || got[1] != "Bearer my-token|" {
		t.Fatalf("capture = %v", got)
	}
	// ...but not from another project.
	if _, err := scoreRemote(e, ctx, "other2/p", scorer("bearer", "MY_TOKEN"), map[string]any{}, "x", nil, nil); err == nil {
		t.Fatal("another project used e/p's secret")
	}
}

// E6: a stored secret is only sent over https (loopback excepted, for local
// development), unless the operator allows http.
func TestSecretsRequireHTTPS(t *testing.T) {
	e, st := scopedEngine(t)
	ctx := context.Background()
	if err := st.PutSecret(ctx, store.SecretWrite{Name: "TOK", Value: "t", Scope: store.SecretScopeProject, ScopeID: "e/p"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*predictor{
		{url: "http://predict.example.invalid/p", timeout: 5e9, secret: "TOK"},
		{url: "https://predict.example.invalid/p", timeout: 5e9, oauth: map[string]any{"token_url": "http://idp.example.invalid/token", "client_id": "c", "client_secret_secret": "TOK"}},
	} {
		_, _, err := e.remotePredict(ctx, "e/p", p, "ref", map[string]any{})
		if err == nil || !strings.Contains(err.Error(), "https") {
			t.Fatalf("%s: %v", p.url, err)
		}
	}
	prev := llm.AllowInsecureSecretHTTP
	llm.AllowInsecureSecretHTTP = true
	defer func() { llm.AllowInsecureSecretHTTP = prev }()
	_, _, err := e.remotePredict(ctx, "e/p", &predictor{url: "http://predict.example.invalid/p", timeout: 5e9, secret: "TOK"}, "ref", map[string]any{})
	if err != nil && strings.Contains(err.Error(), "https") {
		t.Fatalf("operator opt-in: %v", err)
	}
}
