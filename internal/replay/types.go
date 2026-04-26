package replay

import (
	"time"

	"github.com/agentshield-ai/agentshield/internal/engine"
)

// HFRowsResponse is the paginated response from the HF Dataset Viewer /rows endpoint.
type HFRowsResponse struct {
	Features []HFFeature `json:"features"`
	Rows     []TraceRow  `json:"rows"`
	NumRows  int         `json:"num_rows_total"`
}

// HFFeature describes a column in the dataset.
type HFFeature struct {
	Name string `json:"name"`
	Type string `json:"_type"`
}

// TraceRow is a single row returned by the /rows endpoint.
type TraceRow struct {
	RowIdx int                    `json:"row_idx"`
	Row    map[string]interface{} `json:"row"`
}

// ExtractedEvent is a single tool call extracted from a trace row,
// before field enrichment for the rule engine.
type ExtractedEvent struct {
	SessionID string
	ToolName  string
	Args      map[string]string
	Content   string // Retrieved or output content (for semantic rules)
	FilePath  string
	URL       string
}

// ReplayResult is the outcome of evaluating one extracted event.
type ReplayResult struct {
	TraceIndex     int
	EventIndex     int
	SessionID      string
	ToolName       string
	Command        string
	Action         string
	Alerts         []engine.RuleResult
	EvalDurationNs int64
	Cached         bool
}

// ReportData is the final aggregated report.
type ReportData struct {
	Metadata           ReportMetadata       `json:"metadata" yaml:"metadata"`
	Summary            ReportSummary        `json:"summary" yaml:"summary"`
	RuleCoverage       RuleCoverageReport   `json:"rule_coverage" yaml:"rule_coverage"`
	AlertsByRule       []RuleAlertCount     `json:"alerts_by_rule" yaml:"alerts_by_rule"`
	AlertsBySeverity   map[string]int       `json:"alerts_by_severity" yaml:"alerts_by_severity"`
	TopAlertingRules   []RuleAlertCount     `json:"top_alerting_rules" yaml:"top_alerting_rules"`
	FPCandidates       []FPCandidate        `json:"fp_candidates" yaml:"fp_candidates"`
	ActionDistribution map[string]int       `json:"action_distribution" yaml:"action_distribution"`
	LatencyStats       LatencyStats         `json:"latency_stats" yaml:"latency_stats"`
	CacheStats         CacheStats           `json:"cache_stats" yaml:"cache_stats"`
}

// CacheStats reports verdict-cache behaviour observed during the replay.
// HitRate is fraction of total evaluations served from cache; latency
// percentiles are split so the deterministic-path advantage is visible.
type CacheStats struct {
	Enabled          bool    `json:"enabled" yaml:"enabled"`
	HitCount         int     `json:"hit_count" yaml:"hit_count"`
	MissCount        int     `json:"miss_count" yaml:"miss_count"`
	HitRate          float64 `json:"hit_rate" yaml:"hit_rate"`
	HitLatencyP50Us  int64   `json:"hit_latency_p50_us" yaml:"hit_latency_p50_us"`
	HitLatencyP95Us  int64   `json:"hit_latency_p95_us" yaml:"hit_latency_p95_us"`
	MissLatencyP50Us int64   `json:"miss_latency_p50_us" yaml:"miss_latency_p50_us"`
	MissLatencyP95Us int64   `json:"miss_latency_p95_us" yaml:"miss_latency_p95_us"`
}

// ReportMetadata captures context about the replay run.
type ReportMetadata struct {
	Dataset         string    `json:"dataset" yaml:"dataset"`
	TracesProcessed int       `json:"traces_processed" yaml:"traces_processed"`
	EventsEvaluated int       `json:"events_evaluated" yaml:"events_evaluated"`
	EventsSkipped   int       `json:"events_skipped" yaml:"events_skipped"`
	StartTime       time.Time `json:"start_time" yaml:"start_time"`
	EndTime         time.Time `json:"end_time" yaml:"end_time"`
	RulesLoaded     int       `json:"rules_loaded" yaml:"rules_loaded"`
	EvaluationMode  string    `json:"evaluation_mode" yaml:"evaluation_mode"`
}

// ReportSummary provides high-level stats.
type ReportSummary struct {
	TotalAlerts        int     `json:"total_alerts" yaml:"total_alerts"`
	AlertRate          float64 `json:"alert_rate_pct" yaml:"alert_rate_pct"`
	UniqueRulesMatched int     `json:"unique_rules_matched" yaml:"unique_rules_matched"`
	BlockRate          float64 `json:"block_rate_pct" yaml:"block_rate_pct"`
}

// RuleCoverageReport shows what fraction of loaded rules fired at least once.
type RuleCoverageReport struct {
	TotalRules      int      `json:"total_rules" yaml:"total_rules"`
	MatchedRules    int      `json:"matched_rules" yaml:"matched_rules"`
	CoveragePercent float64  `json:"coverage_percent" yaml:"coverage_percent"`
	UnmatchedRules  []string `json:"unmatched_rules" yaml:"unmatched_rules"`
}

// RuleAlertCount tracks how many times a specific rule fired.
type RuleAlertCount struct {
	RuleID   string `json:"rule_id" yaml:"rule_id"`
	RuleName string `json:"rule_name" yaml:"rule_name"`
	Severity string `json:"severity" yaml:"severity"`
	Count    int    `json:"count" yaml:"count"`
}

// FPCandidate is a trace that triggered a rule but looks benign.
type FPCandidate struct {
	TraceIndex int    `json:"trace_index" yaml:"trace_index"`
	EventIndex int    `json:"event_index" yaml:"event_index"`
	ToolName   string `json:"tool_name" yaml:"tool_name"`
	Command    string `json:"command" yaml:"command"`
	RuleID     string `json:"rule_id" yaml:"rule_id"`
	RuleName   string `json:"rule_name" yaml:"rule_name"`
	Reason     string `json:"reason" yaml:"reason"`
}

// LatencyStats records evaluation timing percentiles.
type LatencyStats struct {
	P50Us int64 `json:"p50_us" yaml:"p50_us"`
	P95Us int64 `json:"p95_us" yaml:"p95_us"`
	P99Us int64 `json:"p99_us" yaml:"p99_us"`
	MaxUs int64 `json:"max_us" yaml:"max_us"`
}

// RunConfig holds all parameters for a replay run.
type RunConfig struct {
	Dataset         string
	RulesDir        string
	MaxTraces       int
	Mode            string // "audit", "enforce", "shadow"
	EnableSessions  bool
	OutputFormat    string // "json" or "yaml"
	OutputPath      string // empty = stdout
	ExportTestcases string // path for bench YAML export; empty = skip
	PageSize        int    // HF API page size (max 100)
	Verbose         bool
	// Verdict cache (library mode only; HTTP mode reads cached flag from server response).
	DisableCache bool          // when true, no cache is attached and HitCount stays 0
	CacheSize    int           // 0 → default (10000)
	CacheTTL     time.Duration // 0 → default (5m)
	// HTTP mode (alternative to library mode).
	HTTPMode  bool
	Endpoint  string
	AuthToken string
	// Test-only seam: when non-nil, replaces the default HF fetcher.
	Fetcher Fetcher `json:"-" yaml:"-"`
}
