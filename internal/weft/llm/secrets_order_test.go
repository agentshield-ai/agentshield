package llm

import (
	"context"
	"strings"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

func recSecrets(recs ...store.SecretRecord) SecretFunc {
	return func(_ context.Context, _, name string) (*store.SecretRecord, error) {
		for i := range recs {
			if recs[i].Name == name {
				r := recs[i]
				return &r, nil
			}
		}
		return nil, nil
	}
}

func secretRec(name, scope, value string, hosts ...string) store.SecretRecord {
	if hosts == nil {
		hosts = []string{}
	}
	return store.SecretRecord{SecretInfo: store.SecretInfo{Name: name, Scope: scope, Kind: store.SecretKindSecret, AllowedHosts: hosts}, Value: value}
}

// N5: refusals are decided from the secret's scope and allowed hosts before
// its value is read, and a URL-valued secret gets one answer whatever it
// holds: an error never reveals a secret's shape.
func TestSecretErrorsRevealNoShape(t *testing.T) {
	c := New(recSecrets(
		secretRec("SRV_GARBAGE", store.SecretScopeServer, "not a url"),
		secretRec("SRV_URL", store.SecretScopeServer, "https://hooks.example.com/T0/B0/xyz"),
		secretRec("SRV_HTTP", store.SecretScopeServer, "http://hooks.example.com/x"),
		secretRec("H_GARBAGE", store.SecretScopeServer, "not a url", "allowed.example"),
		secretRec("H_URL", store.SecretScopeServer, "https://other.example/x", "allowed.example"),
		secretRec("H_HTTP", store.SecretScopeServer, "http://allowed.example/x", "allowed.example"),
		secretRec("P_GARBAGE", store.SecretScopeProject, "not a url"),
		secretRec("P_HTTP", store.SecretScopeProject, "http://hooks.example.com/x"),
	), nil)
	ctx := context.Background()
	msg := func(name string) string {
		_, err := c.LookupURLSecret(ctx, "acme/app", name)
		if err == nil {
			t.Fatalf("%s: used", name)
		}
		return strings.ReplaceAll(err.Error(), name, "NAME")
	}
	// Server secrets without allowed hosts: refused for their scope alone.
	for _, n := range []string{"SRV_GARBAGE", "SRV_URL", "SRV_HTTP"} {
		if m := msg(n); !strings.Contains(m, "server secret") || m != msg("SRV_GARBAGE") {
			t.Fatalf("%s: %s", n, m)
		}
	}
	// With allowed hosts, or a project's own: one answer for any bad value.
	want := msg("H_GARBAGE")
	for _, n := range []string{"H_URL", "H_HTTP", "P_GARBAGE", "P_HTTP"} {
		if m := msg(n); m != want {
			t.Fatalf("%s: %q, want %q", n, m, want)
		}
	}
}

// N4: a team secret is used by a project's own objects only toward its
// allowed hosts, like a server secret; built-in provider routes use it
// anywhere in the team.
func TestTeamSecretNeedsAllowedHosts(t *testing.T) {
	c := New(recSecrets(
		secretRec("TEAM_OPEN", store.SecretScopeTeam, "t1"),
		secretRec("TEAM_HOSTS", store.SecretScopeTeam, "t2", "api.example.com"),
		secretRec("PROJ", store.SecretScopeProject, "p1"),
	), nil)
	ctx := context.Background()
	if _, err := c.LookupSecret(ctx, "acme/app", "TEAM_OPEN", "https://attacker.example/v1"); err == nil || !strings.Contains(err.Error(), "team secret") {
		t.Fatalf("team secret without allowed hosts: %v", err)
	}
	if v, err := c.LookupSecret(ctx, "acme/app", "TEAM_HOSTS", "https://api.example.com/v1"); err != nil || v != "t2" {
		t.Fatalf("team secret to its allowed host: %q %v", v, err)
	}
	if _, err := c.LookupSecret(ctx, "acme/app", "TEAM_HOSTS", "https://attacker.example/v1"); err == nil {
		t.Fatal("team secret sent to another host")
	}
	if v, _, err := c.UseSecret(ctx, SecretUse{ProjectID: "acme/app", Name: "TEAM_OPEN", Dest: "https://api.openai.com/v1/", Builtin: true}); err != nil || v != "t1" {
		t.Fatalf("built-in route with a team secret: %q %v", v, err)
	}
	if v, err := c.LookupSecret(ctx, "acme/app", "PROJ", "https://anywhere.example/"); err != nil || v != "p1" {
		t.Fatalf("project secret: %q %v", v, err)
	}
}
