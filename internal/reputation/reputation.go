// Package reputation provides privacy-preserving reputation lookups using
// k-anonymity. Only a hash prefix is sent to the reputation service; the
// full hash is checked locally against the returned set. This follows the
// same model as HaveIBeenPwned's range API.
package reputation

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultPrefixLen is the default number of hex characters sent as a
	// prefix (5 hex chars = 20 bits of the 256-bit SHA-256 hash).
	DefaultPrefixLen = 5

	// DefaultTimeout is the default context timeout for a single lookup.
	DefaultTimeout = 3 * time.Second
)

// ReputationResult holds the outcome of a reputation check.
type ReputationResult struct {
	Matched  bool   `json:"matched"`            // true if the full hash was found
	Severity string `json:"severity,omitempty"`  // severity from the reputation DB
	Hash     string `json:"hash"`               // full SHA-256 hex digest that was checked
}

// Checker performs privacy-preserving reputation lookups.
type Checker struct {
	provider  Provider
	prefixLen int
	timeout   time.Duration
}

// Option configures a Checker via the functional options pattern.
type Option func(*Checker)

// WithPrefixLen sets the number of hex characters used as the hash prefix.
func WithPrefixLen(n int) Option {
	return func(c *Checker) {
		if n >= 2 && n <= 16 {
			c.prefixLen = n
		}
	}
}

// WithTimeout sets the per-lookup timeout.
func WithTimeout(d time.Duration) Option {
	return func(c *Checker) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// NewChecker creates a new reputation Checker with the given provider.
func NewChecker(provider Provider, opts ...Option) *Checker {
	c := &Checker{
		provider:  provider,
		prefixLen: DefaultPrefixLen,
		timeout:   DefaultTimeout,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// CheckURL normalises the given URL, hashes it with SHA-256, and performs
// a k-anonymity prefix lookup against the configured provider.
func (c *Checker) CheckURL(ctx context.Context, rawURL string) (*ReputationResult, error) {
	normalised := normaliseURL(rawURL)
	if normalised == "" {
		return nil, fmt.Errorf("invalid or empty URL")
	}
	return c.checkHash(ctx, hashString(normalised))
}

// CheckFile hashes the given file path and performs a k-anonymity prefix
// lookup against the configured provider.
func (c *Checker) CheckFile(ctx context.Context, filePath string) (*ReputationResult, error) {
	if filePath == "" {
		return nil, fmt.Errorf("empty file path")
	}
	return c.checkHash(ctx, hashString(filePath))
}

// checkHash performs the k-anonymity lookup: send only the prefix, then
// check the returned suffixes locally for a full match.
func (c *Checker) checkHash(ctx context.Context, fullHash string) (*ReputationResult, error) {
	if len(fullHash) < c.prefixLen {
		return nil, fmt.Errorf("hash too short for configured prefix length")
	}

	prefix := fullHash[:c.prefixLen]
	suffix := fullHash[c.prefixLen:]

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	matches, err := c.provider.LookupPrefix(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("provider lookup failed: %w", err)
	}

	// Check locally: does any returned suffix match ours?
	for _, m := range matches {
		if strings.EqualFold(m.Suffix, suffix) {
			slog.Info("Reputation match found",
				"provider", c.provider.Name(),
				"severity", m.Severity,
			)
			return &ReputationResult{
				Matched:  true,
				Severity: m.Severity,
				Hash:     fullHash,
			}, nil
		}
	}

	return &ReputationResult{
		Matched: false,
		Hash:    fullHash,
	}, nil
}

// hashString returns the lowercase hex-encoded SHA-256 digest of s.
func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h)
}

// normaliseURL canonicalises a URL for consistent hashing:
//   - lowercases scheme and host
//   - removes fragment
//   - removes common tracking query parameters
//   - removes trailing slash from path (unless path is just "/")
//   - sorts remaining query parameters
func normaliseURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}

	// Must have a scheme and host.
	if parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}

	// Lowercase scheme and host.
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)

	// Remove fragment.
	parsed.Fragment = ""

	// Remove common tracking parameters.
	trackingParams := map[string]struct{}{
		"utm_source":   {},
		"utm_medium":   {},
		"utm_campaign": {},
		"utm_term":     {},
		"utm_content":  {},
		"fbclid":       {},
		"gclid":        {},
		"ref":          {},
	}

	q := parsed.Query()
	for param := range trackingParams {
		q.Del(param)
	}
	parsed.RawQuery = q.Encode() // Encode sorts keys alphabetically.

	// Remove trailing slash (but keep "/" as the root path).
	if len(parsed.Path) > 1 {
		parsed.Path = strings.TrimRight(parsed.Path, "/")
	}

	return parsed.String()
}

// ExtractURLs scans a map of string key-value pairs for values that look
// like URLs (http:// or https://). This is used by the evaluation pipeline
// to find URLs in tool call arguments.
func ExtractURLs(fields map[string]string) []string {
	var urls []string
	seen := make(map[string]struct{})
	for _, v := range fields {
		for _, candidate := range extractURLCandidates(v) {
			if _, ok := seen[candidate]; !ok {
				seen[candidate] = struct{}{}
				urls = append(urls, candidate)
			}
		}
	}
	return urls
}

// extractURLCandidates finds substrings that look like HTTP(S) URLs.
func extractURLCandidates(s string) []string {
	var results []string
	remaining := s
	for {
		idx := strings.Index(remaining, "http://")
		idxs := strings.Index(remaining, "https://")

		// Find the earliest match.
		start := -1
		if idx >= 0 && (idxs < 0 || idx < idxs) {
			start = idx
		} else if idxs >= 0 {
			start = idxs
		}
		if start < 0 {
			break
		}

		// Extract until whitespace, quote, or end of string.
		end := start
		for end < len(remaining) {
			ch := remaining[end]
			if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' ||
				ch == '"' || ch == '\'' || ch == '>' || ch == ')' {
				break
			}
			end++
		}

		candidate := remaining[start:end]
		if parsed, err := url.Parse(candidate); err == nil && parsed.Host != "" {
			results = append(results, candidate)
		}
		remaining = remaining[end:]
	}
	return results
}
