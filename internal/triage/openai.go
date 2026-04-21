package triage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/hashicorp/go-retryablehttp"
)

// OpenAIProvider implements the Provider interface for OpenAI
type OpenAIProvider struct {
	config *config.TriageConfig
	client *retryablehttp.Client
	apiURL string
}

// OpenAIRequest represents the request format for OpenAI API
type OpenAIRequest struct {
	Model          string                `json:"model"`
	Messages       []OpenAIMessage       `json:"messages"`
	MaxTokens      int                   `json:"max_tokens"`
	Temperature    float64               `json:"temperature"`
	ResponseFormat *OpenAIResponseFormat `json:"response_format,omitempty"`
}

// OpenAIResponseFormat enables OpenAI structured outputs. When type is
// "json_schema", the model is constrained to emit a response that matches
// the provided schema. See https://platform.openai.com/docs/guides/structured-outputs
type OpenAIResponseFormat struct {
	Type       string                 `json:"type"`                  // "json_schema"
	JSONSchema *OpenAIJSONSchemaBlock `json:"json_schema,omitempty"`
}

type OpenAIJSONSchemaBlock struct {
	Name   string         `json:"name"`
	Strict bool           `json:"strict"`
	Schema map[string]any `json:"schema"`
}

// OpenAIMessage represents a message in the OpenAI format
type OpenAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// OpenAIResponse represents the response from OpenAI API
type OpenAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

// OpenAIError represents an error response from OpenAI API
type OpenAIError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// NewOpenAIProvider creates a new OpenAI provider.
// Supports custom base URLs (e.g. OpenRouter) via cfg.BaseURL.
func NewOpenAIProvider(cfg *config.TriageConfig) (*OpenAIProvider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("OpenAI API key is required")
	}

	apiURL := "https://api.openai.com/v1/chat/completions"
	if cfg.BaseURL != "" {
		apiURL = cfg.BaseURL + "/chat/completions"
	}

	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	return &OpenAIProvider{
		config: cfg,
		client: createHTTPClient(timeout),
		apiURL: apiURL,
	}, nil
}

// Name returns the provider name
func (p *OpenAIProvider) Name() string {
	return "openai"
}

// Triage performs triage analysis using OpenAI
func (p *OpenAIProvider) Triage(ctx context.Context, triageCtx *TriageContext) (*TriageResult, error) {
	start := time.Now()

	// Split prompt into system (policy) and user (evidence) messages. This
	// keeps the fixed policy out of the cacheable-per-request surface and lets
	// structured outputs constrain the final assistant message to strict JSON.
	policyPath := ""
	if triageCtx != nil {
		policyPath = triageCtx.PolicyPath
	}
	systemPrompt := renderPolicySystemPrompt(policyPath)
	userPrompt := buildTriageEvidence(triageCtx)

	reqBody := OpenAIRequest{
		Model: p.config.Model,
		Messages: []OpenAIMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		MaxTokens:   p.config.MaxTokens,
		Temperature: 0.1, // Low temperature for consistent analysis
	}

	if p.config.StructuredOutput {
		reqBody.ResponseFormat = &OpenAIResponseFormat{
			Type: "json_schema",
			JSONSchema: &OpenAIJSONSchemaBlock{
				Name:   "agentshield_triage",
				Strict: true,
				Schema: triageOutputSchema(),
			},
		}
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
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", p.config.APIKey))
	req.Header.Set("User-Agent", "AgentShield/1.0")

	// Make the request
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("making request to OpenAI: %w", err)
	}
	defer resp.Body.Close()

	// Read response with size limit to prevent OOM from malicious responses
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024)) // 1MB max
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	// Handle non-200 responses
	if resp.StatusCode != http.StatusOK {
		var openaiErr OpenAIError
		if json.Unmarshal(body, &openaiErr) == nil && openaiErr.Error.Message != "" {
			return nil, fmt.Errorf("OpenAI API error (%d): %s", resp.StatusCode, openaiErr.Error.Message)
		}
		return nil, fmt.Errorf("OpenAI API error (%d): %s", resp.StatusCode, string(body))
	}

	// Parse response
	var openaiResp OpenAIResponse
	if err := json.Unmarshal(body, &openaiResp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	if len(openaiResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in OpenAI response")
	}

	content := openaiResp.Choices[0].Message.Content
	processingTime := time.Since(start).Milliseconds()

	// Parse the triage response
	return parseTriageResponse(content, p.Name(), p.config.Model, processingTime)
}

// HealthCheck performs a health check against the OpenAI API.
// When HealthCheckMode is "connectivity", it uses the free /v1/models endpoint.
// Otherwise (default "full"), it makes a minimal completion request.
func (p *OpenAIProvider) HealthCheck(ctx context.Context) error {
	if p.config.HealthCheckMode == "connectivity" {
		return p.healthCheckConnectivity(ctx)
	}
	return p.healthCheckFull(ctx)
}

// healthCheckConnectivity validates API key and connectivity using the
// free models list endpoint (GET /v1/models), which costs zero tokens.
func (p *OpenAIProvider) healthCheckConnectivity(ctx context.Context) error {
	modelsURL := strings.TrimSuffix(p.apiURL, "/chat/completions") + "/models"

	req, err := retryablehttp.NewRequestWithContext(ctx, "GET", modelsURL, nil)
	if err != nil {
		return fmt.Errorf("creating connectivity check request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", p.config.APIKey))
	req.Header.Set("User-Agent", "AgentShield/1.0")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("OpenAI connectivity check failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("OpenAI API authentication failed - check API key")
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf("OpenAI API server error: %d", resp.StatusCode)
	}
	return nil
}

// healthCheckFull makes a minimal completion request to verify full API functionality.
func (p *OpenAIProvider) healthCheckFull(ctx context.Context) error {
	reqBody := OpenAIRequest{
		Model: p.config.Model,
		Messages: []OpenAIMessage{
			{
				Role:    "user",
				Content: "Test",
			},
		},
		MaxTokens: 5,
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
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", p.config.APIKey))
	req.Header.Set("User-Agent", "AgentShield/1.0")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("OpenAI health check failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("OpenAI API authentication failed - check API key")
	}

	if resp.StatusCode >= 500 {
		return fmt.Errorf("OpenAI API server error: %d", resp.StatusCode)
	}

	return nil
}