package reputation

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// PrefixMatch represents a single hash suffix and its associated severity
// returned by a reputation provider.
type PrefixMatch struct {
	Suffix   string // hex-encoded hash suffix (everything after the prefix)
	Severity string // severity level: "low", "medium", "high", "critical"
}

// Provider is the interface for pluggable reputation backends.
type Provider interface {
	// LookupPrefix queries the backend for all known hashes sharing the
	// given hex prefix. The prefix length is determined by the Checker.
	LookupPrefix(ctx context.Context, hashPrefix string) ([]PrefixMatch, error)

	// Name returns a human-readable name for this provider (used in logging).
	Name() string
}

// HTTPProvider implements Provider using a remote HTTP API.
// The API contract follows the k-anonymity model: GET {endpoint}/range/{prefix}
// returns newline-separated "suffix:severity" pairs.
type HTTPProvider struct {
	client   *http.Client
	endpoint string // base URL without trailing slash
}

// NewHTTPProvider creates a Provider backed by a remote HTTP reputation API.
func NewHTTPProvider(endpoint string, timeout time.Duration) *HTTPProvider {
	return &HTTPProvider{
		client: &http.Client{
			Timeout: timeout,
		},
		endpoint: strings.TrimRight(endpoint, "/"),
	}
}

// Name returns the provider name.
func (p *HTTPProvider) Name() string {
	return "http"
}

// LookupPrefix queries the remote API for hash prefix matches.
func (p *HTTPProvider) LookupPrefix(ctx context.Context, hashPrefix string) ([]PrefixMatch, error) {
	url := fmt.Sprintf("%s/range/%s", p.endpoint, hashPrefix)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", "AgentShield/1.0")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Limit response body to 10 MB to prevent memory exhaustion.
	limited := io.LimitReader(resp.Body, 10*1024*1024)
	return parsePrefixResponse(limited)
}

// parsePrefixResponse parses newline-separated "suffix:severity" pairs.
func parsePrefixResponse(r io.Reader) ([]PrefixMatch, error) {
	var matches []PrefixMatch
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue // skip malformed lines
		}
		matches = append(matches, PrefixMatch{
			Suffix:   strings.TrimSpace(parts[0]),
			Severity: strings.TrimSpace(parts[1]),
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	return matches, nil
}

// LocalProvider implements Provider using a local sorted hash database file.
// The file format is one "hash:severity" entry per line, sorted by hash.
// This is useful for offline or air-gapped deployments.
type LocalProvider struct {
	mu      sync.RWMutex
	entries []localEntry // sorted by hash
	name    string
}

type localEntry struct {
	hash     string
	severity string
}

// NewLocalProvider loads a local hash database from the given file path.
// The file must contain one "hash:severity" line per entry, sorted by hash.
func NewLocalProvider(dbPath string) (*LocalProvider, error) {
	f, err := os.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening local DB: %w", err)
	}
	defer f.Close()

	var entries []localEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		entries = append(entries, localEntry{
			hash:     strings.ToLower(strings.TrimSpace(parts[0])),
			severity: strings.TrimSpace(parts[1]),
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading local DB: %w", err)
	}

	// Ensure sorted for binary search.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].hash < entries[j].hash
	})

	return &LocalProvider{
		entries: entries,
		name:    "local",
	}, nil
}

// Name returns the provider name.
func (p *LocalProvider) Name() string {
	return p.name
}

// LookupPrefix returns all entries whose hash starts with the given prefix.
// Uses binary search to find the first matching entry, then scans forward.
func (p *LocalProvider) LookupPrefix(_ context.Context, hashPrefix string) ([]PrefixMatch, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	prefix := strings.ToLower(hashPrefix)
	prefixLen := len(prefix)

	// Binary search: find the first entry >= prefix.
	i := sort.Search(len(p.entries), func(i int) bool {
		h := p.entries[i].hash
		if len(h) < prefixLen {
			return h >= prefix
		}
		return h[:prefixLen] >= prefix
	})

	var matches []PrefixMatch
	for ; i < len(p.entries); i++ {
		e := p.entries[i]
		if len(e.hash) < prefixLen || e.hash[:prefixLen] != prefix {
			break
		}
		// Return the suffix (everything after the prefix).
		matches = append(matches, PrefixMatch{
			Suffix:   e.hash[prefixLen:],
			Severity: e.severity,
		})
	}
	return matches, nil
}
