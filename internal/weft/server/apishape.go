package server

import (
	"context"
	"net/http"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

// Alternative response shapes some endpoints offer (selected by request
// fields), plus GET /version.

func init() {
	// Public like /server_info: no API key needed.
	publicRouteExtensions = append(publicRouteExtensions, func(s *Server, r chi.Router) {
		r.Get("/version", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, map[string]any{"version": Version, "server": "weft", "trace_server_version": Version})
		})
	})
}

// metricStatsFields select the metric-bucket form of /calls/stats
// (usage_buckets and call_buckets with per-metric aggregations) instead of
// the plain time series. Any of them selects it.
type metricStatsFields struct {
	Granularity  *int64                    `json:"granularity"`
	UsageMetrics *[]store.BucketMetricSpec `json:"usage_metrics"`
	CallMetrics  *[]store.BucketMetricSpec `json:"call_metrics"`
	Timezone     *string                   `json:"timezone"`
}

func (f metricStatsFields) isMetricStats() bool {
	return f.Granularity != nil || f.UsageMetrics != nil || f.CallMetrics != nil || f.Timezone != nil
}

func (s *Server) metricCallsStats(ctx context.Context, req *callsStatsReq) (any, error) {
	q := store.MetricStatsReq{ProjectID: req.ProjectID, Start: req.Start, End: req.End, Granularity: req.Granularity, Filter: req.Filter}
	if req.UsageMetrics != nil {
		q.UsageMetrics = *req.UsageMetrics
	}
	if req.CallMetrics != nil {
		q.CallMetrics = *req.CallMetrics
	}
	if req.Timezone != nil {
		q.Timezone = *req.Timezone
	}
	return s.store.ComputeMetricStats(ctx, q)
}
