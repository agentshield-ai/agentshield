package server

import (
	"context"
	"net/http"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// /embeddings/create: OpenAI-compatible embeddings through the server's
// provider keys (used by the SDKs' EmbeddingSimilarityScorer when run
// locally). mock/* models return offline bag-of-words embeddings.

type embeddingsReq struct {
	ProjectID string `json:"project_id"`
	Inputs    struct {
		Model string `json:"model"`
		Input any    `json:"input"`
	} `json:"inputs"`
}

func (s *Server) embeddingsCreate(ctx context.Context, _ *http.Request, req *embeddingsReq) (any, error) {
	pid := ""
	if req.ProjectID != "" {
		p, err := project(req.ProjectID)
		if err != nil {
			return nil, err
		}
		pid = p
	}
	var texts []string
	switch in := req.Inputs.Input.(type) {
	case string:
		texts = []string{in}
	case []any:
		for _, x := range in {
			t, ok := x.(string)
			if !ok {
				return nil, store.Invalidf("inputs.input must be a string or a list of strings")
			}
			texts = append(texts, t)
		}
	default:
		return nil, store.Invalidf("inputs.input must be a string or a list of strings")
	}
	if len(texts) == 0 || len(texts) > 2048 {
		return nil, store.Invalidf("inputs.input must hold 1 to 2048 strings")
	}
	if req.Inputs.Model == "" {
		return nil, store.Invalidf("inputs.model is required")
	}
	res, err := s.llm.Embed(ctx, pid, req.Inputs.Model, texts)
	if err != nil {
		return nil, store.Invalidf("%v", err)
	}
	data := make([]any, len(res.Embeddings))
	for i, e := range res.Embeddings {
		data[i] = map[string]any{"object": "embedding", "index": i, "embedding": e}
	}
	model := res.Model
	if model == "" {
		model = req.Inputs.Model
	}
	out := map[string]any{"object": "list", "data": data, "model": model}
	if res.Usage != nil {
		out["usage"] = res.Usage
	}
	return map[string]any{"response": out}, nil
}
