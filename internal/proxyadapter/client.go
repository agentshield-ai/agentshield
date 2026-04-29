package proxyadapter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/agentshield-ai/agentshield/internal/models"
)

// Client posts evaluation requests to AgentShield's /api/v1/evaluate.
// Failures are returned to the caller; the adapter loop logs + drops on
// failure rather than blocking the stream. This is acceptable for an
// observability adapter — losing one observation under engine downtime is
// preferable to back-pressuring the proxy.
type Client struct {
	endpoint string
	token    string
	http     *http.Client
}

func NewClient(endpoint, token string) *Client {
	return &Client{
		endpoint: endpoint,
		token:    token,
		http:     &http.Client{Timeout: 10 * time.Second},
	}
}

// Post sends one evaluation request. Returns the parsed response so the
// adapter can log the action AgentShield decided (which may differ from the
// upstream proxy's own decision — that's the whole point of layering).
func (c *Client) Post(req *models.EvaluationRequest) (*EvalResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, c.endpoint+"/api/v1/evaluate", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("engine status %d: %s", resp.StatusCode, string(respBody))
	}

	var out EvalResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

// EvalResponse is the subset of evaluate.EvaluationResponse this adapter
// reports. Avoiding a direct dependency on internal/evaluate keeps the
// adapter compilable as a leaf package.
type EvalResponse struct {
	Action       string `json:"action"`
	AlertCount   int    `json:"-"`
	AlertSummary string `json:"-"`
	Cached       bool   `json:"cached"`
}
