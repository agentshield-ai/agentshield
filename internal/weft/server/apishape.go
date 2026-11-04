package server

import (
	"context"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// Alternative response shapes some endpoints offer (selected by request
// fields), plus GET /version.

// metricStatsFields select the metric-bucket form of /api/v1/spans/stats
// (usage_buckets and span_buckets with per-metric aggregations) instead of
// the plain time series. Any of them selects it.
type metricStatsFields struct {
	Granularity  *int64                    `json:"granularity"`
	UsageMetrics *[]store.BucketMetricSpec `json:"usage_metrics"`
	SpanMetrics  *[]store.BucketMetricSpec `json:"span_metrics"`
	Timezone     *string                   `json:"timezone"`
}

func (f metricStatsFields) isMetricStats() bool {
	return f.Granularity != nil || f.UsageMetrics != nil || f.SpanMetrics != nil || f.Timezone != nil
}

func (s *Server) metricCallsStats(ctx context.Context, req *callsStatsReq) (any, error) {
	q := store.MetricStatsReq{ProjectID: req.ProjectID, Start: req.Start, End: req.End, Granularity: req.Granularity, Filter: req.Filter}
	if req.UsageMetrics != nil {
		q.UsageMetrics = *req.UsageMetrics
	}
	if req.SpanMetrics != nil {
		q.SpanMetrics = *req.SpanMetrics
	}
	if req.Timezone != nil {
		q.Timezone = *req.Timezone
	}
	return s.store.ComputeMetricStats(ctx, q)
}
