// Package triage provides LLM-powered alert triage and analysis.
// This handles threat analysis, false positive reduction, and alert prioritization.
package triage

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/agentshield-ai/agentshield/internal/store"
	"github.com/hashicorp/go-retryablehttp"
)

var (
	// Compile regexes once at package level for better performance
	controlCharsRegex = regexp.MustCompile(`[\x00-\x1F\x7F]`)
	injectionRegex    = regexp.MustCompile(`(?i)(ignore|forget|system|prompt|instruction)[\s]*[:=]`)
)

// TriageResult represents the result of LLM triage analysis.
//
// Risk and authorization are modelled as two independent axes, mirroring
// OpenAI Codex guardian mode: `risk_level` is the intrinsic risk of the action
// and `user_authorization` is how clearly the user approved the exact action.
// The derived `verdict` follows from the policy mapping of those two axes.
type TriageResult struct {
	RiskLevel         RiskLevel         `json:"risk_level"`         // intrinsic risk of the action
	UserAuthorization UserAuthorization `json:"user_authorization"` // how clearly the user authorized it
	Verdict           string            `json:"verdict"`            // "block", "allow", "investigate"
	Confidence        float64           `json:"confidence"`         // 0.0-1.0
	Reasoning         string            `json:"reasoning"`          // Explanation (alias of rationale)
	SuggestedAction   string            `json:"suggested_action"`   // Recommended next steps
	Provider          string            `json:"provider"`           // Which LLM provider was used
	Model             string            `json:"model"`              // Which model was used
	ProcessingTime    int64             `json:"processing_time"`    // Time in milliseconds
}

// CorrelationSummary contains deterministic correlation metadata used by triage.
type CorrelationSummary struct {
	Score                  float64  `json:"score"`
	Factors                []string `json:"factors"`
	RecentCount            int      `json:"recent_count"`
	WindowSec              int      `json:"window_sec"`
	EscalatedByCorrelation bool     `json:"escalated_by_correlation"`
}

// TriageContext provides context for triage analysis
type TriageContext struct {
	Alert        engine.RuleResult         `json:"alert"`
	Request      *models.EvaluationRequest `json:"request"`
	RecentAlerts []store.Alert             `json:"recent_alerts"`
	Correlation  CorrelationSummary        `json:"correlation"`
	// PolicyPath is an optional path to a tenant-specific policy markdown file
	// that overrides internal/triage/policy.md. Empty means use the embedded default.
	PolicyPath string `json:"-"`
}

// Provider interface for LLM triage providers
type Provider interface {
	Triage(ctx context.Context, triageCtx *TriageContext) (*TriageResult, error)
	Name() string
	HealthCheck(ctx context.Context) error
}

// Triager handles the triage process
type Triager struct {
	config   *config.TriageConfig
	provider Provider
	store    *store.Store
}

// NewTriager creates a new triager instance
func NewTriager(cfg *config.TriageConfig, store *store.Store) (*Triager, error) {
	if !cfg.Enabled {
		return nil, nil // Triage disabled
	}

	// Create provider based on config
	var provider Provider
	var err error

	switch cfg.Provider {
	case "openai":
		provider, err = NewOpenAIProvider(cfg)
	case "anthropic":
		provider, err = NewAnthropicProvider(cfg)
	default:
		return nil, fmt.Errorf("unsupported triage provider: %s (supported: openai, anthropic)", cfg.Provider)
	}

	if err != nil {
		return nil, fmt.Errorf("creating triage provider: %w", err)
	}

	return &Triager{
		config:   cfg,
		provider: provider,
		store:    store,
	}, nil
}

// ShouldTriage determines if an alert should be triaged
func (t *Triager) ShouldTriage(alert engine.RuleResult) bool {
	if t == nil || !t.config.Enabled {
		return false
	}

	// Only triage high and critical severity alerts by default
	return alert.Severity == engine.SeverityHigh || alert.Severity == engine.SeverityCritical
}

// TriageAlerts performs triage analysis on a set of alerts
func (t *Triager) TriageAlerts(ctx context.Context, alerts []engine.RuleResult, req *models.EvaluationRequest) ([]TriageResult, error) {
	if t == nil || !t.config.Enabled {
		return nil, nil
	}

	var results []TriageResult

	// Get recent context from store (time-windowed + bounded count)
	corr := effectiveCorrelationConfig(t.config.Correlation)

	since := time.Now().Add(-time.Duration(corr.WindowSec) * time.Second)
	recentQuery := &store.AlertQuery{
		Since: &since,
		Limit: corr.MaxAlerts,
	}
	if corr.RequireSameSession {
		recentQuery.SessionID = req.SessionID
	}

	recentAlerts, err := t.store.QueryAlerts(recentQuery)
	if err != nil {
		// Log error but don't fail triage
		recentAlerts = nil
	}

	// Process each alert that should be triaged
	for _, alert := range alerts {
		if !t.ShouldTriage(alert) {
			continue
		}

		filteredRecent := filterRecentAlertsForCurrent(corr, req, recentAlerts)
		correlation := scoreCorrelation(corr, alert, req, filteredRecent)

		triageCtx := &TriageContext{
			Alert:        alert,
			Request:      req,
			RecentAlerts: filteredRecent,
			Correlation:  correlation,
			PolicyPath:   t.config.PolicyPath,
		}

		result, err := t.provider.Triage(ctx, triageCtx)
		if err != nil {
			// For triage failures, return a fallback result based on alert severity.
			// RiskLevel mirrors rule severity (our best available signal when the
			// LLM is unreachable) and UserAuthorization is marked unknown because
			// we have no evidence we can trust without the reviewer.
			fallbackResult := &TriageResult{
				RiskLevel:         severityToRiskLevel(alert.Severity),
				UserAuthorization: UserAuthorizationUnknown,
				Verdict:           t.getFallbackVerdict(alert.Severity),
				Confidence:        0.5,
				Reasoning:         fmt.Sprintf("Triage failed: %v. Falling back to rule-only verdict.", err),
				SuggestedAction:   "Review manually due to triage failure",
				Provider:          t.provider.Name(),
				Model:             t.config.Model,
				ProcessingTime:    0,
			}
			results = append(results, *fallbackResult)
			continue
		}

		results = append(results, *result)
	}

	return results, nil
}

// severityToRiskLevel maps engine rule severity onto triage risk level. Used
// when the LLM reviewer is unreachable and we have to fall back to the rule
// severity as our only risk signal.
func severityToRiskLevel(severity engine.AlertSeverity) RiskLevel {
	switch severity {
	case engine.SeverityCritical:
		return RiskLevelCritical
	case engine.SeverityHigh:
		return RiskLevelHigh
	case engine.SeverityMedium:
		return RiskLevelMedium
	default:
		return RiskLevelLow
	}
}

// getFallbackVerdict returns a safe fallback verdict when triage fails
func (t *Triager) getFallbackVerdict(severity engine.AlertSeverity) string {
	switch severity {
	case engine.SeverityCritical:
		return "block" // Fail closed for critical
	case engine.SeverityHigh:
		return "block" // Fail closed for high
	case engine.SeverityMedium:
		return "allow" // Fail open for medium
	case engine.SeverityLow:
		return "allow" // Fail open for low
	default:
		return "block" // Default to safe side
	}
}

// HealthCheck checks if the triager is healthy
func (t *Triager) HealthCheck(ctx context.Context) error {
	if t == nil || !t.config.Enabled {
		return nil
	}

	return t.provider.HealthCheck(ctx)
}

// sanitizeInput sanitizes user input before including in LLM prompts
func sanitizeInput(input string) string {
	if input == "" {
		return ""
	}

	// Strip control characters
	sanitized := controlCharsRegex.ReplaceAllString(input, "")

	// Limit length to prevent prompt injection/memory issues
	const maxLength = 2000
	if len(sanitized) > maxLength {
		sanitized = sanitized[:maxLength] + "..."
	}

	// Remove any potential prompt injection patterns
	sanitized = injectionRegex.ReplaceAllString(sanitized, "[REDACTED]")

	return sanitized
}

// maskSensitiveData removes potential secrets from input
func maskSensitiveData(data map[string]string) map[string]string {
	if data == nil {
		return nil
	}

	masked := make(map[string]string)
	sensitiveKeys := []string{"api_key", "token", "password", "secret", "key", "credential"}

	for k, v := range data {
		key := strings.ToLower(k)
		shouldMask := false

		for _, sensitiveKey := range sensitiveKeys {
			if strings.Contains(key, sensitiveKey) {
				shouldMask = true
				break
			}
		}

		if shouldMask {
			masked[k] = "[REDACTED]"
		} else {
			masked[k] = sanitizeInput(v)
		}
	}

	return masked
}

func effectiveCorrelationConfig(c config.CorrelationConfig) config.CorrelationConfig {
	if c.WindowSec <= 0 {
		c.WindowSec = 900
	}
	if c.MaxAlerts <= 0 {
		c.MaxAlerts = 5
	}
	if c.TimeDecayHalfLifeSec <= 0 {
		c.TimeDecayHalfLifeSec = 300
	}
	if c.EscalateThreshold <= 0 {
		c.EscalateThreshold = 0.8
	}
	return c
}

func filterRecentAlertsForCurrent(c config.CorrelationConfig, req *models.EvaluationRequest, recent []store.Alert) []store.Alert {
	if len(recent) == 0 {
		return recent
	}
	if !c.RequireSameTool {
		return recent
	}
	if req == nil || req.Tool == "" {
		return recent
	}

	filtered := make([]store.Alert, 0, len(recent))
	for _, a := range recent {
		if a.Tool == req.Tool {
			filtered = append(filtered, a)
		}
	}
	return filtered
}

func scoreCorrelation(c config.CorrelationConfig, current engine.RuleResult, req *models.EvaluationRequest, recent []store.Alert) CorrelationSummary {
	if !c.Enabled {
		return CorrelationSummary{}
	}
	if req != nil && strings.EqualFold(req.Context, "test") {
		return CorrelationSummary{
			Score:                  0,
			Factors:                []string{"test_context"},
			RecentCount:            len(recent),
			WindowSec:              c.WindowSec,
			EscalatedByCorrelation: false,
		}
	}
	c = effectiveCorrelationConfig(c)

	now := time.Now()
	score := 0.0
	factors := make([]string, 0, 8)
	currentRule := strings.ToLower(current.RuleName)

	for _, a := range recent {
		ageSec := now.Sub(a.Timestamp).Seconds()
		if ageSec < 0 {
			ageSec = 0
		}
		decay := math.Pow(0.5, ageSec/float64(c.TimeDecayHalfLifeSec))

		switch strings.ToLower(a.Severity) {
		case "critical":
			score += c.WeightCritical * decay
			factors = append(factors, "recent_critical")
		case "high":
			score += c.WeightHigh * decay
			factors = append(factors, "recent_high")
		}

		if req != nil && req.Tool != "" && a.Tool == req.Tool {
			score += c.WeightRepeatBonus * decay
			factors = append(factors, "same_tool_repeat")
		}

		if looksLikeChain(strings.ToLower(a.RuleName), currentRule) {
			score += c.WeightChainBonus * decay
			factors = append(factors, "attack_chain_pattern")
		}
	}

	return CorrelationSummary{
		Score:                  score,
		Factors:                dedupeStrings(factors),
		RecentCount:            len(recent),
		WindowSec:              c.WindowSec,
		EscalatedByCorrelation: score >= c.EscalateThreshold,
	}
}

func looksLikeChain(prevRule, currentRule string) bool {
	if prevRule == "" || currentRule == "" {
		return false
	}
	if (strings.Contains(prevRule, "rce") || strings.Contains(prevRule, "execution")) && strings.Contains(currentRule, "persistence") {
		return true
	}
	if strings.Contains(prevRule, "persistence") && (strings.Contains(currentRule, "exfil") || strings.Contains(currentRule, "dns")) {
		return true
	}
	return false
}

func dedupeStrings(in []string) []string {
	if len(in) == 0 {
		return in
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// buildTriageEvidence builds the per-request evidence block that is paired
// with the policy system prompt (renderPolicySystemPrompt). It contains only
// the evidence the reviewer is expected to assess — the risk taxonomy,
// authorization scoring, and output contract live in the system prompt.
func buildTriageEvidence(triageCtx *TriageContext) string {
	ruleName := sanitizeInput(triageCtx.Alert.RuleName)
	description := sanitizeInput(triageCtx.Alert.Description)
	tool := sanitizeInput(triageCtx.Request.Tool)
	contextLabel := sanitizeInput(triageCtx.Request.Context)
	if contextLabel == "" {
		contextLabel = "prod"
	}

	args := maskSensitiveData(triageCtx.Request.Args)
	argsJSON, _ := json.Marshal(args)

	var recentContext strings.Builder
	if len(triageCtx.RecentAlerts) > 0 {
		recentContext.WriteString("Recent alerts (last 5):\n")
		for i, alert := range triageCtx.RecentAlerts {
			if i >= 5 {
				break
			}
			recentContext.WriteString(fmt.Sprintf("- %s (%s) at %s\n",
				sanitizeInput(alert.RuleName),
				sanitizeInput(alert.Severity),
				alert.Timestamp.Format("15:04:05")))
		}
	} else {
		recentContext.WriteString("No recent alerts in this session.")
	}

	return fmt.Sprintf(`>>> EVIDENCE START
Triggered rule: %s (severity: %s)
Rule description: %s
Tool called: %s
Execution context: %s
Arguments: %s
%s
Correlation score: %.2f
Correlation factors: %v
Correlation window: %ds, recent alerts: %d
>>> EVIDENCE END

Assess the intrinsic risk of the tool call above, score the user authorization based on the evidence, and emit the JSON verdict described in the system prompt.`,
		ruleName,
		string(triageCtx.Alert.Severity),
		description,
		tool,
		contextLabel,
		string(argsJSON),
		recentContext.String(),
		triageCtx.Correlation.Score,
		triageCtx.Correlation.Factors,
		triageCtx.Correlation.WindowSec,
		triageCtx.Correlation.RecentCount)
}

// buildTriagePrompt builds a single-string prompt embedding both the policy
// and the evidence. Providers that can send separate system + user messages
// should prefer renderPolicySystemPrompt + buildTriageEvidence; this is the
// compatibility path for providers or tests that want a single blob.
func buildTriagePrompt(triageCtx *TriageContext) string {
	policyPath := ""
	if triageCtx != nil && triageCtx.PolicyPath != "" {
		policyPath = triageCtx.PolicyPath
	}
	return renderPolicySystemPrompt(policyPath) + "\n" + buildTriageEvidence(triageCtx)
}

// parseTriageResponse parses the LLM response into a TriageResult.
//
// Accepts both the new two-axis schema (risk_level + user_authorization +
// verdict) and a best-effort legacy single-verdict schema where risk and
// authorization are inferred from verdict + severity. Missing fields are
// filled conservatively (default risk=medium, authorization=unknown) rather
// than failing the response outright.
func parseTriageResponse(response string, provider, model string, processingTime int64) (*TriageResult, error) {
	jsonStart := strings.Index(response, "{")
	jsonEnd := strings.LastIndex(response, "}")

	if jsonStart == -1 || jsonEnd == -1 || jsonEnd <= jsonStart {
		return nil, fmt.Errorf("no valid JSON found in response")
	}

	jsonStr := response[jsonStart : jsonEnd+1]

	var result struct {
		RiskLevel         string  `json:"risk_level"`
		UserAuthorization string  `json:"user_authorization"`
		Verdict           string  `json:"verdict"`
		Outcome           string  `json:"outcome"`         // guardian-style alias for verdict ("allow"/"deny")
		Confidence        float64 `json:"confidence"`
		Reasoning         string  `json:"reasoning"`
		Rationale         string  `json:"rationale"`       // guardian-style alias for reasoning
		SuggestedAction   string  `json:"suggested_action"`
	}

	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("parsing JSON response: %w", err)
	}

	// Accept `outcome` as a drop-in alias for `verdict` (guardian shape).
	// Map guardian "deny" onto our "block". "allow" stays "allow".
	if result.Verdict == "" && result.Outcome != "" {
		switch result.Outcome {
		case "deny":
			result.Verdict = "block"
		case "allow":
			result.Verdict = "allow"
		default:
			result.Verdict = result.Outcome
		}
	}

	// Accept `rationale` as a drop-in alias for `reasoning`.
	if result.Reasoning == "" && result.Rationale != "" {
		result.Reasoning = result.Rationale
	}

	// Normalise / validate risk level
	risk := RiskLevel(strings.ToLower(strings.TrimSpace(result.RiskLevel)))
	if !risk.IsValid() {
		risk = RiskLevelMedium // conservative default when the model omits risk
	}

	// Normalise / validate user authorization
	auth := UserAuthorization(strings.ToLower(strings.TrimSpace(result.UserAuthorization)))
	if !auth.IsValid() {
		auth = UserAuthorizationUnknown
	}

	// Derive verdict from the two axes if the model did not supply one.
	verdict := strings.ToLower(strings.TrimSpace(result.Verdict))
	switch verdict {
	case "block", "allow", "investigate":
		// explicit and valid
	case "":
		verdict = deriveVerdict(risk, auth)
	default:
		return nil, fmt.Errorf("invalid verdict: %s", result.Verdict)
	}

	// Clamp confidence to [0,1]. Missing confidence is neutral.
	if result.Confidence < 0.0 || result.Confidence > 1.0 {
		result.Confidence = 0.5
	}

	return &TriageResult{
		RiskLevel:         risk,
		UserAuthorization: auth,
		Verdict:           verdict,
		Confidence:        result.Confidence,
		Reasoning:         sanitizeInput(result.Reasoning),
		SuggestedAction:   sanitizeInput(result.SuggestedAction),
		Provider:          provider,
		Model:             model,
		ProcessingTime:    processingTime,
	}, nil
}

// deriveVerdict applies the default risk × authorization mapping described in
// internal/triage/policy_template.md when the model omits an explicit verdict.
// Tenant policy overrides live in the prompt itself; this is only the fallback
// used when the model returns the two axes without a verdict.
func deriveVerdict(risk RiskLevel, auth UserAuthorization) string {
	switch risk {
	case RiskLevelCritical:
		return "block"
	case RiskLevelHigh:
		if auth == UserAuthorizationMedium || auth == UserAuthorizationHigh {
			return "allow"
		}
		return "block"
	case RiskLevelMedium, RiskLevelLow:
		return "allow"
	default:
		return "investigate"
	}
}

// ssrfSafeDialContext returns a DialContext function that resolves DNS and
// rejects connections to private/loopback/link-local IP ranges.
// Uses net/netip with Unmap() to prevent IPv4-mapped IPv6 bypass vectors
// (e.g., ::ffff:127.0.0.1 being treated as a non-loopback IPv6 address).
func ssrfSafeDialContext(baseDialer *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("SSRF: invalid address %q: %w", addr, err)
		}

		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("SSRF: DNS resolution failed for %q: %w", host, err)
		}

		if len(ips) == 0 {
			return nil, fmt.Errorf("SSRF: no addresses resolved for %q", host)
		}

		for _, ip := range ips {
			if isSSRFBlockedIP(ip.IP) {
				return nil, fmt.Errorf("SSRF: resolved %q to private/local IP %s", host, ip.IP)
			}
		}

		// Dial only the validated addresses
		return baseDialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
}

// isSSRFBlockedIP checks if an IP is in a private/local range. Uses net/netip
// with Unmap() to catch IPv4-mapped IPv6 bypass vectors.
func isSSRFBlockedIP(ip net.IP) bool {
	if addr, ok := netip.AddrFromSlice(ip); ok {
		// Unmap IPv4-mapped IPv6 (::ffff:127.0.0.1 -> 127.0.0.1)
		addr = addr.Unmap()
		return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
			addr.IsLinkLocalMulticast() || addr.IsUnspecified()
	}
	// Fallback for unusual net.IP values
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// Common HTTP client with security settings
func createHTTPClient(timeout time.Duration) *retryablehttp.Client {
	// Create a retryable HTTP client with configured retry policy
	client := retryablehttp.NewClient()
	client.Logger = nil // Prevent retryablehttp from logging URLs/headers containing API keys

	// Configure retry policy: 3 retries, exponential backoff
	client.RetryMax = 3
	client.RetryWaitMin = 1 * time.Second
	client.RetryWaitMax = 30 * time.Second

	// Retry on 429 (rate limit) and 500-level errors
	client.CheckRetry = func(ctx context.Context, resp *http.Response, err error) (bool, error) {
		if err != nil {
			return true, err
		}
		// Retry on 429, 500, 502, 503, 504
		return resp.StatusCode == 429 ||
			resp.StatusCode == 500 ||
			resp.StatusCode == 502 ||
			resp.StatusCode == 503 ||
			resp.StatusCode == 504, nil
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second}

	// Configure the underlying HTTP client with SSRF protection
	client.HTTPClient = &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			MaxIdleConns:        10,
			IdleConnTimeout:     30 * time.Second,
			DisableKeepAlives:   false,
			TLSHandshakeTimeout: 10 * time.Second,
			DialContext:         ssrfSafeDialContext(dialer),
		},
	}

	return client
}

// createLocalHTTPClient creates an HTTP client without SSRF protection,
// for use with intentionally-local services like the OpenClaw gateway.
func createLocalHTTPClient(timeout time.Duration) *retryablehttp.Client {
	client := retryablehttp.NewClient()
	client.Logger = nil // Prevent retryablehttp from logging URLs/headers containing API keys
	client.RetryMax = 2
	client.RetryWaitMin = 500 * time.Millisecond
	client.RetryWaitMax = 5 * time.Second
	client.HTTPClient = &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			MaxIdleConns:        5,
			IdleConnTimeout:     30 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
		},
	}
	return client
}
