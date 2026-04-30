package triage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/hashicorp/go-retryablehttp"
)

// AnthropicProvider implements the Provider interface for Anthropic
type AnthropicProvider struct {
	config *config.TriageConfig
	client *retryablehttp.Client
	apiURL string
}

// AnthropicRequest represents the request format for Anthropic API
type AnthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	Messages  []AnthropicMessage `json:"messages"`
}

// AnthropicMessage represents a message in the Anthropic format
type AnthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// AnthropicResponse represents the response from Anthropic API
type AnthropicResponse struct {
	Content []struct {
		Text string `json:"text"`
		Type string `json:"type"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// AnthropicError represents an error response from Anthropic API
type AnthropicError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// NewAnthropicProvider creates a new Anthropic provider
func NewAnthropicProvider(cfg *config.TriageConfig) (*AnthropicProvider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("anthropic API key is required")
	}

	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	return &AnthropicProvider{
		config: cfg,
		client: createHTTPClient(timeout),
		apiURL: "https://api.anthropic.com/v1/messages",
	}, nil
}

// Name returns the provider name
func (p *AnthropicProvider) Name() string {
	return "anthropic"
}

// Triage performs triage analysis using Anthropic Claude
func (p *AnthropicProvider) Triage(ctx context.Context, triageCtx *TriageContext) (*TriageResult, error) {
	start := time.Now()

	// Build the prompt
	prompt := buildTriagePrompt(triageCtx)

	// Create the request
	reqBody := AnthropicRequest{
		Model:     p.config.Model,
		MaxTokens: p.config.MaxTokens,
		Messages: []AnthropicMessage{
			{
				Role:    "user",
				Content: prompt,
			},
		},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	// Create retryable HTTP request
	req, err := retryablehttp.NewRequestWithContext(ctx, "POST", p.apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.config.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("User-Agent", "AgentShield/1.0")

	// Make the request
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("making request to Anthropic: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Read response with size limit to prevent OOM from malicious responses
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024)) // 1MB max
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	// Handle non-200 responses
	if resp.StatusCode != http.StatusOK {
		var anthropicErr AnthropicError
		if json.Unmarshal(body, &anthropicErr) == nil && anthropicErr.Error.Message != "" {
			return nil, fmt.Errorf("anthropic API error (%d): %s", resp.StatusCode, anthropicErr.Error.Message)
		}
		return nil, fmt.Errorf("anthropic API error (%d): %s", resp.StatusCode, string(body))
	}

	// Parse response
	var anthropicResp AnthropicResponse
	if err := json.Unmarshal(body, &anthropicResp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	if len(anthropicResp.Content) == 0 {
		return nil, fmt.Errorf("no content in anthropic response")
	}

	content := anthropicResp.Content[0].Text
	processingTime := time.Since(start).Milliseconds()

	// Parse the triage response
	return parseTriageResponse(content, p.Name(), p.config.Model, processingTime)
}

// HealthCheck performs a health check against the Anthropic API.
// When HealthCheckMode is "connectivity", it uses a minimal request (max_tokens=1,
// single-char prompt) to minimize token cost. Otherwise (default "full"), it uses
// the standard health check request.
func (p *AnthropicProvider) HealthCheck(ctx context.Context) error {
	if p.config.HealthCheckMode == "connectivity" {
		return p.healthCheckConnectivity(ctx)
	}
	return p.healthCheckFull(ctx)
}

// healthCheckConnectivity validates API key and connectivity with a minimal
// completion request. Anthropic has no free endpoint, so we minimize cost by
// using max_tokens=1 and the shortest possible prompt (".").
func (p *AnthropicProvider) healthCheckConnectivity(ctx context.Context) error {
	reqBody := AnthropicRequest{
		Model:     p.config.Model,
		MaxTokens: 1,
		Messages: []AnthropicMessage{
			{
				Role:    "user",
				Content: ".",
			},
		},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshaling connectivity check request: %w", err)
	}

	req, err := retryablehttp.NewRequestWithContext(ctx, "POST", p.apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("creating connectivity check request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.config.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("User-Agent", "AgentShield/1.0")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("anthropic connectivity check failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("anthropic API authentication failed - check API key")
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf("anthropic API server error: %d", resp.StatusCode)
	}
	return nil
}

// healthCheckFull makes a standard completion request to verify full API functionality.
func (p *AnthropicProvider) healthCheckFull(ctx context.Context) error {
	reqBody := AnthropicRequest{
		Model:     p.config.Model,
		MaxTokens: 5,
		Messages: []AnthropicMessage{
			{
				Role:    "user",
				Content: "Test",
			},
		},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshaling health check request: %w", err)
	}

	req, err := retryablehttp.NewRequestWithContext(ctx, "POST", p.apiURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("creating health check request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.config.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("User-Agent", "AgentShield/1.0")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("anthropic health check failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("anthropic API authentication failed - check API key")
	}

	if resp.StatusCode >= 500 {
		return fmt.Errorf("anthropic API server error: %d", resp.StatusCode)
	}

	return nil
}
