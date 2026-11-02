package evals

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/egress"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// OAuth 2.0 client-credentials tokens for RemoteScorer:
//
//	"auth_config": {"token_url": "https://idp/oauth/token", "client_id": "weft",
//	                "client_secret_secret": "SCORER_CLIENT_SECRET", "scopes": ["score"],
//	                "audience": "https://scorer" (optional)}
//
// The client secret is read from the named stored secret (or environment
// variable). Tokens are cached per token URL, client, secret and scopes and
// fetched again shortly before they expire.

var (
	oauthMu      sync.Mutex
	oauthSources = map[string]oauth2.TokenSource{}
	// oauthHTTP fetches tokens through the egress guard; tests may replace it.
	oauthHTTP = egress.NewClient(30*time.Second, false)
)

// oauthToken returns a bearer token for auth_config, or "" when it has no token_url.
func (e *Engine) oauthToken(ctx context.Context, ac map[string]any) (string, error) {
	tokenURL := strField(ac, "token_url")
	if tokenURL == "" {
		return "", nil
	}
	u, err := url.Parse(tokenURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("auth_config.token_url must be an http(s) URL")
	}
	clientID := strField(ac, "client_id")
	secretName := strField(ac, "client_secret_secret", "client_secret_name")
	if clientID == "" || secretName == "" {
		return "", fmt.Errorf("auth_config needs client_id and client_secret_secret (the name of a stored secret)")
	}
	secret, ok := e.LLM.LookupSecret(ctx, secretName)
	if !ok {
		return "", fmt.Errorf("secret %q is not set", secretName)
	}
	var scopes []string
	switch v := ac["scopes"].(type) {
	case string:
		scopes = strings.Fields(v)
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok && s != "" {
				scopes = append(scopes, s)
			}
		}
	}
	sort.Strings(scopes)
	params := url.Values{}
	if aud := strField(ac, "audience"); aud != "" {
		params.Set("audience", aud)
	}
	h := sha256.Sum256([]byte(strings.Join([]string{tokenURL, clientID, secret, strings.Join(scopes, " "), params.Encode()}, "\x00")))
	key := hex.EncodeToString(h[:])

	oauthMu.Lock()
	ts, ok := oauthSources[key]
	if !ok {
		cfg := clientcredentials.Config{ClientID: clientID, ClientSecret: secret, TokenURL: tokenURL, Scopes: scopes, EndpointParams: params}
		// The source outlives this request, so it gets its own context.
		ts = oauth2.ReuseTokenSourceWithExpiry(nil, cfg.TokenSource(context.WithValue(context.Background(), oauth2.HTTPClient, oauthHTTP)), 30*time.Second)
		oauthSources[key] = ts
	}
	oauthMu.Unlock()

	type result struct {
		tok *oauth2.Token
		err error
	}
	ch := make(chan result, 1)
	go func() {
		t, err := ts.Token()
		ch <- result{t, err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-ch:
		if r.err != nil {
			// Do not cache failures (a fixed secret should work on the next call).
			oauthMu.Lock()
			delete(oauthSources, key)
			oauthMu.Unlock()
			if egress.IsBlocked(r.err) {
				return "", fmt.Errorf("fetching OAuth token: %w", egress.ErrBlocked)
			}
			return "", fmt.Errorf("fetching OAuth token from %s: %s", u.Host, oauthErrSummary(r.err))
		}
		return r.tok.AccessToken, nil
	}
}

// oauthErrSummary keeps error text short and free of request bodies.
func oauthErrSummary(err error) string {
	if re, ok := err.(*oauth2.RetrieveError); ok {
		if re.ErrorCode != "" {
			return fmt.Sprintf("%d %s", re.Response.StatusCode, re.ErrorCode)
		}
		return re.Response.Status
	}
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}
