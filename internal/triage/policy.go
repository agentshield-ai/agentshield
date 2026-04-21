package triage

import (
	_ "embed"
	"fmt"
	"os"
	"strings"
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

// buildPolicyPrompt renders the triage policy prompt by substituting the
// tenant-specific policy block into the shared template. When
// tenantPolicyPath is empty or unreadable, the embedded defaults are used.
func buildPolicyPrompt(tenantPolicyPath string) string {
	tenant := strings.TrimSpace(defaultTenantPolicy)
	if tenantPolicyPath != "" {
		if data, err := os.ReadFile(tenantPolicyPath); err == nil {
			tenant = strings.TrimSpace(string(data))
		}
	}
	template := strings.TrimRight(defaultPolicyTemplate, "\n")
	return strings.ReplaceAll(template, "{tenant_policy_config}", tenant)
}

// triageOutputSchema returns the JSON schema enforced on the provider response.
// Matches the shape produced by parseTriageResponse. Providers that support
// structured outputs (OpenAI json_schema) should set this on their request.
func triageOutputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"risk_level": map[string]any{
				"type": "string",
				"enum": []string{"low", "medium", "high", "critical"},
			},
			"user_authorization": map[string]any{
				"type": "string",
				"enum": []string{"unknown", "low", "medium", "high"},
			},
			"verdict": map[string]any{
				"type": "string",
				"enum": []string{"allow", "block", "investigate"},
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
			"risk_level",
			"user_authorization",
			"verdict",
			"confidence",
			"rationale",
			"suggested_action",
		},
	}
}

// outputContractPrompt is appended to the policy prompt so the model knows the
// exact schema it must emit. Providers that also enforce the schema via
// structured outputs still benefit from this contract for better adherence.
func outputContractPrompt() string {
	return `Your final message must be strict JSON with this exact schema:
{
  "risk_level": "low" | "medium" | "high" | "critical",
  "user_authorization": "unknown" | "low" | "medium" | "high",
  "verdict": "allow" | "block" | "investigate",
  "confidence": number between 0 and 1,
  "rationale": one concise sentence,
  "suggested_action": one short sentence
}`
}

// renderPolicySystemPrompt renders the full system prompt: the policy template
// (with tenant policy merged in) followed by the output contract.
func renderPolicySystemPrompt(tenantPolicyPath string) string {
	return fmt.Sprintf("%s\n\n%s\n", buildPolicyPrompt(tenantPolicyPath), outputContractPrompt())
}
