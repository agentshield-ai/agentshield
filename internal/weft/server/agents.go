package server

import (
	"context"
	"net/http"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

// Weave's Agents API (/agents/*): agents, versions, spans, conversations and
// chat timelines derived from calls through the store's span index. Request
// and response shapes follow the weave client's generated server SDK.

func init() {
	routeExtensions = append(routeExtensions, func(s *Server, r chi.Router) {
		r.Post("/agents/query", handle(s.agentsQuery))
		r.Post("/agents/agent-versions/query", handle(s.agentVersionsQuery))
		r.Post("/agents/search", handle(s.agentsSearch))
		r.Post("/agents/spans/query", handle(s.agentSpansQuery))
		r.Post("/agents/spans/stats", handle(s.agentSpansStats))
		r.Post("/agents/spans/custom-attrs/schema", handle(s.agentCustomAttrsSchema))
		r.Post("/agents/conversations/spans", handle(s.agentConversationSpans))
		r.Post("/agents/conversations/chat", handle(s.agentConversationChat))
		r.Post("/agents/traces/chat", handle(s.agentTraceChat))
		// Weave's agents OTLP endpoint; the same ingest as /otel/v1/traces.
		r.Post("/agents/otel/v1/traces", s.otelTraces)
	})
}

func (s *Server) agentsQuery(ctx context.Context, _ *http.Request, req *store.AgentsQueryReq) (any, error) {
	rows, total, err := s.store.QueryAgents(ctx, *req)
	if err != nil {
		return nil, err
	}
	return map[string]any{"agents": rows, "total_count": total}, nil
}

func (s *Server) agentVersionsQuery(ctx context.Context, _ *http.Request, req *store.AgentVersionsQueryReq) (any, error) {
	rows, total, err := s.store.QueryAgentVersions(ctx, *req)
	if err != nil {
		return nil, err
	}
	return map[string]any{"versions": rows, "total_count": total}, nil
}

func (s *Server) agentsSearch(ctx context.Context, _ *http.Request, req *store.AgentSearchReq) (any, error) {
	rows, total, err := s.store.SearchAgentMessages(ctx, *req)
	if err != nil {
		return nil, err
	}
	return map[string]any{"results": rows, "total_conversations": total}, nil
}

func (s *Server) agentSpansQuery(ctx context.Context, _ *http.Request, req *store.AgentSpansQueryReq) (any, error) {
	return s.store.QueryAgentSpans(ctx, *req)
}

func (s *Server) agentSpansStats(ctx context.Context, _ *http.Request, req *store.AgentSpanStatsReq) (any, error) {
	return s.store.SpanStats(ctx, *req)
}

func (s *Server) agentCustomAttrsSchema(ctx context.Context, _ *http.Request, req *store.AgentCustomAttrsSchemaReq) (any, error) {
	attrs, more, limit, err := s.store.CustomAttrsSchema(ctx, *req)
	if err != nil {
		return nil, err
	}
	return map[string]any{"attributes": attrs, "has_more": more, "limit": limit, "offset": req.Offset}, nil
}

func (s *Server) agentConversationSpans(ctx context.Context, _ *http.Request, req *store.AgentConversationSpansReq) (any, error) {
	convs, err := s.store.ConversationSpanSequences(ctx, *req)
	if err != nil {
		return nil, err
	}
	return map[string]any{"conversations": convs}, nil
}

func (s *Server) agentConversationChat(ctx context.Context, _ *http.Request, req *store.AgentConversationChatReq) (any, error) {
	return s.store.ConversationChat(ctx, *req)
}

func (s *Server) agentTraceChat(ctx context.Context, _ *http.Request, req *store.AgentTraceChatReq) (any, error) {
	return s.store.TraceChat(ctx, *req)
}
