package llm

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// E3: provider responses are bounded (plain, gzip-compressed and streamed).
func TestProviderResponsesBounded(t *testing.T) {
	prevC, prevS, prevSC := MaxCompletionResponseBytes, MaxStreamBytes, MaxStreamContentBytes
	MaxCompletionResponseBytes, MaxStreamBytes, MaxStreamContentBytes = 1<<20, 4<<20, 1<<20
	defer func() { MaxCompletionResponseBytes, MaxStreamBytes, MaxStreamContentBytes = prevC, prevS, prevSC }()

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(`{"choices":[{"message":{"content":"`))
	_, _ = zw.Write(bytes.Repeat([]byte("A"), 8<<20))
	_, _ = zw.Write([]byte(`"}}]}`))
	_ = zw.Close()
	var sent atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/big/chat/completions":
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"`))
			chunk := bytes.Repeat([]byte("A"), 64<<10)
			for i := 0; i < 256; i++ { // 16 MiB
				n, err := w.Write(chunk)
				sent.Add(int64(n))
				if err != nil {
					return
				}
			}
			_, _ = w.Write([]byte(`"}}]}`))
		case "/gzip/chat/completions":
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write(gz.Bytes())
		case "/stream/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			piece := strings.Repeat("B", 16<<10)
			for i := 0; i < 200; i++ { // 3.2 MiB of content
				if _, err := fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", piece); err != nil {
					return
				}
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	for _, path := range []string{"/big", "/gzip"} {
		c := New(StaticSecrets(map[string]string{"OPENAI_BASE_URL": srv.URL + path, "OPENAI_API_KEY": "k"}), nil)
		_, err := c.Complete(ctx, "e/p", map[string]any{"model": "gpt-4o", "messages": []any{}})
		if !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("%s: %v", path, err)
		}
	}
	c := New(StaticSecrets(map[string]string{"OPENAI_BASE_URL": srv.URL + "/stream", "OPENAI_API_KEY": "k"}), nil)
	_, err := c.Stream(ctx, "e/p", map[string]any{"model": "gpt-4o", "messages": []any{}}, func(map[string]any) error { return nil })
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("stream: %v", err)
	}
}

// E2: when the base URL comes from a stored secret, a failed request does not
// report it.
func TestStoredBaseURLNotInErrors(t *testing.T) {
	secretURL := "http://127.0.0.1:1/v1/SECRET-PATH-TOKEN"
	c := New(StaticSecrets(map[string]string{"OPENAI_BASE_URL": secretURL, "OPENAI_API_KEY": "k"}), nil)
	_, err := c.Complete(context.Background(), "e/p", map[string]any{"model": "gpt-4o", "messages": []any{}})
	if err == nil || strings.Contains(err.Error(), "SECRET-PATH-TOKEN") || strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("error = %v", err)
	}
}

// E7: the provider client does not follow redirects, so the key (Authorization
// or x-api-key) never reaches the redirect target.
func TestProviderRedirectNotFollowed(t *testing.T) {
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("x-api-key") != "" {
			leaked.Add(1)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer target.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, strings.Replace(target.URL, "127.0.0.1", "localhost", 1)+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redir.Close()
	c := New(StaticSecrets(map[string]string{"OPENAI_BASE_URL": redir.URL, "OPENAI_API_KEY": "k"}), nil)
	_, err := c.Complete(context.Background(), "e/p", map[string]any{"model": "gpt-4o", "messages": []any{}})
	if err == nil || !strings.Contains(err.Error(), "307") {
		t.Fatalf("redirect: %v", err)
	}
	if leaked.Load() != 0 {
		t.Fatal("the key followed the redirect")
	}
}

// E6 / E1 policy table for UseSecret.
func TestUseSecretPolicy(t *testing.T) {
	recs := map[string]*store.SecretRecord{
		"SRV":    {SecretInfo: store.SecretInfo{Scope: store.SecretScopeServer}, Value: "s"},
		"SHARED": {SecretInfo: store.SecretInfo{Scope: store.SecretScopeServer, AllowedHosts: []string{"api.example.com", "*.corp.example"}}, Value: "s"},
		"PROJ":   {SecretInfo: store.SecretInfo{Scope: store.SecretScopeProject}, Value: "p"},
		"PINNED": {SecretInfo: store.SecretInfo{Scope: store.SecretScopeProject, AllowedHosts: []string{"api.example.com:8443"}}, Value: "p"},
		"HOOK":   {SecretInfo: store.SecretInfo{Scope: store.SecretScopeProject}, Value: "http://hooks.example.com/T/x"},
		"HOOKS":  {SecretInfo: store.SecretInfo{Scope: store.SecretScopeProject}, Value: "https://hooks.example.com/T/x"},
	}
	open := false
	c := &Client{Secret: func(_ context.Context, _, name string) (*store.SecretRecord, error) { return recs[name], nil },
		ServerSecretsOpen: func(context.Context) bool { return open }}
	ctx := context.Background()
	for _, tc := range []struct {
		use SecretUse
		ok  bool
	}{
		{SecretUse{ProjectID: "e/p", Name: "SRV", Dest: "https://x.example"}, false},
		{SecretUse{ProjectID: "e/p", Name: "SRV", Dest: "https://x.example", Builtin: true}, true},
		{SecretUse{ProjectID: "e/p", Name: "SHARED", Dest: "https://api.example.com/v1"}, true},
		{SecretUse{ProjectID: "e/p", Name: "SHARED", Dest: "https://a.b.corp.example/v1"}, true},
		{SecretUse{ProjectID: "e/p", Name: "SHARED", Dest: "https://evil.example/v1"}, false},
		{SecretUse{ProjectID: "e/p", Name: "PROJ", Dest: "https://evil.example/v1"}, true},
		{SecretUse{ProjectID: "e/p", Name: "PROJ", Dest: "http://evil.example/v1"}, false}, // E6
		{SecretUse{ProjectID: "e/p", Name: "PROJ", Dest: "http://localhost:8080/v1"}, true},
		{SecretUse{ProjectID: "e/p", Name: "PROJ", Dest: "http://127.0.0.1:8080/v1"}, true},
		{SecretUse{ProjectID: "e/p", Name: "PROJ", Dest: "http://internal:8080/v1", OperatorDest: true}, true},
		{SecretUse{ProjectID: "e/p", Name: "PINNED", Dest: "https://api.example.com:8443/v1"}, true},
		{SecretUse{ProjectID: "e/p", Name: "PINNED", Dest: "https://api.example.com/v1"}, false},
		{SecretUse{ProjectID: "e/p", Name: "HOOK", URLValued: true}, false},
		{SecretUse{ProjectID: "e/p", Name: "HOOKS", URLValued: true}, true},
		{SecretUse{ProjectID: "e/p", Name: "PROJ", Dest: "https://x.example", MinScope: store.SecretScopeProject}, true},
		{SecretUse{ProjectID: "e/p", Name: "SRV", Dest: "https://x.example", Builtin: true, MinScope: store.SecretScopeProject}, false},
		{SecretUse{ProjectID: "e/p", Name: "MISSING", Dest: "https://x.example"}, false},
	} {
		_, _, err := c.UseSecret(ctx, tc.use)
		if (err == nil) != tc.ok {
			t.Errorf("%+v: err = %v, want ok=%v", tc.use, err, tc.ok)
		}
	}
	open = true
	if _, _, err := c.UseSecret(ctx, SecretUse{ProjectID: "e/p", Name: "SRV", Dest: "https://x.example"}); err != nil {
		t.Fatalf("server secret while only admins exist: %v", err)
	}
}
