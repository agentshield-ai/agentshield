package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

// mockEmbeddingDims is the size of mock/* embeddings.
const mockEmbeddingDims = 64

// EmbeddingResult is the outcome of an /embeddings request.
type EmbeddingResult struct {
	Model      string      `json:"model"`
	Embeddings [][]float64 `json:"embeddings"`
	Usage      any         `json:"usage,omitempty"`
}

func (c *Client) routeHeaders(ctx context.Context, rt Route) (map[string]string, error) {
	headers := map[string]string{}
	if rt.KeyName != "" {
		key, ok := c.routeKey(ctx, rt)
		if !ok && !strings.HasPrefix(rt.BaseURL, "http://localhost") && !strings.HasPrefix(rt.BaseURL, "http://127.0.0.1") {
			return nil, fmt.Errorf("no API key configured: set the %s secret", rt.KeyName)
		}
		if key != "" {
			headers["Authorization"] = "Bearer " + key
		}
	}
	for k, v := range rt.Headers {
		headers[k] = v
	}
	return headers, nil
}

// Embed returns one embedding per text using an OpenAI-compatible
// /embeddings endpoint. mock/* models return a deterministic bag-of-words
// embedding so texts sharing words are similar, which works offline.
func (c *Client) Embed(ctx context.Context, projectID, model string, texts []string) (*EmbeddingResult, error) {
	if len(texts) == 0 {
		return &EmbeddingResult{Model: model, Embeddings: [][]float64{}}, nil
	}
	rt, err := c.Resolve(ctx, projectID, model)
	if err != nil {
		return nil, err
	}
	switch rt.Kind {
	case "mock":
		out := &EmbeddingResult{Model: model}
		n := 0
		for _, t := range texts {
			out.Embeddings = append(out.Embeddings, MockEmbedding(t))
			n += len(strings.Fields(t))
		}
		out.Usage = map[string]any{"prompt_tokens": n, "total_tokens": n}
		return out, nil
	case "anthropic":
		return nil, fmt.Errorf("model %q does not support embeddings; use an OpenAI-compatible embedding model such as openai/text-embedding-3-small", model)
	}
	headers, err := c.routeHeaders(ctx, rt)
	if err != nil {
		return nil, err
	}
	resp, err := c.post(ctx, rt.BaseURL+"/embeddings", headers, map[string]any{"model": rt.Model, "input": texts})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Model string `json:"model"`
		Data  []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
		Usage any `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decoding embeddings response: %w", err)
	}
	if len(body.Data) != len(texts) {
		return nil, fmt.Errorf("provider returned %d embeddings for %d inputs", len(body.Data), len(texts))
	}
	out := &EmbeddingResult{Model: body.Model, Embeddings: make([][]float64, len(texts)), Usage: body.Usage}
	for i, d := range body.Data {
		idx := d.Index
		if idx < 0 || idx >= len(texts) {
			idx = i
		}
		out.Embeddings[idx] = d.Embedding
	}
	return out, nil
}

// MockEmbedding hashes lower-cased words into a unit vector.
func MockEmbedding(text string) []float64 {
	v := make([]float64, mockEmbeddingDims)
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for _, w := range words {
		h := fnv.New32a()
		h.Write([]byte(w))
		x := h.Sum32()
		sign := 1.0
		if x&1 == 1 {
			sign = -1
		}
		v[int(x>>1)%mockEmbeddingDims] += sign
	}
	var norm float64
	for _, x := range v {
		norm += x * x
	}
	if norm == 0 {
		return v
	}
	norm = math.Sqrt(norm)
	for i := range v {
		v[i] /= norm
	}
	return v
}

// CosineSimilarity of two vectors (0 when either is zero or sizes differ).
func CosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// ModerationResult is the first result of an OpenAI /moderations call.
type ModerationResult struct {
	Flagged        bool               `json:"flagged"`
	Categories     map[string]bool    `json:"categories"`
	CategoryScores map[string]float64 `json:"category_scores"`
}

// mockModerationTerms flag text in mock/* moderation (offline testing).
var mockModerationTerms = map[string]string{
	"kill": "violence", "attack": "violence", "hate": "hate", "idiot": "harassment", "stupid": "harassment",
	"bomb": "violence", "suicide": "self-harm",
}

// Moderate checks text with the OpenAI moderation API (model defaults to
// omni-moderation-latest). The OPENAI_API_KEY secret is used; mock/* models
// flag a small list of words offline.
func (c *Client) Moderate(ctx context.Context, projectID, model, text string) (*ModerationResult, error) {
	if model == "" {
		model = "omni-moderation-latest"
	}
	if strings.HasPrefix(model, "mock/") {
		res := &ModerationResult{Categories: map[string]bool{}, CategoryScores: map[string]float64{}}
		for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) }) {
			if cat, ok := mockModerationTerms[w]; ok {
				res.Flagged = true
				res.Categories[cat] = true
				res.CategoryScores[cat] = 0.9
			}
		}
		return res, nil
	}
	name := strings.TrimPrefix(model, "openai/")
	p := openAICompatible["openai"]
	rt := c.openAIRoute(ctx, "openai", name, p.BaseURL, p.KeyName)
	headers, err := c.routeHeaders(ctx, rt)
	if err != nil {
		return nil, err
	}
	resp, err := c.post(ctx, rt.BaseURL+"/moderations", headers, map[string]any{"model": name, "input": text})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Results []ModerationResult `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decoding moderation response: %w", err)
	}
	if len(body.Results) == 0 {
		return nil, fmt.Errorf("moderation response has no results")
	}
	return &body.Results[0], nil
}

// LookupSecret returns a stored secret, falling back to the environment.
func (c *Client) LookupSecret(ctx context.Context, name string) (string, bool) {
	return c.secret(ctx, name)
}
