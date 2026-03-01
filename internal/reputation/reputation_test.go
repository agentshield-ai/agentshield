package reputation

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- URL normalisation tests ---

func TestNormaliseURL_LowercasesSchemeAndHost(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"HTTP://Example.COM/path", "http://example.com/path"},
		{"HTTPS://FOO.BAR.COM/", "https://foo.bar.com/"},
	}
	for _, tt := range tests {
		got := normaliseURL(tt.input)
		if got != tt.want {
			t.Errorf("normaliseURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNormaliseURL_RemovesFragment(t *testing.T) {
	got := normaliseURL("https://example.com/page#section")
	if strings.Contains(got, "#") {
		t.Errorf("expected fragment removed, got %q", got)
	}
}

func TestNormaliseURL_RemovesTrailingSlash(t *testing.T) {
	got := normaliseURL("https://example.com/path/")
	if strings.HasSuffix(got, "/path/") {
		t.Errorf("expected trailing slash removed, got %q", got)
	}

	// Root path "/" should be preserved.
	root := normaliseURL("https://example.com/")
	if !strings.HasSuffix(root, "/") {
		t.Errorf("expected root slash preserved, got %q", root)
	}
}

func TestNormaliseURL_RemovesTrackingParams(t *testing.T) {
	got := normaliseURL("https://example.com/page?utm_source=twitter&utm_medium=social&valid=1")
	if strings.Contains(got, "utm_source") || strings.Contains(got, "utm_medium") {
		t.Errorf("expected tracking params removed, got %q", got)
	}
	if !strings.Contains(got, "valid=1") {
		t.Errorf("expected non-tracking param preserved, got %q", got)
	}
}

func TestNormaliseURL_SortsQueryParams(t *testing.T) {
	got := normaliseURL("https://example.com/page?z=1&a=2")
	// url.Values.Encode() sorts alphabetically.
	if !strings.Contains(got, "a=2&z=1") {
		t.Errorf("expected sorted query params, got %q", got)
	}
}

func TestNormaliseURL_InvalidInput(t *testing.T) {
	tests := []string{
		"",
		"not-a-url",
		"://missing-scheme",
		"http://", // empty host after parse
	}
	for _, input := range tests {
		got := normaliseURL(input)
		if got != "" {
			t.Errorf("normaliseURL(%q) = %q, want empty string", input, got)
		}
	}
}

func TestNormaliseURL_HTTPvsHTTPS(t *testing.T) {
	httpURL := normaliseURL("http://example.com/path")
	httpsURL := normaliseURL("https://example.com/path")

	if httpURL == httpsURL {
		t.Errorf("http and https URLs should normalise differently: %q vs %q", httpURL, httpsURL)
	}
	if !strings.HasPrefix(httpURL, "http://") {
		t.Errorf("expected http:// prefix, got %q", httpURL)
	}
	if !strings.HasPrefix(httpsURL, "https://") {
		t.Errorf("expected https:// prefix, got %q", httpsURL)
	}
}

// --- Hash prefix generation tests ---

func TestHashString_Deterministic(t *testing.T) {
	h1 := hashString("https://example.com")
	h2 := hashString("https://example.com")
	if h1 != h2 {
		t.Errorf("hashString not deterministic: %q != %q", h1, h2)
	}
	// SHA-256 produces 64 hex characters.
	if len(h1) != 64 {
		t.Errorf("expected 64 hex chars, got %d", len(h1))
	}
}

func TestHashString_DifferentInputs(t *testing.T) {
	h1 := hashString("https://example.com")
	h2 := hashString("https://example.org")
	if h1 == h2 {
		t.Error("different inputs should produce different hashes")
	}
}

func TestHashPrefix_DefaultLength(t *testing.T) {
	hash := hashString("test")
	prefix := hash[:DefaultPrefixLen]
	if len(prefix) != DefaultPrefixLen {
		t.Errorf("expected prefix length %d, got %d", DefaultPrefixLen, len(prefix))
	}
}

// --- k-anonymity matching tests ---

func TestCheckHash_MatchFound(t *testing.T) {
	fullHash := hashString("https://malicious.example.com")
	prefix := fullHash[:DefaultPrefixLen]
	suffix := fullHash[DefaultPrefixLen:]

	provider := &mockProvider{
		matches: []PrefixMatch{
			{Suffix: "decoy1decoy1decoy1decoy1decoy1decoy1decoy1decoy1decoy1decoy", Severity: "low"},
			{Suffix: suffix, Severity: "critical"},
			{Suffix: "decoy2decoy2decoy2decoy2decoy2decoy2decoy2decoy2decoy2decoy", Severity: "medium"},
		},
		wantPrefix: prefix,
	}

	checker := NewChecker(provider)
	result, err := checker.CheckURL(context.Background(), "https://malicious.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Matched {
		t.Error("expected match, got no match")
	}
	if result.Severity != "critical" {
		t.Errorf("expected severity 'critical', got %q", result.Severity)
	}
	if result.Hash != fullHash {
		t.Errorf("expected hash %q, got %q", fullHash, result.Hash)
	}
}

func TestCheckHash_PrefixMatchesButFullHashDoesNot(t *testing.T) {
	// This is the core k-anonymity property: even though the prefix matches,
	// the full hash does not, so no match should be reported.
	provider := &mockProvider{
		matches: []PrefixMatch{
			{Suffix: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa0", Severity: "high"},
			{Suffix: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Severity: "critical"},
		},
	}

	checker := NewChecker(provider)
	result, err := checker.CheckURL(context.Background(), "https://safe.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Matched {
		t.Error("expected no match (prefix collides but full hash differs)")
	}
}

func TestCheckHash_EmptyProviderResponse(t *testing.T) {
	provider := &mockProvider{matches: nil}

	checker := NewChecker(provider)
	result, err := checker.CheckURL(context.Background(), "https://unknown.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Matched {
		t.Error("expected no match for empty provider response")
	}
}

func TestCheckFile_Works(t *testing.T) {
	path := "/etc/passwd"
	fullHash := hashString(path)
	suffix := fullHash[DefaultPrefixLen:]

	provider := &mockProvider{
		matches: []PrefixMatch{
			{Suffix: suffix, Severity: "high"},
		},
	}

	checker := NewChecker(provider)
	result, err := checker.CheckFile(context.Background(), path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Matched {
		t.Error("expected match for known file path")
	}
	if result.Severity != "high" {
		t.Errorf("expected severity 'high', got %q", result.Severity)
	}
}

func TestCheckURL_EmptyURL(t *testing.T) {
	checker := NewChecker(&mockProvider{})
	_, err := checker.CheckURL(context.Background(), "")
	if err == nil {
		t.Error("expected error for empty URL")
	}
}

func TestCheckFile_EmptyPath(t *testing.T) {
	checker := NewChecker(&mockProvider{})
	_, err := checker.CheckFile(context.Background(), "")
	if err == nil {
		t.Error("expected error for empty file path")
	}
}

// --- HTTP provider tests ---

func TestHTTPProvider_Success(t *testing.T) {
	fullHash := hashString("https://malicious.test")
	prefix := fullHash[:DefaultPrefixLen]
	suffix := fullHash[DefaultPrefixLen:]

	body := fmt.Sprintf("decoyaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa:low\n%s:critical\n", suffix)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedPath := "/range/" + prefix
		if r.URL.Path != expectedPath {
			t.Errorf("expected path %q, got %q", expectedPath, r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Header.Get("User-Agent") != "AgentShield/1.0" {
			t.Errorf("expected User-Agent 'AgentShield/1.0', got %q", r.Header.Get("User-Agent"))
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	provider := NewHTTPProvider(srv.URL, 5*time.Second)
	checker := NewChecker(provider)

	result, err := checker.CheckURL(context.Background(), "https://malicious.test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Matched {
		t.Error("expected match")
	}
	if result.Severity != "critical" {
		t.Errorf("expected severity 'critical', got %q", result.Severity)
	}
}

func TestHTTPProvider_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	provider := NewHTTPProvider(srv.URL, 5*time.Second)
	checker := NewChecker(provider)

	_, err := checker.CheckURL(context.Background(), "https://example.com")
	if err == nil {
		t.Error("expected error for server error response")
	}
}

func TestHTTPProvider_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	provider := NewHTTPProvider(srv.URL, 5*time.Second)
	checker := NewChecker(provider, WithTimeout(100*time.Millisecond))

	_, err := checker.CheckURL(context.Background(), "https://example.com")
	if err == nil {
		t.Error("expected error due to timeout")
	}
}

// --- Local provider tests ---

func TestLocalProvider_Match(t *testing.T) {
	hash := hashString("https://bad.example.com")

	dbContent := fmt.Sprintf(
		"0000000000000000000000000000000000000000000000000000000000000000:low\n%s:critical\nffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff:medium\n",
		hash,
	)

	dbPath := filepath.Join(t.TempDir(), "hashes.txt")
	if err := os.WriteFile(dbPath, []byte(dbContent), 0o644); err != nil {
		t.Fatalf("writing temp DB: %v", err)
	}

	provider, err := NewLocalProvider(dbPath)
	if err != nil {
		t.Fatalf("creating local provider: %v", err)
	}

	checker := NewChecker(provider)
	result, err := checker.CheckURL(context.Background(), "https://bad.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Matched {
		t.Error("expected match from local provider")
	}
	if result.Severity != "critical" {
		t.Errorf("expected severity 'critical', got %q", result.Severity)
	}
}

func TestLocalProvider_NoMatch(t *testing.T) {
	dbContent := "0000000000000000000000000000000000000000000000000000000000000000:low\nffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff:high\n"

	dbPath := filepath.Join(t.TempDir(), "hashes.txt")
	if err := os.WriteFile(dbPath, []byte(dbContent), 0o644); err != nil {
		t.Fatalf("writing temp DB: %v", err)
	}

	provider, err := NewLocalProvider(dbPath)
	if err != nil {
		t.Fatalf("creating local provider: %v", err)
	}

	checker := NewChecker(provider)
	result, err := checker.CheckURL(context.Background(), "https://unique-safe-url.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Matched {
		t.Error("expected no match from local provider")
	}
}

func TestLocalProvider_EmptyFile(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(dbPath, []byte(""), 0o644); err != nil {
		t.Fatalf("writing temp DB: %v", err)
	}

	provider, err := NewLocalProvider(dbPath)
	if err != nil {
		t.Fatalf("creating local provider: %v", err)
	}

	checker := NewChecker(provider)
	result, err := checker.CheckURL(context.Background(), "https://example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Matched {
		t.Error("expected no match from empty database")
	}
}

func TestLocalProvider_CommentsIgnored(t *testing.T) {
	dbContent := "# This is a comment\n0000000000000000000000000000000000000000000000000000000000000000:low\n"

	dbPath := filepath.Join(t.TempDir(), "with_comments.txt")
	if err := os.WriteFile(dbPath, []byte(dbContent), 0o644); err != nil {
		t.Fatalf("writing temp DB: %v", err)
	}

	provider, err := NewLocalProvider(dbPath)
	if err != nil {
		t.Fatalf("creating local provider: %v", err)
	}

	// The provider should have loaded 1 entry (not the comment).
	matches, err := provider.LookupPrefix(context.Background(), "00000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("expected 1 match, got %d", len(matches))
	}
}

func TestLocalProvider_FileNotFound(t *testing.T) {
	_, err := NewLocalProvider("/nonexistent/path/db.txt")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

// --- Options tests ---

func TestWithPrefixLen(t *testing.T) {
	checker := NewChecker(&mockProvider{}, WithPrefixLen(8))
	if checker.prefixLen != 8 {
		t.Errorf("expected prefixLen 8, got %d", checker.prefixLen)
	}
}

func TestWithPrefixLen_InvalidValues(t *testing.T) {
	// Too small - should keep default.
	checker := NewChecker(&mockProvider{}, WithPrefixLen(1))
	if checker.prefixLen != DefaultPrefixLen {
		t.Errorf("expected default prefixLen %d for invalid input, got %d", DefaultPrefixLen, checker.prefixLen)
	}

	// Too large - should keep default.
	checker = NewChecker(&mockProvider{}, WithPrefixLen(20))
	if checker.prefixLen != DefaultPrefixLen {
		t.Errorf("expected default prefixLen %d for invalid input, got %d", DefaultPrefixLen, checker.prefixLen)
	}
}

func TestWithTimeout(t *testing.T) {
	checker := NewChecker(&mockProvider{}, WithTimeout(10*time.Second))
	if checker.timeout != 10*time.Second {
		t.Errorf("expected timeout 10s, got %v", checker.timeout)
	}
}

// --- URL extraction tests ---

func TestExtractURLs_FindsHTTPAndHTTPS(t *testing.T) {
	fields := map[string]string{
		"url":     "https://example.com/page",
		"command": "curl http://other.com/api",
		"safe":    "no urls here",
	}

	urls := ExtractURLs(fields)
	if len(urls) != 2 {
		t.Errorf("expected 2 URLs, got %d: %v", len(urls), urls)
	}
}

func TestExtractURLs_Deduplicates(t *testing.T) {
	fields := map[string]string{
		"url1": "https://example.com",
		"url2": "https://example.com",
	}

	urls := ExtractURLs(fields)
	if len(urls) != 1 {
		t.Errorf("expected 1 URL after dedup, got %d: %v", len(urls), urls)
	}
}

func TestExtractURLs_EmptyFields(t *testing.T) {
	urls := ExtractURLs(map[string]string{})
	if len(urls) != 0 {
		t.Errorf("expected 0 URLs, got %d", len(urls))
	}
}

func TestExtractURLs_URLInMiddleOfText(t *testing.T) {
	fields := map[string]string{
		"arg": "please visit https://example.com/path and then continue",
	}

	urls := ExtractURLs(fields)
	if len(urls) != 1 {
		t.Fatalf("expected 1 URL, got %d: %v", len(urls), urls)
	}
	if urls[0] != "https://example.com/path" {
		t.Errorf("expected 'https://example.com/path', got %q", urls[0])
	}
}

// --- Mock provider ---

type mockProvider struct {
	matches    []PrefixMatch
	wantPrefix string
	err        error
}

func (m *mockProvider) LookupPrefix(_ context.Context, hashPrefix string) ([]PrefixMatch, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.wantPrefix != "" && hashPrefix != m.wantPrefix {
		return nil, fmt.Errorf("unexpected prefix: got %q, want %q", hashPrefix, m.wantPrefix)
	}
	return m.matches, nil
}

func (m *mockProvider) Name() string {
	return "mock"
}
