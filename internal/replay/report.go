package replay

import (
	"sort"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/engine"
)

// ReportAggregator accumulates replay results and produces a summary report.
type ReportAggregator struct {
	dataset    string
	mode       string
	rulesLoaded map[string]engine.RuleResult // ruleID -> rule info
	startTime  time.Time

	tracesProcessed int
	eventsEvaluated int
	eventsSkipped   int
	totalAlerts     int

	ruleHitCounts   map[string]int  // ruleID -> count
	severityCounts  map[string]int  // severity -> count
	actionCounts    map[string]int  // action -> count
	matchedRules    map[string]bool // ruleID -> true if matched at least once
	latencies       []int64         // nanoseconds per evaluation

	fpCandidates []FPCandidate // bounded to maxFPCandidates
}

const maxFPCandidates = 100
const maxTopRules = 20

// NewReportAggregator creates an aggregator seeded with the full rule list.
func NewReportAggregator(dataset, mode string, loadedRules []engine.RuleResult) *ReportAggregator {
	rulesMap := make(map[string]engine.RuleResult, len(loadedRules))
	for _, r := range loadedRules {
		rulesMap[r.RuleID] = r
	}
	return &ReportAggregator{
		dataset:        dataset,
		mode:           mode,
		rulesLoaded:    rulesMap,
		startTime:      time.Now(),
		ruleHitCounts:  make(map[string]int),
		severityCounts: make(map[string]int),
		actionCounts:   make(map[string]int),
		matchedRules:   make(map[string]bool),
	}
}

// RecordTrace increments the trace counter.
func (a *ReportAggregator) RecordTrace() {
	a.tracesProcessed++
}

// RecordSkip increments the skipped event counter.
func (a *ReportAggregator) RecordSkip() {
	a.eventsSkipped++
}

// Record adds a single evaluation result to the aggregator.
func (a *ReportAggregator) Record(result ReplayResult) {
	a.eventsEvaluated++
	a.actionCounts[result.Action]++
	a.latencies = append(a.latencies, result.EvalDurationNs)

	for _, alert := range result.Alerts {
		a.totalAlerts++
		a.ruleHitCounts[alert.RuleID]++
		a.severityCounts[string(alert.Severity)]++
		a.matchedRules[alert.RuleID] = true

		// Check if this is an FP candidate
		if reason := isFPCandidate(result); reason != "" && len(a.fpCandidates) < maxFPCandidates {
			a.fpCandidates = append(a.fpCandidates, FPCandidate{
				TraceIndex: result.TraceIndex,
				EventIndex: result.EventIndex,
				ToolName:   result.ToolName,
				Command:    truncate(result.Command, 200),
				RuleID:     alert.RuleID,
				RuleName:   alert.RuleName,
				Reason:     reason,
			})
		}
	}
}

// Report generates the final report.
func (a *ReportAggregator) Report() *ReportData {
	endTime := time.Now()

	// Build rule coverage
	var unmatchedRules []string
	for ruleID := range a.rulesLoaded {
		if !a.matchedRules[ruleID] {
			unmatchedRules = append(unmatchedRules, ruleID)
		}
	}
	sort.Strings(unmatchedRules)

	totalRules := len(a.rulesLoaded)
	matchedCount := len(a.matchedRules)
	coveragePct := 0.0
	if totalRules > 0 {
		coveragePct = float64(matchedCount) / float64(totalRules) * 100
	}

	// Build alerts-by-rule (sorted by count desc)
	var alertsByRule []RuleAlertCount
	for ruleID, count := range a.ruleHitCounts {
		rule := a.rulesLoaded[ruleID]
		alertsByRule = append(alertsByRule, RuleAlertCount{
			RuleID:   ruleID,
			RuleName: rule.RuleName,
			Severity: string(rule.Severity),
			Count:    count,
		})
	}
	sort.Slice(alertsByRule, func(i, j int) bool {
		return alertsByRule[i].Count > alertsByRule[j].Count
	})

	// Top alerting rules
	topRules := alertsByRule
	if len(topRules) > maxTopRules {
		topRules = topRules[:maxTopRules]
	}

	// Alert rate
	alertRate := 0.0
	if a.eventsEvaluated > 0 {
		alertRate = float64(a.totalAlerts) / float64(a.eventsEvaluated) * 100
	}

	// Block rate
	blockCount := a.actionCounts["block"]
	blockRate := 0.0
	if a.eventsEvaluated > 0 {
		blockRate = float64(blockCount) / float64(a.eventsEvaluated) * 100
	}

	return &ReportData{
		Metadata: ReportMetadata{
			Dataset:         a.dataset,
			TracesProcessed: a.tracesProcessed,
			EventsEvaluated: a.eventsEvaluated,
			EventsSkipped:   a.eventsSkipped,
			StartTime:       a.startTime,
			EndTime:         endTime,
			RulesLoaded:     totalRules,
			EvaluationMode:  a.mode,
		},
		Summary: ReportSummary{
			TotalAlerts:        a.totalAlerts,
			AlertRate:          alertRate,
			UniqueRulesMatched: matchedCount,
			BlockRate:          blockRate,
		},
		RuleCoverage: RuleCoverageReport{
			TotalRules:      totalRules,
			MatchedRules:    matchedCount,
			CoveragePercent: coveragePct,
			UnmatchedRules:  unmatchedRules,
		},
		AlertsByRule:       alertsByRule,
		AlertsBySeverity:   a.severityCounts,
		TopAlertingRules:   topRules,
		FPCandidates:       a.fpCandidates,
		ActionDistribution: a.actionCounts,
		LatencyStats:       computeLatencyStats(a.latencies),
	}
}

// isFPCandidate checks if a result that triggered alerts looks like a benign operation.
func isFPCandidate(result ReplayResult) string {
	cmd := strings.ToLower(result.Command)
	tool := strings.ToLower(result.ToolName)

	// Common benign dev commands that often trigger rules
	benignPrefixes := []string{
		"git ", "npm ", "go ", "make ", "cargo ", "pip ",
		"ls ", "cat ", "echo ", "cd ", "pwd", "mkdir ",
		"read: ", "write: ", "edit: ", "glob: ", "grep: ",
	}
	for _, prefix := range benignPrefixes {
		if strings.HasPrefix(cmd, prefix) {
			return "common dev command: " + prefix
		}
	}

	// Read of non-sensitive files
	if (tool == "read" || tool == "file_read") && !isSensitivePath(cmd) {
		return "read of non-sensitive path"
	}

	// Content retrieval from well-known benign domains
	if tool == "web_fetch" || tool == "fetch" || tool == "http" || tool == "webfetch" {
		benignDomains := []string{
			"github.com", "arxiv.org", "stackoverflow.com", "docs.python.org",
			"developer.mozilla.org", "wikipedia.org", "npmjs.com", "pypi.org",
			"golang.org", "pkg.go.dev", "crates.io", "rubygems.org",
		}
		for _, domain := range benignDomains {
			if strings.Contains(cmd, domain) {
				return "content retrieval from known-benign domain: " + domain
			}
		}
	}

	// Heredoc file creation (cat > file.py << 'EOF') is writing, not executing
	if strings.HasPrefix(cmd, "cat ") && strings.Contains(cmd, "<<") {
		return "heredoc file creation, not execution"
	}

	return ""
}

// isSensitivePath checks if a path targets sensitive files.
func isSensitivePath(path string) bool {
	sensitivePatterns := []string{
		"/etc/shadow", "/etc/passwd", ".env", ".aws/", ".ssh/",
		"credentials", "secret", "token", "password", ".git/config",
	}
	lower := strings.ToLower(path)
	for _, pattern := range sensitivePatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// computeLatencyStats calculates P50, P95, P99, and Max from a slice of nanosecond durations.
func computeLatencyStats(latencies []int64) LatencyStats {
	if len(latencies) == 0 {
		return LatencyStats{}
	}
	sorted := make([]int64, len(latencies))
	copy(sorted, latencies)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	percentile := func(p float64) int64 {
		idx := int(float64(len(sorted)-1) * p)
		return sorted[idx] / 1000 // ns -> us
	}

	return LatencyStats{
		P50Us: percentile(0.50),
		P95Us: percentile(0.95),
		P99Us: percentile(0.99),
		MaxUs: sorted[len(sorted)-1] / 1000,
	}
}
