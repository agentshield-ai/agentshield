package server

import (
	"context"
	"net/http"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

// Weave response shapes for endpoints whose Weft shape differs, plus
// GET /version.

func init() {
	// Public like /server_info: no API key needed.
	publicRouteExtensions = append(publicRouteExtensions, func(s *Server, r chi.Router) {
		r.Get("/version", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, map[string]any{"version": Version, "server": "weft", "trace_server_version": Version})
		})
	})
}

// weaveCallStatsFields are CallStatsReq fields that Weft's own /calls/stats
// request does not use. Any of them selects Weave's request/response shape.
type weaveCallStatsFields struct {
	Granularity  *int64                   `json:"granularity"`
	UsageMetrics *[]store.WeaveMetricSpec `json:"usage_metrics"`
	CallMetrics  *[]store.WeaveMetricSpec `json:"call_metrics"`
	Timezone     *string                  `json:"timezone"`
}

func (f weaveCallStatsFields) isWeave() bool {
	return f.Granularity != nil || f.UsageMetrics != nil || f.CallMetrics != nil || f.Timezone != nil
}

func (s *Server) weaveCallsStats(ctx context.Context, req *callsStatsReq) (any, error) {
	q := store.WeaveCallStatsReq{ProjectID: req.ProjectID, Start: req.Start, End: req.End, Granularity: req.Granularity, Filter: req.Filter}
	if req.UsageMetrics != nil {
		q.UsageMetrics = *req.UsageMetrics
	}
	if req.CallMetrics != nil {
		q.CallMetrics = *req.CallMetrics
	}
	if req.Timezone != nil {
		q.Timezone = *req.Timezone
	}
	return s.store.ComputeWeaveCallStats(ctx, q)
}
