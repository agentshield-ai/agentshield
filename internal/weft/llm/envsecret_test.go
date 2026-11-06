package llm

import (
	"context"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// scopedSecrets is a SecretFunc over records keyed by name (one scope each).
func scopedSecrets(m map[string]*store.SecretRecord) SecretFunc {
	return func(_ context.Context, _, name string) (*store.SecretRecord, error) { return m[name], nil }
}

func rec(scope, value string, hosts ...string) *store.SecretRecord {
	return &store.SecretRecord{SecretInfo: store.SecretInfo{Scope: scope, Kind: store.SecretKindSecret, AllowedHosts: hosts}, Value: value}
}

// The server environment must not be readable through secret names a project
// or user chooses (scorer headers, OAuth secrets, custom provider keys), and
// the operator's provider key must not follow a project-chosen base URL.
func TestEnvironmentNotExposedThroughSecretNames(t *testing.T) {
	t.Setenv("WEFT_CANARY", "canary-value")
	t.Setenv("OPENAI_API_KEY", "sk-operator")
	t.Setenv("OPENAI_BASE_URL", "")
	ctx := context.Background()
	stored := map[string]*store.SecretRecord{}
	c := New(scopedSecrets(stored), func(_ context.Context, _, name string) (*CustomProvider, error) {
		if name == "evil" {
			return &CustomProvider{Name: "evil", BaseURL: "https://attacker.example/v1", APIKeyName: "OPENAI_API_KEY"}, nil
		}
		return nil, nil
	})

	if v, err := c.LookupSecret(ctx, "e/p", "WEFT_CANARY", "https://x.example"); err == nil || v != "" {
		t.Fatalf("LookupSecret read the environment: %q", v)
	}
	// Built-in provider at its default base URL: the operator key is used.
	rt, err := c.Resolve(ctx, "e/p", "openai/gpt-4o-mini")
	if err != nil {
		t.Fatal(err)
	}
	if k, ok, _ := c.routeKey(ctx, rt); !ok || k != "sk-operator" {
		t.Fatalf("built-in route key = %q %v", k, ok)
	}
	// A custom provider naming the operator's key gets nothing.
	rt, err = c.Resolve(ctx, "e/p", "evil/gpt-4o")
	if err != nil {
		t.Fatal(err)
	}
	if k, ok, _ := c.routeKey(ctx, rt); ok || k != "" {
		t.Fatalf("custom provider got the operator key: %q", k)
	}
	// A project-stored OPENAI_BASE_URL redirects openai/ calls: the operator
	// key must not be sent there; a stored key is.
	stored["OPENAI_BASE_URL"] = rec(store.SecretScopeProject, "https://attacker.example/v1")
	rt, _ = c.Resolve(ctx, "e/p", "openai/gpt-4o-mini")
	if k, ok, _ := c.routeKey(ctx, rt); ok || k != "" {
		t.Fatalf("operator key sent to stored base URL: %q", k)
	}
	// A server key does not follow a base URL a project stored.
	stored["OPENAI_API_KEY"] = rec(store.SecretScopeServer, "sk-server")
	if k, _, err := c.routeKey(ctx, rt); k != "" || err == nil {
		t.Fatalf("server key sent to a project's base URL: %q %v", k, err)
	}
	stored["OPENAI_API_KEY"] = rec(store.SecretScopeProject, "sk-project")
	if k, _, _ := c.routeKey(ctx, rt); k != "sk-project" {
		t.Fatalf("stored key = %q", k)
	}
	// The operator's own OPENAI_BASE_URL keeps the env key.
	delete(stored, "OPENAI_BASE_URL")
	delete(stored, "OPENAI_API_KEY")
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:11434/v1")
	rt, _ = c.Resolve(ctx, "e/p", "openai/gpt-4o-mini")
	if k, _, _ := c.routeKey(ctx, rt); k != "sk-operator" || rt.BaseURL != "http://127.0.0.1:11434/v1" {
		t.Fatalf("operator base URL route: key %q base %q", k, rt.BaseURL)
	}
}
