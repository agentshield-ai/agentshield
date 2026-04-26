// Package enrich provides field-derivation helpers that run on every
// evaluation request regardless of entry point (HTTP, in-process, replay).
// Keeping enrichment here rather than in the HTTP server ensures library
// callers and the replay tool see the same url.* fields the rules depend on.
package enrich

import (
	"net"
	"net/url"
	"regexp"
	"strings"
)

// urlPattern matches the first http(s)/file URL in a string. Quote chars and
// shell-pipeline-terminator punctuation are excluded from the character class
// so the URL doesn't capture surrounding context. `]` is intentionally NOT
// excluded so IPv6 bracketed hosts (`http://[::1]:80/`) match correctly.
var urlPattern = regexp.MustCompile(`(?i)(https?|file)://[^\s'"<>` + "`" + `\)|]+`)

// internalMetadataHosts are well-known cloud metadata service hostnames that
// resolve to link-local addresses but are referenced by name in tool calls.
// Matched without DNS resolution so the eval hot path stays predictable.
var internalMetadataHosts = map[string]struct{}{
	"metadata.google.internal":   {},
	"metadata.azure.com":         {},
	"instance-data.ec2.internal": {},
}

// URLFields scans args for the first URL-shaped substring and injects
// structured url.* fields into fields for Sigma rule matching. Skipped
// silently when no URL is found or when fields already carries url.host
// (so callers that pre-enriched aren't overwritten).
func URLFields(args, fields map[string]string) {
	if fields == nil {
		return
	}
	if _, exists := fields["url.host"]; exists {
		return
	}

	raw := findFirstURL(args)
	if raw == "" {
		return
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return
	}
	if u.Scheme != "file" && u.Host == "" {
		return
	}

	host := strings.ToLower(u.Hostname())
	port := u.Port()
	scheme := strings.ToLower(u.Scheme)
	path := u.Path
	if path == "" {
		path = "/"
	}

	loopback, private, linkLocal := classifyHost(host)

	fields["url.scheme"] = scheme
	fields["url.host"] = host
	fields["url.port"] = port
	fields["url.path"] = path
	fields["url.is_loopback"] = boolStr(loopback)
	fields["url.is_private_ip"] = boolStr(private)
	fields["url.is_link_local"] = boolStr(linkLocal)
}

// findFirstURL returns the first URL substring found across args. Common
// args are checked in priority order so the most-likely-to-contain-a-URL
// keys are scanned first; the rest are scanned in iteration order as a
// fallback. Trailing punctuation common in shell pipelines is trimmed.
func findFirstURL(args map[string]string) string {
	for _, key := range []string{"url", "command", "endpoint", "uri"} {
		if v, ok := args[key]; ok {
			if m := urlPattern.FindString(v); m != "" {
				return strings.TrimRight(m, ".,;")
			}
		}
	}
	for k, v := range args {
		switch k {
		case "url", "command", "endpoint", "uri":
			continue
		}
		if m := urlPattern.FindString(v); m != "" {
			return strings.TrimRight(m, ".,;")
		}
	}
	return ""
}

// classifyHost returns (isLoopback, isPrivate, isLinkLocal) for a hostname or
// IP literal. Hostnames are matched against a small internal-metadata list
// and the literal "localhost"; IP literals use net.ParseIP.
func classifyHost(host string) (loopback, private, linkLocal bool) {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true, false, false
	}
	if _, ok := internalMetadataHosts[host]; ok {
		return false, false, true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false, false, false
	}
	return ip.IsLoopback(), ip.IsPrivate(), ip.IsLinkLocalUnicast()
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
