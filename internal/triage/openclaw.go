package triage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/hashicorp/go-retryablehttp"
)

// OpenClawProvider implements the Provider interface for the OpenClaw gateway.
// It calls the synchronous "completions" tool via POST /tools/invoke.
type OpenClawProvider struct {
	config       *config.TriageConfig
	client       *retryablehttp.Client
	gatewayURL   string
	gatewayToken string
}

// NewOpenClawProvider creates a new OpenClaw provider.
// Token resolution: config field → OPENCLAW_GATEWAY_TOKEN env → ~/.openclaw/openclaw.json file.
func NewOpenClawProvider(cfg *config.TriageConfig) (*OpenClawProvider, error) {
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
		return nil, fmt.Errorf("openclaw triage requires a gateway token: set triage.gateway_token, OPENCLAW_GATEWAY_TOKEN env, or ensure ~/.openclaw/openclaw.json is readable")
	}

	timeout := time.Duration(cfg.TimeoutSec) * time.Second

	return &OpenClawProvider{
		config:       cfg,
		client:       createLocalHTTPClient(timeout),
		gatewayURL:   gatewayURL,
		gatewayToken: gatewayToken,
	}, nil
}

// Name returns the provider name.
func (p *OpenClawProvider) Name() string {
	return "openclaw"
}

// Triage performs triage analysis by calling the OpenClaw gateway completions tool.
func (p *OpenClawProvider) Triage(ctx context.Context, triageCtx *TriageContext) (*TriageResult, error) {
	start := time.Now()

	prompt := buildTriagePrompt(triageCtx)

	result, err := p.invokeCompletions(ctx, prompt, p.config.MaxTokens)
	if err != nil {
		return nil, err
	}

	processingTime := time.Since(start).Milliseconds()

	return parseTriageResponse(result, p.Name(), p.config.Model, processingTime)
}

// HealthCheck performs a health check against the OpenClaw gateway.
// "connectivity" mode uses a minimal completions call; "full" mode uses a standard one.
func (p *OpenClawProvider) HealthCheck(ctx context.Context) error {
	if p.config.HealthCheckMode == "connectivity" {
		_, err := p.invokeCompletions(ctx, ".", 1)
		if err != nil {
			return fmt.Errorf("OpenClaw connectivity check failed: %w", err)
		}
		return nil
	}

	_, err := p.invokeCompletions(ctx, "Test", 5)
	if err != nil {
		return fmt.Errorf("OpenClaw health check failed: %w", err)
	}
	return nil
}

// invokeCompletions calls the OpenClaw gateway's completions tool synchronously.
func (p *OpenClawProvider) invokeCompletions(ctx context.Context, prompt string, maxTokens int) (string, error) {
	args := map[string]interface{}{
		"model":      p.config.Model,
		"system":     "You are a cybersecurity expert analyzing AI agent behavior. Respond only with the requested JSON format.",
		"prompt":     prompt,
		"max_tokens": maxTokens,
	}

	reqBody := openclawToolsInvokeRequest{
		Tool: "completions",
		Args: args,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshaling request: %w", err)
	}

	url := fmt.Sprintf("%s/tools/invoke", p.gatewayURL)
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.gatewayToken)
	httpReq.Header.Set("User-Agent", "AgentShield/1.0")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("calling OpenClaw gateway: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024)) // 1MB max
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("OpenClaw gateway authentication failed - check gateway token")
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OpenClaw gateway error (%d): %s", resp.StatusCode, string(respBody))
	}

	var invokeResp openclawToolsInvokeResponse
	if err := json.Unmarshal(respBody, &invokeResp); err != nil {
		return "", fmt.Errorf("decoding response: %w", err)
	}

	if !invokeResp.OK {
		errMsg := "unknown error"
		if invokeResp.Error != nil {
			errMsg = invokeResp.Error.Message
		}
		return "", fmt.Errorf("OpenClaw completions failed: %s", errMsg)
	}

	if invokeResp.Result == nil || invokeResp.Result.Result == "" {
		return "", fmt.Errorf("no content in OpenClaw completions response")
	}

	return invokeResp.Result.Result, nil
}
