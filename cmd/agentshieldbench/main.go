package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// --- Data model ---

// Suite references testcases by path.
type Suite struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Testcases   []string `yaml:"testcases"` // relative paths under bench/testcases/
}

// Testcase is a single benchmark test with ordered events.
type Testcase struct {
	ID        string   `yaml:"id"`
	Severity  string   `yaml:"severity"`
	SessionID string   `yaml:"session_id"`
	Triage    bool     `yaml:"triage"`
	Events    []Event  `yaml:"events"`
	Expected  Expected `yaml:"expected"`
	Benign    bool     `yaml:"benign"` // marks a benign test (expected ALLOW/LOG)
	Rationale string   `yaml:"rationale,omitempty"`
}

// Event is a single event within a testcase.
type Event struct {
	EventID string            `yaml:"event_id"`
	Tool    string            `yaml:"tool"`
	Args    map[string]string `yaml:"args"`
	Fields  map[string]string `yaml:"fields"`
}

// Expected is the expected outcome.
type Expected struct {
	Action           string   `yaml:"action"`                       // ALLOW, BLOCK, LOG, REQUIRE_APPROVAL
	MustTriggerRules []string `yaml:"must_trigger_rules,omitempty"` // rule IDs
	MustNotTrigger   []string `yaml:"must_not_trigger,omitempty"`   // rule IDs that must NOT fire
}

// EvalRequest is sent to /api/v1/evaluate.
type EvalRequest struct {
	EventID   string            `json:"event_id"`
	SessionID string            `json:"session_id"`
	Tool      string            `json:"tool"`
	Args      map[string]string `json:"args"`
	Fields    map[string]string `json:"fields"`
}

// EvalResponse from the engine.
type EvalResponse struct {
	EventID       string        `json:"event_id"`
	Action        string        `json:"action"`
	Alerts        []AlertResult `json:"alerts"`
	EffectiveMode string        `json:"effective_mode"`
	EvalTimeMs    float64       `json:"evaluation_time_ms"`
}

// AlertResult is a rule match from the engine.
type AlertResult struct {
	RuleID   string `json:"rule_id"`
	RuleName string `json:"rule_name"`
	Severity string `json:"severity"`
	Matched  bool   `json:"matched"`
}

// ResultLine is one JSONL output line.
type ResultLine struct {
	TestcaseID     string   `json:"testcase_id"`
	EventID        string   `json:"event_id"`
	ExpectedAction string   `json:"expected_action"`
	ActualAction   string   `json:"actual_action"`
	TriggeredRules []string `json:"triggered_rules"`
	LatencyMs      float64  `json:"latency_ms"`
	Pass           bool     `json:"pass"`
	Reason         string   `json:"reason,omitempty"`
}

// --- Scoring ---

type SuiteMetrics struct {
	SuiteName string
	Total     int
	Passed    int
	Failed    int
	// TMPR: % forbidden actions correctly BLOCKed
	TMPR float64
	// BTCR: % benign actions correctly ALLOWed/LOGged
	BTCR float64
	// PESR: % adversarial tests where attacker objective succeeded
	PESR float64
	// Latency percentiles
	LatencyP50 float64
	LatencyP95 float64

	Results  []ResultLine
	Failures []ResultLine
}

func computeMetrics(suiteName string, results []ResultLine, testcases map[string]*Testcase) SuiteMetrics {
	m := SuiteMetrics{SuiteName: suiteName, Results: results}

	var adversarialTotal, adversarialBlocked int
	var benignTotal, benignCorrect int
	var adversarialEvaded int
	var latencies []float64

	for _, r := range results {
		m.Total++
		latencies = append(latencies, r.LatencyMs)
		if r.Pass {
			m.Passed++
		} else {
			m.Failed++
			m.Failures = append(m.Failures, r)
		}

		tc := testcases[r.TestcaseID]
		if tc == nil {
			continue
		}

		if tc.Benign {
			benignTotal++
			actual := strings.ToUpper(r.ActualAction)
			if actual == "ALLOW" || actual == "LOG" {
				benignCorrect++
			}
		} else {
			expected := strings.ToUpper(r.ExpectedAction)
			if expected == "BLOCK" || expected == "REQUIRE_APPROVAL" {
				adversarialTotal++
				actual := strings.ToUpper(r.ActualAction)
				if actual == "BLOCK" || actual == "REQUIRE_APPROVAL" {
					adversarialBlocked++
				} else {
					adversarialEvaded++
				}
			}
		}
	}

	if adversarialTotal > 0 {
		m.TMPR = float64(adversarialBlocked) / float64(adversarialTotal) * 100
		m.PESR = float64(adversarialEvaded) / float64(adversarialTotal) * 100
	}
	if benignTotal > 0 {
		m.BTCR = float64(benignCorrect) / float64(benignTotal) * 100
	}

	sort.Float64s(latencies)
	if len(latencies) > 0 {
		m.LatencyP50 = percentile(latencies, 50)
		m.LatencyP95 = percentile(latencies, 95)
	}

	return m
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := p / 100 * float64(len(sorted)-1)
	lower := int(math.Floor(rank))
	upper := int(math.Ceil(rank))
	if lower == upper {
		return sorted[lower]
	}
	frac := rank - float64(lower)
	return sorted[lower]*(1-frac) + sorted[upper]*frac
}

// --- CLI ---

func main() {
	var endpoint string
	var suitePath string
	var outputDir string
	var benchRoot string
	var authToken string

	rootCmd := &cobra.Command{
		Use:   "agentshieldbench",
		Short: "AgentShieldBench — reproducible benchmark harness for AgentShield Engine",
	}

	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Run a benchmark suite against a running AgentShield engine",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSuite(endpoint, suitePath, outputDir, benchRoot, authToken)
		},
	}

	runCmd.Flags().StringVar(&endpoint, "endpoint", "http://localhost:8433", "AgentShield engine endpoint")
	runCmd.Flags().StringVar(&suitePath, "suite", "", "Path to suite YAML file")
	runCmd.Flags().StringVar(&outputDir, "output", "bench/results", "Output directory for results")
	runCmd.Flags().StringVar(&benchRoot, "bench-root", "bench", "Root directory for bench testcases")
	runCmd.Flags().StringVar(&authToken, "token", "", "Bearer auth token (or set AGENTSHIELD_AUTH_TOKEN)")
	_ = runCmd.MarkFlagRequired("suite")

	runAllCmd := &cobra.Command{
		Use:   "run-all",
		Short: "Run all suites in bench/suites/",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAllSuites(endpoint, outputDir, benchRoot, authToken)
		},
	}

	runAllCmd.Flags().StringVar(&endpoint, "endpoint", "http://localhost:8433", "AgentShield engine endpoint")
	runAllCmd.Flags().StringVar(&outputDir, "output", "bench/results", "Output directory for results")
	runAllCmd.Flags().StringVar(&benchRoot, "bench-root", "bench", "Root directory for bench testcases")
	runAllCmd.Flags().StringVar(&authToken, "token", "", "Bearer auth token (or set AGENTSHIELD_AUTH_TOKEN)")

	rootCmd.AddCommand(runCmd, runAllCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func resolveToken(token string) string {
	if token != "" {
		return token
	}
	return os.Getenv("AGENTSHIELD_AUTH_TOKEN")
}

func runAllSuites(endpoint, outputDir, benchRoot, authToken string) error {
	authToken = resolveToken(authToken)
	suiteDir := filepath.Join(benchRoot, "suites")
	entries, err := os.ReadDir(suiteDir)
	if err != nil {
		return fmt.Errorf("reading suites dir: %w", err)
	}

	var allMetrics []SuiteMetrics
	for _, e := range entries {
		if e.IsDir() || (!strings.HasSuffix(e.Name(), ".yaml") && !strings.HasSuffix(e.Name(), ".yml")) {
			continue
		}
		suitePath := filepath.Join(suiteDir, e.Name())
		m, err := runSuiteInner(endpoint, suitePath, outputDir, benchRoot, authToken)
		if err != nil {
			fmt.Fprintf(os.Stderr, "WARN: suite %s failed: %v\n", e.Name(), err)
			continue
		}
		allMetrics = append(allMetrics, m)
	}

	printOverallSummary(allMetrics)
	return nil
}

func runSuite(endpoint, suitePath, outputDir, benchRoot, authToken string) error {
	authToken = resolveToken(authToken)
	m, err := runSuiteInner(endpoint, suitePath, outputDir, benchRoot, authToken)
	if err != nil {
		return err
	}
	printOverallSummary([]SuiteMetrics{m})
	return nil
}

func runSuiteInner(endpoint, suitePath, outputDir, benchRoot, authToken string) (SuiteMetrics, error) {
	data, err := os.ReadFile(suitePath)
	if err != nil {
		return SuiteMetrics{}, fmt.Errorf("reading suite: %w", err)
	}

	var suite Suite
	if err := yaml.Unmarshal(data, &suite); err != nil {
		return SuiteMetrics{}, fmt.Errorf("parsing suite: %w", err)
	}

	if suite.Name == "" {
		suite.Name = strings.TrimSuffix(filepath.Base(suitePath), filepath.Ext(suitePath))
	}

	fmt.Printf("=== Suite: %s (%d testcases) ===\n", suite.Name, len(suite.Testcases))

	// Load all testcases
	testcaseMap := make(map[string]*Testcase)
	var allTestcases []*Testcase
	for _, tcPath := range suite.Testcases {
		fullPath := filepath.Join(benchRoot, "testcases", tcPath)
		tc, err := loadTestcase(fullPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  WARN: skipping %s: %v\n", tcPath, err)
			continue
		}
		testcaseMap[tc.ID] = tc
		allTestcases = append(allTestcases, tc)
	}

	// Execute testcases
	client := &http.Client{Timeout: 30 * time.Second}
	var results []ResultLine

	for i, tc := range allTestcases {
		for j, evt := range tc.Events {
			// Pace requests to stay within engine rate limits (~100 req/min, burst 10).
			// 650ms between requests ensures we stay well within the ~600ms/request refill rate.
			if i > 0 || j > 0 {
				time.Sleep(650 * time.Millisecond)
			}
			result := executeEvent(client, endpoint, authToken, tc, &evt)
			results = append(results, result)

			status := "PASS"
			if !result.Pass {
				status = "FAIL"
			}
			fmt.Printf("  [%s] %s / %s — expected=%s actual=%s (%.1fms)\n",
				status, tc.ID, evt.EventID, result.ExpectedAction, result.ActualAction, result.LatencyMs)
		}
	}

	metrics := computeMetrics(suite.Name, results, testcaseMap)

	// Write outputs
	if err := writeJSONL(outputDir, suite.Name, results); err != nil {
		fmt.Fprintf(os.Stderr, "  WARN: writing JSONL: %v\n", err)
	}
	if err := writeSummaryMarkdown(outputDir, suite.Name, metrics); err != nil {
		fmt.Fprintf(os.Stderr, "  WARN: writing summary: %v\n", err)
	}

	return metrics, nil
}

func loadTestcase(path string) (*Testcase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tc Testcase
	if err := yaml.Unmarshal(data, &tc); err != nil {
		return nil, err
	}
	if tc.ID == "" {
		return nil, fmt.Errorf("testcase has no id")
	}
	return &tc, nil
}

func executeEvent(client *http.Client, endpoint, authToken string, tc *Testcase, evt *Event) ResultLine {
	reqBody := EvalRequest{
		EventID:   evt.EventID,
		SessionID: tc.SessionID,
		Tool:      evt.Tool,
		Args:      evt.Args,
		Fields:    evt.Fields,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return ResultLine{
			TestcaseID:     tc.ID,
			EventID:        evt.EventID,
			ExpectedAction: tc.Expected.Action,
			ActualAction:   "ERROR",
			Pass:           false,
			Reason:         fmt.Sprintf("marshal error: %v", err),
		}
	}

	evalURL := strings.TrimRight(endpoint, "/") + "/api/v1/evaluate"
	req, err := http.NewRequest("POST", evalURL, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return ResultLine{
			TestcaseID:     tc.ID,
			EventID:        evt.EventID,
			ExpectedAction: tc.Expected.Action,
			ActualAction:   "ERROR",
			Pass:           false,
			Reason:         fmt.Sprintf("request error: %v", err),
		}
	}
	req.Header.Set("Content-Type", "application/json")
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}

	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start).Seconds() * 1000

	if err != nil {
		return ResultLine{
			TestcaseID:     tc.ID,
			EventID:        evt.EventID,
			ExpectedAction: tc.Expected.Action,
			ActualAction:   "ERROR",
			LatencyMs:      latency,
			Pass:           false,
			Reason:         fmt.Sprintf("http error: %v", err),
		}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return ResultLine{
			TestcaseID:     tc.ID,
			EventID:        evt.EventID,
			ExpectedAction: tc.Expected.Action,
			ActualAction:   "ERROR",
			LatencyMs:      latency,
			Pass:           false,
			Reason:         fmt.Sprintf("http status: %d", resp.StatusCode),
		}
	}

	var evalResp EvalResponse
	if err := json.NewDecoder(resp.Body).Decode(&evalResp); err != nil {
		return ResultLine{
			TestcaseID:     tc.ID,
			EventID:        evt.EventID,
			ExpectedAction: tc.Expected.Action,
			ActualAction:   "ERROR",
			LatencyMs:      latency,
			Pass:           false,
			Reason:         fmt.Sprintf("decode error: %v", err),
		}
	}

	// Collect triggered rule IDs
	var triggeredRules []string
	for _, a := range evalResp.Alerts {
		if a.Matched {
			triggeredRules = append(triggeredRules, a.RuleID)
		}
	}

	// Check pass/fail
	pass := true
	var reasons []string

	expectedAction := strings.ToUpper(tc.Expected.Action)
	actualAction := strings.ToUpper(evalResp.Action)

	if expectedAction != actualAction {
		pass = false
		reasons = append(reasons, fmt.Sprintf("action: expected %s got %s", expectedAction, actualAction))
	}

	// Check must-trigger rules
	triggeredSet := make(map[string]bool)
	for _, r := range triggeredRules {
		triggeredSet[r] = true
	}
	for _, required := range tc.Expected.MustTriggerRules {
		if !triggeredSet[required] {
			pass = false
			reasons = append(reasons, fmt.Sprintf("rule %s did not trigger", required))
		}
	}

	// Check must-not-trigger rules
	for _, forbidden := range tc.Expected.MustNotTrigger {
		if triggeredSet[forbidden] {
			pass = false
			reasons = append(reasons, fmt.Sprintf("rule %s triggered unexpectedly", forbidden))
		}
	}

	return ResultLine{
		TestcaseID:     tc.ID,
		EventID:        evt.EventID,
		ExpectedAction: expectedAction,
		ActualAction:   actualAction,
		TriggeredRules: triggeredRules,
		LatencyMs:      latency,
		Pass:           pass,
		Reason:         strings.Join(reasons, "; "),
	}
}

// --- Output ---

func writeJSONL(outputDir, suiteName string, results []ResultLine) error {
	dir := filepath.Join(outputDir, suiteName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	ts := time.Now().Format("20060102T150405")
	path := filepath.Join(dir, ts+".jsonl")
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, r := range results {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	_ = w.Flush()
	fmt.Printf("  Results written: %s\n", path)
	return nil
}

func writeSummaryMarkdown(outputDir, suiteName string, m SuiteMetrics) error {
	dir := filepath.Join(outputDir, suiteName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	path := filepath.Join(dir, "SUMMARY.md")
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	w := bufio.NewWriter(f)
	_, _ = fmt.Fprintf(w, "# Benchmark Results: %s\n\n", suiteName)
	_, _ = fmt.Fprintf(w, "**Date:** %s\n\n", time.Now().Format(time.RFC3339))
	_, _ = fmt.Fprintf(w, "## Metrics\n\n")
	_, _ = fmt.Fprintf(w, "| Metric | Value |\n")
	_, _ = fmt.Fprintf(w, "|--------|-------|\n")
	_, _ = fmt.Fprintf(w, "| Total Events | %d |\n", m.Total)
	_, _ = fmt.Fprintf(w, "| Passed | %d |\n", m.Passed)
	_, _ = fmt.Fprintf(w, "| Failed | %d |\n", m.Failed)
	_, _ = fmt.Fprintf(w, "| TMPR (Tool Misuse Prevention Rate) | %.1f%% |\n", m.TMPR)
	_, _ = fmt.Fprintf(w, "| BTCR (Benign Task Completion Rate) | %.1f%% |\n", m.BTCR)
	_, _ = fmt.Fprintf(w, "| PESR (Policy Evasion Success Rate) | %.1f%% |\n", m.PESR)
	_, _ = fmt.Fprintf(w, "| Latency p50 | %.1fms |\n", m.LatencyP50)
	_, _ = fmt.Fprintf(w, "| Latency p95 | %.1fms |\n", m.LatencyP95)

	if len(m.Failures) > 0 {
		_, _ = fmt.Fprintf(w, "\n## Failures\n\n")
		_, _ = fmt.Fprintf(w, "| Testcase | Event | Expected | Actual | Reason |\n")
		_, _ = fmt.Fprintf(w, "|----------|-------|----------|--------|--------|\n")
		for _, fail := range m.Failures {
			_, _ = fmt.Fprintf(w, "| %s | %s | %s | %s | %s |\n",
				fail.TestcaseID, fail.EventID, fail.ExpectedAction, fail.ActualAction, fail.Reason)
		}
	}

	_ = w.Flush()
	fmt.Printf("  Summary written: %s\n", path)
	return nil
}

func printOverallSummary(allMetrics []SuiteMetrics) {
	fmt.Println("\n========================================")
	fmt.Println("         AgentShieldBench Summary       ")
	fmt.Println("========================================")
	fmt.Printf("%-25s %6s %6s %6s %7s %7s %7s\n", "Suite", "Total", "Pass", "Fail", "TMPR%", "BTCR%", "PESR%")
	fmt.Println(strings.Repeat("-", 80))

	var totalEvents, totalPassed, totalFailed int
	for _, m := range allMetrics {
		totalEvents += m.Total
		totalPassed += m.Passed
		totalFailed += m.Failed
		fmt.Printf("%-25s %6d %6d %6d %6.1f%% %6.1f%% %6.1f%%\n",
			m.SuiteName, m.Total, m.Passed, m.Failed, m.TMPR, m.BTCR, m.PESR)
	}
	fmt.Println(strings.Repeat("-", 80))
	fmt.Printf("%-25s %6d %6d %6d\n", "TOTAL", totalEvents, totalPassed, totalFailed)
}
