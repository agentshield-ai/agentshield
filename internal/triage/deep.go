package triage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/hashicorp/go-retryablehttp"
)

// openclawToolsInvokeRequest matches the OpenClaw /tools/invoke HTTP API
type openclawToolsInvokeRequest struct {
	Tool       string                 `json:"tool"`
	Args       map[string]interface{} `json:"args"`
	SessionKey string                 `json:"sessionKey,omitempty"`
}

// openclawToolsInvokeResponse matches the OpenClaw /tools/invoke response
type openclawToolsInvokeResponse struct {
	OK     bool                `json:"ok"`
	Result *openclawToolsResult `json:"result,omitempty"`
	Error  *openclawToolsError  `json:"error,omitempty"`
}

type openclawToolsResult struct {
	Status string `json:"status"`
	Result string `json:"result,omitempty"`
}

type openclawToolsError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// readOpenClawToken reads the gateway token from the OpenClaw config file
func readOpenClawToken() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(home + "/.openclaw/openclaw.json")
	if err != nil {
		return "", err
	}

	var cfg struct {
		Gateway struct {
			Auth struct {
				Token string `json:"token"`
			} `json:"auth"`
		} `json:"gateway"`
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", err
	}

	return cfg.Gateway.Auth.Token, nil
}

// DeepTriager handles async deep triage via OpenClaw sub-agents.
// It fires off an investigation in the background and delivers results
// via the OpenClaw announce mechanism (appears in user's chat) or webhook.
type DeepTriager struct {
	config       *config.DeepTriageConfig
	gatewayURL   string
	gatewayToken string
	client       *retryablehttp.Client
}

// NewDeepTriager creates a new deep triager. Returns nil if disabled.
func NewDeepTriager(cfg *config.DeepTriageConfig) (*DeepTriager, error) {
	if cfg == nil || !cfg.Enabled {
		return nil, nil
	}

	gatewayURL := cfg.GatewayURL
	if gatewayURL == "" {
		gatewayURL = "http://127.0.0.1:18789"
	}

	gatewayToken := cfg.GatewayToken
	if gatewayToken == "" {
		gatewayToken = os.Getenv("OPENCLAW_GATEWAY_TOKEN")
	}
	if gatewayToken == "" {
		token, err := readOpenClawToken()
		if err == nil && token != "" {
			gatewayToken = token
		}
	}

	if gatewayToken == "" {
		return nil, fmt.Errorf("deep triage requires OpenClaw gateway token: set deep_triage.gateway_token, OPENCLAW_GATEWAY_TOKEN env, or ensure ~/.openclaw/openclaw.json is readable")
	}

	// Apply defaults
	if cfg.MinSeverity == "" {
		cfg.MinSeverity = "critical"
	}

	agentCfg := cfg.Agent
	if agentCfg.SystemPrompt == "" {
		agentCfg.SystemPrompt = DefaultDeepTriagePrompt
	}
	if agentCfg.TimeoutSec == 0 {
		agentCfg.TimeoutSec = 120
	}

	timeout := time.Duration(agentCfg.TimeoutSec) * time.Second

	return &DeepTriager{
		config:       cfg,
		gatewayURL:   gatewayURL,
		gatewayToken: gatewayToken,
		client:       createHTTPClient(timeout),
	}, nil
}

// ShouldDeepTriage determines if an alert warrants deep investigation
func (d *DeepTriager) ShouldDeepTriage(alert engine.RuleResult) bool {
	if d == nil {
		return false
	}

	switch d.config.MinSeverity {
	case "low":
		return true
	case "medium":
		return alert.Severity != engine.SeverityLow
	case "high":
		return alert.Severity == engine.SeverityHigh || alert.Severity == engine.SeverityCritical
	case "critical":
		return alert.Severity == engine.SeverityCritical
	default:
		return alert.Severity == engine.SeverityCritical
	}
}

// InvestigateAsync fires off a deep triage investigation in the background.
// It does NOT block — results are delivered via OpenClaw announce or webhook.
func (d *DeepTriager) InvestigateAsync(alerts []engine.RuleResult, req *models.EvaluationRequest, fastResults []TriageResult) {
	if d == nil {
		return
	}

	// Filter to alerts that warrant deep triage
	var deepAlerts []engine.RuleResult
	for _, alert := range alerts {
		if d.ShouldDeepTriage(alert) {
			deepAlerts = append(deepAlerts, alert)
		}
	}

	if len(deepAlerts) == 0 {
		return
	}

	// Fire and forget
	go func() {
		if err := d.investigate(deepAlerts, req, fastResults); err != nil {
			slog.Warn("Deep triage failed", "error", err)
		}
	}()
}

func (d *DeepTriager) investigate(alerts []engine.RuleResult, req *models.EvaluationRequest, fastResults []TriageResult) error {
	task := d.buildTask(alerts, req, fastResults)

	// Build spawn args
	spawnArgs := map[string]interface{}{
		"task":    task,
		"label":   "agentshield-deep-triage",
		"cleanup": "delete",
	}
	if d.config.Agent.AgentID != "" {
		spawnArgs["agentId"] = d.config.Agent.AgentID
	}
	if d.config.Agent.Model != "" {
		spawnArgs["model"] = d.config.Agent.Model
	}
	if d.config.Agent.Thinking != "" {
		spawnArgs["thinking"] = d.config.Agent.Thinking
	}
	if d.config.Agent.TimeoutSec > 0 {
		spawnArgs["timeoutSeconds"] = d.config.Agent.TimeoutSec
	}

	reqBody := openclawToolsInvokeRequest{
		Tool: "sessions_spawn",
		Args: spawnArgs,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	url := fmt.Sprintf("%s/tools/invoke", d.gatewayURL)
	httpReq, err := retryablehttp.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+d.gatewayToken)

	resp, err := d.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("calling openclaw gateway: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("openclaw gateway returned %d", resp.StatusCode)
	}

	var invokeResp openclawToolsInvokeResponse
	if err := json.NewDecoder(resp.Body).Decode(&invokeResp); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	if !invokeResp.OK {
		errMsg := "unknown error"
		if invokeResp.Error != nil {
			errMsg = invokeResp.Error.Message
		}
		return fmt.Errorf("spawn failed: %s", errMsg)
	}

	slog.Info("Deep triage investigation spawned", "alert_count", len(alerts))
	return nil
}

func (d *DeepTriager) buildTask(alerts []engine.RuleResult, req *models.EvaluationRequest, fastResults []TriageResult) string {
	var b strings.Builder

	b.WriteString(d.config.Agent.SystemPrompt)
	b.WriteString("\n\n---\n\n")

	// Tool instructions
	if len(d.config.Agent.Tools) > 0 {
		b.WriteString("You have access to tools. Use them to investigate thoroughly:\n")
		for _, tool := range d.config.Agent.Tools {
			switch tool {
			case "web_search":
				b.WriteString("- web_search: Look up CVEs, known attack patterns, threat intelligence, IOCs\n")
			case "web_fetch":
				b.WriteString("- web_fetch: Fetch specific URLs for detailed threat analysis\n")
			case "memory_search":
				b.WriteString("- memory_search: Search past alert history and analyst decisions\n")
			case "read":
				b.WriteString("- read: Read rule files or config for deeper analysis\n")
			}
		}
		b.WriteString("\nUse these tools actively — you have time for a thorough investigation.\n\n")
	}

	b.WriteString("## Security Alert — Deep Investigation Required\n\n")

	// Include the fast triage results for context
	if len(fastResults) > 0 {
		b.WriteString("### Fast Triage Results (already completed)\n")
		for i, fr := range fastResults {
			b.WriteString(fmt.Sprintf("%d. **%s** (confidence: %.0f%%) — %s\n",
				i+1, fr.Verdict, fr.Confidence*100, fr.Reasoning))
		}
		b.WriteString("\n")
	}

	b.WriteString("### Alerts to Investigate\n\n")

	for i, alert := range alerts {
		b.WriteString(fmt.Sprintf("**Alert %d: %s** (Severity: %s)\n", i+1, sanitizeInput(alert.RuleName), string(alert.Severity)))
		b.WriteString(fmt.Sprintf("- Rule ID: %s\n", sanitizeInput(alert.RuleID)))
		b.WriteString(fmt.Sprintf("- Description: %s\n", sanitizeInput(alert.Description)))
		// Tags not yet in RuleResult — future: add MITRE ATT&CK tags
		b.WriteString("\n")
	}

	// Request context
	b.WriteString("### Request Context\n")
	b.WriteString(fmt.Sprintf("- Tool: %s\n", sanitizeInput(req.Tool)))
	b.WriteString(fmt.Sprintf("- Session: %s\n", sanitizeInput(req.SessionID)))
	maskedArgs := maskSensitiveData(req.Args)
	argsJSON, _ := json.Marshal(maskedArgs)
	b.WriteString(fmt.Sprintf("- Arguments: %s\n\n", string(argsJSON)))

	b.WriteString("### Your Task\n")
	b.WriteString("Perform a thorough investigation. Use your tools to:\n")
	b.WriteString("1. Search for known attack patterns matching this behaviour\n")
	b.WriteString("2. Check if the domains/IPs/commands are associated with known threats\n")
	b.WriteString("3. Correlate with any recent alert patterns\n")
	b.WriteString("4. Assess the full attack chain and potential impact\n")
	b.WriteString("5. Provide a detailed report with concrete recommendations\n\n")
	b.WriteString("Write your findings as a clear, concise security report. This will be delivered to the security team.\n")

	return b.String()
}

// DefaultDeepTriagePrompt is the system prompt for deep triage agents
const DefaultDeepTriagePrompt = `You are a Senior Security Analyst conducting a deep investigation into an AI agent security alert.

Unlike fast triage (which makes a quick call), you have time and tools to investigate thoroughly. You should:
- Search for threat intelligence on any domains, IPs, or attack patterns
- Check CVE databases for relevant vulnerabilities  
- Correlate with known AI agent attack campaigns
- Analyse the full attack chain
- Provide actionable remediation steps

Your investigation will be delivered as a report to the security team. Be thorough but concise.
Focus on what's actionable — the team needs to know what happened, how bad it is, and what to do about it.`
