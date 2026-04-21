package triage

import (
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
)

//go:embed policy_template.md
var defaultPolicyTemplate string

//go:embed policy.md
var defaultTenantPolicy string

// RiskLevel is the intrinsic risk of a planned action, independent of user authorization.
// Inspired by OpenAI Codex guardian mode (codex-rs/core/src/guardian/policy_template.md).
type RiskLevel string

const (
	RiskLevelLow      RiskLevel = "low"
	RiskLevelMedium   RiskLevel = "medium"
	RiskLevelHigh     RiskLevel = "high"
	RiskLevelCritical RiskLevel = "critical"
)

// IsValid reports whether r is one of the known risk levels.
func (r RiskLevel) IsValid() bool {
	switch r {
	case RiskLevelLow, RiskLevelMedium, RiskLevelHigh, RiskLevelCritical:
		return true
	}
	return false
}

// UserAuthorization is the analyst's judgement of how clearly the user
// authorized the exact action being evaluated.
type UserAuthorization string

const (
	UserAuthorizationUnknown UserAuthorization = "unknown"
	UserAuthorizationLow     UserAuthorization = "low"
	UserAuthorizationMedium  UserAuthorization = "medium"
	UserAuthorizationHigh    UserAuthorization = "high"
)

// IsValid reports whether a is one of the known authorization levels.
func (a UserAuthorization) IsValid() bool {
	switch a {
	case UserAuthorizationUnknown, UserAuthorizationLow, UserAuthorizationMedium, UserAuthorizationHigh:
		return true
	}
	return false
}

// Verdict is the final triage decision derived from risk_level × user_authorization
// and tenant policy.
type Verdict string

const (
	VerdictAllow       Verdict = "allow"
	VerdictBlock       Verdict = "block"
	VerdictInvestigate Verdict = "investigate"
)

// IsValid reports whether v is one of the known verdicts.
func (v Verdict) IsValid() bool {
	switch v {
	case VerdictAllow, VerdictBlock, VerdictInvestigate:
		return true
	}
	return false
}

var (
	riskLevelValues = []string{
		string(RiskLevelLow), string(RiskLevelMedium), string(RiskLevelHigh), string(RiskLevelCritical),
	}
	userAuthorizationValues = []string{
		string(UserAuthorizationUnknown), string(UserAuthorizationLow), string(UserAuthorizationMedium), string(UserAuthorizationHigh),
	}
	verdictValues = []string{
		string(VerdictAllow), string(VerdictBlock), string(VerdictInvestigate),
	}
)

// outputContract is the JSON schema description appended to every policy
// system prompt. Providers that also enforce the schema via structured
// outputs still benefit from this contract for better model adherence.
const outputContract = `Your final message must be strict JSON with this exact schema:
{
  "risk_level": "low" | "medium" | "high" | "critical",
  "user_authorization": "unknown" | "low" | "medium" | "high",
  "verdict": "allow" | "block" | "investigate",
  "confidence": number between 0 and 1,
  "rationale": one concise sentence,
  "suggested_action": one short sentence
}`

// normalizeEnumString lower-cases and trims a string value for enum parsing.
func normalizeEnumString(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// cachedTriageOutputSchema is built once and reused across every OpenAI
// structured-output request. The schema is deterministic, so there's no reason
// to reallocate it per call.
var cachedTriageOutputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"risk_level": map[string]any{
			"type": "string",
			"enum": riskLevelValues,
		},
		"user_authorization": map[string]any{
			"type": "string",
			"enum": userAuthorizationValues,
		},
		"verdict": map[string]any{
			"type": "string",
			"enum": verdictValues,
		},
		"confidence": map[string]any{
			"type":    "number",
			"minimum": 0,
			"maximum": 1,
		},
		"rationale": map[string]any{
			"type": "string",
		},
		"suggested_action": map[string]any{
			"type": "string",
		},
	},
	"required": []string{
		"risk_level", "user_authorization", "verdict",
		"confidence", "rationale", "suggested_action",
	},
}

// triageOutputSchema returns the JSON schema enforced on the provider response.
// Matches the shape produced by parseTriageResponse. The returned map is
// shared; providers must treat it as read-only.
func triageOutputSchema() map[string]any { return cachedTriageOutputSchema }

var (
	// trimmedPolicyTemplate is the embedded template with the trailing newline
	// stripped, computed once at package load.
	trimmedPolicyTemplate = strings.TrimRight(defaultPolicyTemplate, "\n")
	// trimmedDefaultTenantPolicy is the embedded tenant block ready to
	// substitute into the template.
	trimmedDefaultTenantPolicy = strings.TrimSpace(defaultTenantPolicy)

	// policyPromptCache memoises the rendered system prompt keyed by
	// tenantPolicyPath so the file read + string substitution only runs once
	// per distinct path (typically once total, since the path comes from
	// config). Invalidated on process restart; callers that hot-reload policy
	// files should clear this via invalidatePolicyCache.
	policyPromptCache sync.Map // map[string]string
)

// renderPolicySystemPrompt returns the full system prompt for triage:
// the policy template with the tenant block substituted in, followed by the
// output contract. When tenantPolicyPath is set but unreadable, we log a
// warning once (via the cache) and fall back to the embedded default so the
// triager still works but operators see the misconfiguration.
func renderPolicySystemPrompt(tenantPolicyPath string) string {
	if cached, ok := policyPromptCache.Load(tenantPolicyPath); ok {
		return cached.(string)
	}
	rendered := computePolicySystemPrompt(tenantPolicyPath)
	policyPromptCache.Store(tenantPolicyPath, rendered)
	return rendered
}

func computePolicySystemPrompt(tenantPolicyPath string) string {
	tenant := trimmedDefaultTenantPolicy
	if tenantPolicyPath != "" {
		data, err := os.ReadFile(tenantPolicyPath)
		if err != nil {
			slog.Warn("triage policy file unreadable, using embedded default",
				"path", tenantPolicyPath, "error", err)
		} else {
			tenant = strings.TrimSpace(string(data))
		}
	}
	policy := strings.ReplaceAll(trimmedPolicyTemplate, "{tenant_policy_config}", tenant)
	return fmt.Sprintf("%s\n\n%s\n", policy, outputContract)
}

// invalidatePolicyCache clears the cached policy prompt. Exposed for tests
// and future hot-reload; not currently wired up to SIGHUP.
func invalidatePolicyCache() {
	policyPromptCache.Range(func(k, _ any) bool {
		policyPromptCache.Delete(k)
		return true
	})
}
