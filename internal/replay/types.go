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

// EventKind distinguishes the production channel an extracted event mirrors.
//
// Production emits tool calls and tool results down two different paths, and
// only some of what a replay corpus contains has a production analogue at all.
// Recording the kind lets the report separate detections production could
// reproduce from detections that exist only because replay can see more.
type EventKind string

const (
	// EventKindCall mirrors a pre-execution tool call posted to /api/v1/evaluate.
	EventKindCall EventKind = "call"
	// EventKindResult mirrors a post-execution tool result posted to /api/v1/audit,
	// which the server scans via toolresult.DetectionFields.
	EventKindResult EventKind = "result"
	// EventKindDescription is a tool-description event synthesised from the
	// corpus's tool schema. No shipped producer emits this; it is replay-only.
	EventKindDescription EventKind = "description"
)

// ExtractedEvent is a single tool call extracted from a trace row,
// before field enrichment for the rule engine.
type ExtractedEvent struct {
	SessionID string
	Kind      EventKind // zero value ("") is treated as EventKindCall
	ToolName  string
	Args      map[string]string
	Content   string // Retrieved or output content (for semantic rules)
	FilePath  string
	URL       string

	// Response is tool output text. Populated for EventKindResult.
	Response string
	// Description is a tool's declared description. Populated for
	// EventKindDescription.
	Description string
	// ArgumentsJSON is the raw argument object re-serialised as JSON. Several
	// rules match on an `arguments` blob; no producer sends one, so this is
	// replay-only enrichment.
	ArgumentsJSON string
}

// TraceLabel carries ground-truth metadata for one trace of a labelled corpus.
type TraceLabel struct {
	// Present is false for unlabelled corpora, which suppresses the whole
	// scoring section of the report.
	Present     bool
	Malicious   bool
	TraceID     string
	RiskSource  string
	FailureMode string
}

// ReplayResult is the outcome of evaluating one extracted event.
type ReplayResult struct {
	TraceIndex     int
	EventIndex     int
	SessionID      string
	Kind           EventKind
	ToolName       string
	Command        string
	Action         string
	Alerts         []engine.RuleResult
	EvalDurationNs int64

	// ProdAlerts is the subset of rules that still fire when the event is
	// restricted to fields a shipped producer can actually supply. It is
	// computed by re-evaluating the production-shaped request, not by
	// inspecting Alerts, so it is exact rather than inferred.
	ProdAlerts []engine.RuleResult

	// Label is the ground truth for the containing trace.
	Label TraceLabel
}

// ReportData is the final aggregated report.
type ReportData struct {
	Metadata           ReportMetadata     `json:"metadata" yaml:"metadata"`
	Summary            ReportSummary      `json:"summary" yaml:"summary"`
	Scoring            *ScoringReport     `json:"scoring,omitempty" yaml:"scoring,omitempty"`
	RuleCoverage       RuleCoverageReport `json:"rule_coverage" yaml:"rule_coverage"`
	AlertsByRule       []RuleAlertCount   `json:"alerts_by_rule" yaml:"alerts_by_rule"`
	AlertsBySeverity   map[string]int     `json:"alerts_by_severity" yaml:"alerts_by_severity"`
	TopAlertingRules   []RuleAlertCount   `json:"top_alerting_rules" yaml:"top_alerting_rules"`
	FPCandidates       []FPCandidate      `json:"fp_candidates" yaml:"fp_candidates"`
	ActionDistribution map[string]int     `json:"action_distribution" yaml:"action_distribution"`
	LatencyStats       LatencyStats       `json:"latency_stats" yaml:"latency_stats"`
}

// ScoringReport holds detection quality metrics for a labelled corpus.
//
// All headline metrics are per trace, never per event. A malicious trace counts
// as detected when any event within it raises at least one alert; a benign trace
// that alerts anywhere is one false positive. Per-event precision is deliberately
// absent: the corpus labels are trace-level and a malicious trace is mostly
// benign events, so crediting every alert inside a malicious trace as a true
// positive would inflate precision exactly when the rules are behaving worst.
type ScoringReport struct {
	TracesLabelled  int             `json:"traces_labelled" yaml:"traces_labelled"`
	MaliciousTraces int             `json:"malicious_traces" yaml:"malicious_traces"`
	BenignTraces    int             `json:"benign_traces" yaml:"benign_traces"`
	Confusion       ConfusionMatrix `json:"confusion_matrix" yaml:"confusion_matrix"`
	Metrics         ScoringMetrics  `json:"metrics" yaml:"metrics"`

	// SampleWarnings flag conditions that make the metrics above unsafe to read
	// at face value, most importantly a class-skewed sample. ATBench rows are
	// ordered by label, so any --max-traces below about 450 returns malicious
	// traces only and reports a precision of 1.0 that means nothing.
	SampleWarnings []string `json:"sample_warnings,omitempty" yaml:"sample_warnings,omitempty"`

	// Production restricts every detection to rules that still fire when the
	// event carries only fields a shipped producer can supply. See
	// FieldProvenance for what that excludes and why.
	Production ProductionScoping `json:"production_reproducible" yaml:"production_reproducible"`

	// EventDiagnostics are descriptive per-event rates. They are NOT precision
	// and make no ground-truth claim about individual events.
	EventDiagnostics EventAlertDiagnostics `json:"event_diagnostics" yaml:"event_diagnostics"`

	// BenignAlertSpread separates one noisy rule firing repeatedly inside a
	// single trace from many traces each tripping once.
	BenignAlertSpread AlertSpread `json:"benign_alert_spread" yaml:"benign_alert_spread"`

	// ByRiskSource is the per-risk_source detection rate over malicious traces,
	// worst-first.
	ByRiskSource []RiskSourceScore `json:"by_risk_source" yaml:"by_risk_source"`

	// BenignByRiskSource splits false positives by whether the benign trace was
	// a clean scenario or an attacked-but-resisted one. In ATBench a label of
	// "safe" means the agent came to no harm, not that no attack was present,
	// so an alert on an attacked-but-resisted trace is not obviously an error.
	BenignByRiskSource []RiskSourceScore `json:"benign_by_risk_source" yaml:"benign_by_risk_source"`

	// MissedMalicious is a bounded sample of undetected malicious traces.
	MissedMalicious []MissedTrace `json:"missed_malicious" yaml:"missed_malicious"`
}

// ConfusionMatrix counts traces, not events.
type ConfusionMatrix struct {
	TruePositives  int `json:"true_positives" yaml:"true_positives"`
	FalsePositives int `json:"false_positives" yaml:"false_positives"`
	TrueNegatives  int `json:"true_negatives" yaml:"true_negatives"`
	FalseNegatives int `json:"false_negatives" yaml:"false_negatives"`
}

// ScoringMetrics are the standard derived rates, all per trace.
type ScoringMetrics struct {
	Precision         float64 `json:"precision" yaml:"precision"`
	Recall            float64 `json:"recall" yaml:"recall"`
	F1                float64 `json:"f1" yaml:"f1"`
	Accuracy          float64 `json:"accuracy" yaml:"accuracy"`
	FalsePositiveRate float64 `json:"false_positive_rate" yaml:"false_positive_rate"`
}

// ProductionScoping reports the same metrics restricted to detections a shipped
// producer could reproduce, alongside the provenance evidence for the restriction.
type ProductionScoping struct {
	Confusion ConfusionMatrix `json:"confusion_matrix" yaml:"confusion_matrix"`
	Metrics   ScoringMetrics  `json:"metrics" yaml:"metrics"`
	// DetectionsLostToReplayOnlyFields is the number of malicious traces
	// detected overall but not detected under production field constraints.
	DetectionsLostToReplayOnlyFields int             `json:"detections_lost_to_replay_only_fields" yaml:"detections_lost_to_replay_only_fields"`
	FieldProvenance                  FieldProvenance `json:"field_provenance" yaml:"field_provenance"`
}

// FieldProvenance records which rule-visible fields a shipped producer can
// actually cause to be populated, and which exist only under replay.
type FieldProvenance struct {
	ProductionSupplied []string `json:"production_supplied" yaml:"production_supplied"`
	ReplayOnly         []string `json:"replay_only" yaml:"replay_only"`
	Note               string   `json:"note" yaml:"note"`
}

// EventAlertDiagnostics are descriptive alert rates per 100 events, split by the
// label of the containing trace. Not precision.
type EventAlertDiagnostics struct {
	EventsInMaliciousTraces          int     `json:"events_in_malicious_traces" yaml:"events_in_malicious_traces"`
	EventsInBenignTraces             int     `json:"events_in_benign_traces" yaml:"events_in_benign_traces"`
	AlertsInMaliciousTraces          int     `json:"alerts_in_malicious_traces" yaml:"alerts_in_malicious_traces"`
	AlertsInBenignTraces             int     `json:"alerts_in_benign_traces" yaml:"alerts_in_benign_traces"`
	AlertRatePer100EventsInMalicious float64 `json:"alert_rate_per_100_events_in_malicious_traces" yaml:"alert_rate_per_100_events_in_malicious_traces"`
	AlertRatePer100EventsInBenign    float64 `json:"alert_rate_per_100_events_in_benign_traces" yaml:"alert_rate_per_100_events_in_benign_traces"`
}

// AlertSpread bucket counts of alerting traces by how many alerts each raised.
type AlertSpread struct {
	TracesWith1Alert      int `json:"traces_with_1_alert" yaml:"traces_with_1_alert"`
	TracesWith2To5Alerts  int `json:"traces_with_2_to_5_alerts" yaml:"traces_with_2_to_5_alerts"`
	TracesWith6PlusAlerts int `json:"traces_with_6_plus_alerts" yaml:"traces_with_6_plus_alerts"`
	MaxAlertsInOneTrace   int `json:"max_alerts_in_one_trace" yaml:"max_alerts_in_one_trace"`
}

// RiskSourceScore is the detection rate for one risk_source category.
type RiskSourceScore struct {
	RiskSource    string  `json:"risk_source" yaml:"risk_source"`
	Traces        int     `json:"traces" yaml:"traces"`
	Detected      int     `json:"detected" yaml:"detected"`
	DetectionRate float64 `json:"detection_rate" yaml:"detection_rate"`
}

// MissedTrace identifies a malicious trace that raised no alert.
type MissedTrace struct {
	TraceIndex  int    `json:"trace_index" yaml:"trace_index"`
	TraceID     string `json:"trace_id" yaml:"trace_id"`
	RiskSource  string `json:"risk_source" yaml:"risk_source"`
	FailureMode string `json:"failure_mode" yaml:"failure_mode"`
	Tools       string `json:"tools" yaml:"tools"`
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
	// HFConfig and HFSplit override the per-dataset defaults in datasetViews.
	HFConfig string
	HFSplit  string
	// FailUnderRecall and FailUnderPrecision are threshold gates on the
	// labelled scoring section. Zero disables the gate.
	FailUnderRecall    float64
	FailUnderPrecision float64
	// DumpFieldsPath, when set, writes one JSON object per evaluated event
	// showing the exact field maps handed to the rule engine. This is the
	// evidence needed to tell a rule-corpus blind spot from a field-mapping
	// artefact, which otherwise look identical in the summary metrics.
	DumpFieldsPath string
	// DumpFieldsLimit bounds the dump. Zero uses defaultDumpLimit.
	DumpFieldsLimit int
	// HTTP mode (alternative to library mode)
	HTTPMode  bool
	Endpoint  string
	AuthToken string
}
