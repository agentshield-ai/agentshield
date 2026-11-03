package llm

import (
	"context"
	"testing"
)

// The server environment must not be readable through secret names a project
// or user chooses (scorer headers, OAuth secrets, custom provider keys), and
// the operator's provider key must not follow a project-chosen base URL.
func TestEnvironmentNotExposedThroughSecretNames(t *testing.T) {
	t.Setenv("WEFT_CANARY", "canary-value")
	t.Setenv("OPENAI_API_KEY", "sk-operator")
	t.Setenv("OPENAI_BASE_URL", "")
	ctx := context.Background()
	stored := map[string]string{}
	c := New(func(_ context.Context, n string) (string, bool) { v, ok := stored[n]; return v, ok }, func(_ context.Context, _, name string) (*CustomProvider, error) {
		if name == "evil" {
			return &CustomProvider{Name: "evil", BaseURL: "https://attacker.example/v1", APIKeyName: "OPENAI_API_KEY"}, nil
		}
		return nil, nil
	})

	if v, ok := c.LookupSecret(ctx, "WEFT_CANARY"); ok || v != "" {
		t.Fatalf("LookupSecret read the environment: %q", v)
	}
	// Built-in provider at its default base URL: the operator key is used.
	rt, err := c.Resolve(ctx, "e/p", "openai/gpt-4o-mini")
	if err != nil {
		t.Fatal(err)
	}
	if k, ok := c.routeKey(ctx, rt); !ok || k != "sk-operator" {
		t.Fatalf("built-in route key = %q %v", k, ok)
	}
	// A custom provider naming the operator's key gets nothing.
	rt, err = c.Resolve(ctx, "e/p", "evil/gpt-4o")
	if err != nil {
		t.Fatal(err)
	}
	if k, ok := c.routeKey(ctx, rt); ok || k != "" {
		t.Fatalf("custom provider got the operator key: %q", k)
	}
	// A project-stored OPENAI_BASE_URL redirects openai/ calls: the operator
	// key must not be sent there; a stored key is.
	stored["OPENAI_BASE_URL"] = "https://attacker.example/v1"
	rt, _ = c.Resolve(ctx, "e/p", "openai/gpt-4o-mini")
	if k, ok := c.routeKey(ctx, rt); ok || k != "" {
		t.Fatalf("operator key sent to stored base URL: %q", k)
	}
	stored["OPENAI_API_KEY"] = "sk-project"
	if k, _ := c.routeKey(ctx, rt); k != "sk-project" {
		t.Fatalf("stored key = %q", k)
	}
	// The operator's own OPENAI_BASE_URL keeps the env key.
	delete(stored, "OPENAI_BASE_URL")
	delete(stored, "OPENAI_API_KEY")
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:11434/v1")
	rt, _ = c.Resolve(ctx, "e/p", "openai/gpt-4o-mini")
	if k, _ := c.routeKey(ctx, rt); k != "sk-operator" || rt.BaseURL != "http://127.0.0.1:11434/v1" {
		t.Fatalf("operator base URL route: key %q base %q", k, rt.BaseURL)
	}
}
