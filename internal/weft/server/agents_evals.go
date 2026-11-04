package server

import (
	"context"
	"net/http"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

// Agent evals (Weft extensions): evaluation runs with the agent spans and
// conversations linked to them (weft.eval.* span attributes, or
// EvaluationLogger predictions' weft.linked_spans / genai_span_ref /
// linked_conversation_ids). /agents/spans/query, /agents/spans/stats and
// /agents/spans/custom-attrs/schema take evaluation_run_id and
// prediction_span_id filters.

func init() {
	routeExtensions = append(routeExtensions, func(s *Server, r chi.Router) {
		r.Post("/agents/evals/query", handle(s.agentEvalsQuery))
		r.Post("/agents/evals/conversations", handle(s.agentEvalConversations))
	})
}

func (s *Server) agentEvalsQuery(ctx context.Context, _ *http.Request, req *store.AgentEvalsQueryReq) (any, error) {
	rows, total, err := s.store.QueryAgentEvals(ctx, *req)
	if err != nil {
		return nil, err
	}
	return map[string]any{"evaluations": rows, "total_count": total}, nil
}

func (s *Server) agentEvalConversations(ctx context.Context, _ *http.Request, req *store.AgentEvalConversationsReq) (any, error) {
	return s.store.EvalConversations(ctx, *req)
}
