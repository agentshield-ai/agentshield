package triage

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/models"
)

func TestRiskLevelIsValid(t *testing.T) {
	valid := []RiskLevel{RiskLevelLow, RiskLevelMedium, RiskLevelHigh, RiskLevelCritical}
	for _, r := range valid {
		if !r.IsValid() {
			t.Errorf("expected %q to be valid", r)
		}
	}
	if RiskLevel("bogus").IsValid() {
		t.Error("expected bogus risk level to be invalid")
	}
}

func TestUserAuthorizationIsValid(t *testing.T) {
	valid := []UserAuthorization{UserAuthorizationUnknown, UserAuthorizationLow, UserAuthorizationMedium, UserAuthorizationHigh}
	for _, a := range valid {
		if !a.IsValid() {
			t.Errorf("expected %q to be valid", a)
		}
	}
	if UserAuthorization("bogus").IsValid() {
		t.Error("expected bogus authorization to be invalid")
	}
}

func TestDeriveVerdict(t *testing.T) {
	cases := []struct {
		risk RiskLevel
		auth UserAuthorization
		want Verdict
	}{
		{RiskLevelCritical, UserAuthorizationHigh, VerdictBlock},
		{RiskLevelCritical, UserAuthorizationLow, VerdictBlock},
		{RiskLevelHigh, UserAuthorizationHigh, VerdictAllow},
		{RiskLevelHigh, UserAuthorizationMedium, VerdictAllow},
		{RiskLevelHigh, UserAuthorizationLow, VerdictBlock},
		{RiskLevelHigh, UserAuthorizationUnknown, VerdictBlock},
		{RiskLevelMedium, UserAuthorizationUnknown, VerdictAllow},
		{RiskLevelLow, UserAuthorizationUnknown, VerdictAllow},
	}
	for _, c := range cases {
		got := deriveVerdict(c.risk, c.auth)
		if got != c.want {
			t.Errorf("deriveVerdict(%s,%s)=%s; want %s", c.risk, c.auth, got, c.want)
		}
	}
}

func TestSeverityToRiskLevel(t *testing.T) {
	cases := []struct {
		sev  engine.AlertSeverity
		want RiskLevel
	}{
		{engine.SeverityCritical, RiskLevelCritical},
		{engine.SeverityHigh, RiskLevelHigh},
		{engine.SeverityMedium, RiskLevelMedium},
		{engine.SeverityLow, RiskLevelLow},
		{engine.AlertSeverity("unknown"), RiskLevelLow},
	}
	for _, c := range cases {
		got := severityToRiskLevel(c.sev)
		if got != c.want {
			t.Errorf("severityToRiskLevel(%s)=%s; want %s", c.sev, got, c.want)
		}
	}
}

func TestParseTriageResponse_TwoAxisSchema(t *testing.T) {
	resp := `{"risk_level":"high","user_authorization":"low","verdict":"block","confidence":0.92,"rationale":"Credential access to untrusted destination","suggested_action":"Block and alert"}`
	result, err := parseTriageResponse(resp, "test", "test-model", 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RiskLevel != RiskLevelHigh {
		t.Errorf("RiskLevel=%s; want high", result.RiskLevel)
	}
	if result.UserAuthorization != UserAuthorizationLow {
		t.Errorf("UserAuthorization=%s; want low", result.UserAuthorization)
	}
	if result.Verdict != "block" {
		t.Errorf("Verdict=%s; want block", result.Verdict)
	}
	if result.Confidence != 0.92 {
		t.Errorf("Confidence=%f; want 0.92", result.Confidence)
	}
	// rationale is accepted as alias for reasoning
	if result.Reasoning != "Credential access to untrusted destination" {
		t.Errorf("Reasoning=%q; want rationale text", result.Reasoning)
	}
	if result.ProcessingTime != 42 {
		t.Errorf("ProcessingTime=%d; want 42", result.ProcessingTime)
	}
}

func TestParseTriageResponse_DerivesVerdictWhenMissing(t *testing.T) {
	// When verdict is absent, it is derived from risk_level + user_authorization
	// per the default mapping.
	cases := []struct {
		name     string
		response     string
		wantVerdict  Verdict
	}{
		{
			name:        "critical + unknown -> block",
			response:    `{"risk_level":"critical","user_authorization":"unknown","confidence":0.9,"rationale":"r","suggested_action":"s"}`,
			wantVerdict: VerdictBlock,
		},
		{
			name:        "high + high -> allow",
			response:    `{"risk_level":"high","user_authorization":"high","confidence":0.9,"rationale":"r","suggested_action":"s"}`,
			wantVerdict: VerdictAllow,
		},
		{
			name:        "high + low -> block",
			response:    `{"risk_level":"high","user_authorization":"low","confidence":0.9,"rationale":"r","suggested_action":"s"}`,
			wantVerdict: VerdictBlock,
		},
		{
			name:        "low + unknown -> allow",
			response:    `{"risk_level":"low","user_authorization":"unknown","confidence":0.9,"rationale":"r","suggested_action":"s"}`,
			wantVerdict: VerdictAllow,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result, err := parseTriageResponse(c.response, "test", "test-model", 0)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Verdict != c.wantVerdict {
				t.Errorf("Verdict=%s; want %s", result.Verdict, c.wantVerdict)
			}
		})
	}
}

func TestParseTriageResponse_GuardianAliases(t *testing.T) {
	// Guardian-style shape: "outcome" + "rationale" without "verdict" or "reasoning".
	resp := `{"risk_level":"high","user_authorization":"low","outcome":"deny","confidence":0.8,"rationale":"Matches exfil pattern","suggested_action":"block"}`
	result, err := parseTriageResponse(resp, "test", "test-model", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Guardian "deny" maps onto our "block"
	if result.Verdict != "block" {
		t.Errorf("Verdict=%s; want block (mapped from deny)", result.Verdict)
	}
	if result.Reasoning != "Matches exfil pattern" {
		t.Errorf("Reasoning=%q; want rationale text", result.Reasoning)
	}
}

func TestParseTriageResponse_LegacyShapeFillsDefaults(t *testing.T) {
	// Old shape — no risk_level, no user_authorization. We still accept it but
	// fall back to conservative defaults for the new fields.
	resp := `{"verdict":"allow","confidence":0.7,"reasoning":"benign","suggested_action":"allow"}`
	result, err := parseTriageResponse(resp, "test", "test-model", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RiskLevel != RiskLevelMedium {
		t.Errorf("RiskLevel=%s; want medium (default)", result.RiskLevel)
	}
	if result.UserAuthorization != UserAuthorizationUnknown {
		t.Errorf("UserAuthorization=%s; want unknown (default)", result.UserAuthorization)
	}
	if result.Verdict != "allow" {
		t.Errorf("Verdict=%s; want allow", result.Verdict)
	}
}

func TestParseTriageResponse_InvalidFieldsFallBackGracefully(t *testing.T) {
	// Unrecognised risk/auth strings fall back to conservative defaults
	// rather than erroring.
	resp := `{"risk_level":"SEVERE","user_authorization":"definitely","verdict":"allow","confidence":0.5,"reasoning":"r","suggested_action":"s"}`
	result, err := parseTriageResponse(resp, "test", "test-model", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RiskLevel != RiskLevelMedium {
		t.Errorf("RiskLevel=%s; want medium (fallback)", result.RiskLevel)
	}
	if result.UserAuthorization != UserAuthorizationUnknown {
		t.Errorf("UserAuthorization=%s; want unknown (fallback)", result.UserAuthorization)
	}
}

func TestParseTriageResponse_OutputRoundtripsThroughJSON(t *testing.T) {
	// The parsed result must marshal back to JSON that includes the new fields
	// so plugins and dashboards can read them.
	resp := `{"risk_level":"high","user_authorization":"medium","verdict":"allow","confidence":0.8,"reasoning":"r","suggested_action":"s"}`
	result, err := parseTriageResponse(resp, "test", "test-model", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, `"risk_level":"high"`) {
		t.Errorf("marshalled JSON missing risk_level: %s", s)
	}
	if !strings.Contains(s, `"user_authorization":"medium"`) {
		t.Errorf("marshalled JSON missing user_authorization: %s", s)
	}
}

func TestRenderPolicySystemPrompt_IncludesContract(t *testing.T) {
	prompt := renderPolicySystemPrompt("")
	// sanity: the template substituted the tenant policy and the contract is appended
	if !strings.Contains(prompt, "Tenant Risk Taxonomy") {
		t.Error("expected rendered prompt to include tenant policy block")
	}
	if !strings.Contains(prompt, "risk_level") {
		t.Error("expected output contract to describe risk_level")
	}
	if !strings.Contains(prompt, "user_authorization") {
		t.Error("expected output contract to describe user_authorization")
	}
	if strings.Contains(prompt, "{tenant_policy_config}") {
		t.Error("template placeholder was not substituted")
	}
}

func TestBuildTriageEvidence_DoesNotLeakPolicy(t *testing.T) {
	// buildTriageEvidence produces just the evidence block — the policy and
	// output contract belong in the system prompt. This separation lets us use
	// structured outputs + prompt caching properly.
	req := &models.EvaluationRequest{Tool: "shell", Args: map[string]string{"cmd": "ls"}}
	ctx := &TriageContext{
		Alert:   engine.RuleResult{RuleName: "example", Description: "d", Severity: engine.SeverityHigh},
		Request: req,
	}
	evidence := buildTriageEvidence(ctx)
	if strings.Contains(evidence, "Tenant Risk Taxonomy") {
		t.Error("evidence block must not embed tenant policy (policy lives in system prompt)")
	}
	if !strings.Contains(evidence, "EVIDENCE START") {
		t.Error("evidence block should be wrapped in EVIDENCE markers")
	}
	if !strings.Contains(evidence, "example") {
		t.Error("evidence block should include the rule name")
	}
}

func TestTriageOutputSchema_Shape(t *testing.T) {
	schema := triageOutputSchema()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema properties missing")
	}
	for _, field := range []string{"risk_level", "user_authorization", "verdict", "confidence", "rationale", "suggested_action"} {
		if _, ok := props[field]; !ok {
			t.Errorf("schema missing required field %q", field)
		}
	}
	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatal("schema required missing or wrong type")
	}
	if len(required) < 6 {
		t.Errorf("expected >=6 required fields, got %d", len(required))
	}
}
