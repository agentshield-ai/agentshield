package server

import (
	"context"
	"net/http"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

// /calls/metrics: custom chart series (any aggregate of a call field over
// time, split by a group key). Used by dashboards and the chart builder.

func init() {
	routeExtensions = append(routeExtensions, func(s *Server, r chi.Router) {
		r.Post("/calls/metrics", handle(s.callsMetrics))
	})
}

func (s *Server) callsMetrics(ctx context.Context, _ *http.Request, req *store.MetricsQuery) (any, error) {
	return s.store.ComputeCallsMetrics(ctx, *req)
}
