// Package triage provides LLM-powered alert triage and analysis.
// This handles threat analysis, false positive reduction, and alert prioritization.
package triage

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
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

// TriageResult represents the result of LLM triage analysis
type TriageResult struct {
	Verdict         string  `json:"verdict"`          // "block", "allow", "investigate"
	Confidence      float64 `json:"confidence"`       // 0.0-1.0
	Reasoning       string  `json:"reasoning"`        // Explanation
	SuggestedAction string  `json:"suggested_action"` // Recommended next steps
	Provider        string  `json:"provider"`         // Which LLM provider was used
	Model           string  `json:"model"`            // Which model was used
	ProcessingTime  int64   `json:"processing_time"`  // Time in milliseconds
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
		}

		result, err := t.provider.Triage(ctx, triageCtx)
		if err != nil {
			// For triage failures, return a fallback result based on alert severity
			fallbackResult := &TriageResult{
				Verdict:         t.getFallbackVerdict(alert.Severity),
				Confidence:      0.5,
				Reasoning:       fmt.Sprintf("Triage failed: %v. Falling back to rule-only verdict.", err),
				SuggestedAction: "Review manually due to triage failure",
				Provider:        t.provider.Name(),
				Model:           t.config.Model,
				ProcessingTime:  0,
			}
			results = append(results, *fallbackResult)
			continue
		}

		results = append(results, *result)
	}

	return results, nil
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

// buildTriagePrompt builds the prompt for LLM triage
func buildTriagePrompt(triageCtx *TriageContext) string {
	// Sanitize all inputs
	ruleName := sanitizeInput(triageCtx.Alert.RuleName)
	description := sanitizeInput(triageCtx.Alert.Description)
	tool := sanitizeInput(triageCtx.Request.Tool)

	// Mask sensitive data in arguments
	args := maskSensitiveData(triageCtx.Request.Args)
	argsJSON, _ := json.Marshal(args)

	// Build recent context (limit to prevent prompt bloat)
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

	prompt := fmt.Sprintf(`You are a security analyst reviewing an AI agent tool call that triggered a detection rule.

Alert: %s (%s)
Rule description: %s
Tool called: %s
Arguments: %s
Recent context: %s
Correlation score: %.2f
Correlation factors: %v
Correlation window: %ds, recent alerts: %d

Determine if this is:
1. A true positive (genuine security concern that should be blocked)
2. A false positive (benign activity that incorrectly triggered the rule)

Respond in JSON format only:
{"verdict": "block"|"allow"|"investigate", "confidence": 0.95, "reasoning": "Brief explanation of your analysis", "suggested_action": "Specific recommendation"}

Guidelines:
- "block": Clear direct security risk in the current event
- "allow": False positive, safe to proceed
- "investigate": Uncertain, needs human review
- confidence: 0.0-1.0 (higher = more certain)
- Keep reasoning under 200 characters
- Correlation is supporting context, not sole proof
- If current command appears benign but correlation is high, prefer "investigate" over "block" unless direct malicious indicators are present`,
		ruleName,
		string(triageCtx.Alert.Severity),
		description,
		tool,
		string(argsJSON),
		recentContext.String(),
		triageCtx.Correlation.Score,
		triageCtx.Correlation.Factors,
		triageCtx.Correlation.WindowSec,
		triageCtx.Correlation.RecentCount)

	return prompt
}

// parseTriageResponse parses the LLM response into a TriageResult
func parseTriageResponse(response string, provider, model string, processingTime int64) (*TriageResult, error) {
	// Try to extract JSON from response (some models wrap it)
	jsonStart := strings.Index(response, "{")
	jsonEnd := strings.LastIndex(response, "}")

	if jsonStart == -1 || jsonEnd == -1 || jsonEnd <= jsonStart {
		return nil, fmt.Errorf("no valid JSON found in response")
	}

	jsonStr := response[jsonStart : jsonEnd+1]

	var result struct {
		Verdict         string  `json:"verdict"`
		Confidence      float64 `json:"confidence"`
		Reasoning       string  `json:"reasoning"`
		SuggestedAction string  `json:"suggested_action"`
	}

	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("parsing JSON response: %w", err)
	}

	// Validate verdict
	switch result.Verdict {
	case "block", "allow", "investigate":
		// Valid
	default:
		return nil, fmt.Errorf("invalid verdict: %s", result.Verdict)
	}

	// Validate confidence range
	if result.Confidence < 0.0 || result.Confidence > 1.0 {
		result.Confidence = 0.5 // Default to neutral confidence
	}

	return &TriageResult{
		Verdict:         result.Verdict,
		Confidence:      result.Confidence,
		Reasoning:       sanitizeInput(result.Reasoning),
		SuggestedAction: sanitizeInput(result.SuggestedAction),
		Provider:        provider,
		Model:           model,
		ProcessingTime:  processingTime,
	}, nil
}

// Common HTTP client with security settings
func createHTTPClient(timeout time.Duration) *retryablehttp.Client {
	// Create a retryable HTTP client with configured retry policy
	client := retryablehttp.NewClient()

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

	// Configure the underlying HTTP client
	client.HTTPClient = &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			MaxIdleConns:        10,
			IdleConnTimeout:     30 * time.Second,
			DisableKeepAlives:   false,
			TLSHandshakeTimeout: 10 * time.Second,
			// SECURITY: Prevent access to internal networks
			// This would need a custom DialContext in production for full SSRF protection
		},
	}

	return client
}
