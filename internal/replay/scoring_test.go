package replay

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/engine"
)

func alert(id string) engine.RuleResult {
	return engine.RuleResult{RuleID: id, RuleName: id, Matched: true}
}

// event builds one evaluated event belonging to a labelled trace.
func event(traceIdx int, malicious bool, risk string, alerts, prodAlerts []engine.RuleResult) ReplayResult {
	return ReplayResult{
		TraceIndex: traceIdx,
		Kind:       EventKindCall,
		ToolName:   "t",
		Alerts:     alerts,
		ProdAlerts: prodAlerts,
		Label: TraceLabel{
			Present:    true,
			Malicious:  malicious,
			RiskSource: risk,
			TraceID:    "trace",
		},
	}
}

func approx(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func TestScoring_UnlabelledCorpusHasNoScoringSection(t *testing.T) {
	s := newScoreAccumulator()
	s.Record(ReplayResult{TraceIndex: 0, Alerts: []engine.RuleResult{alert("r")}})
	if got := s.Report(); got != nil {
		t.Errorf("unlabelled corpus produced a scoring section: %+v", got)
	}
}

// TestScoring_IsPerTraceNotPerEvent pins the central scoring decision: one
// malicious trace with many events and one alert is a single true positive, and
// the many non-alerting events inside it are not counted as anything.
func TestScoring_IsPerTraceNotPerEvent(t *testing.T) {
	s := newScoreAccumulator()
	// Trace 0: malicious, five events, only the third alerts.
	for i := range 5 {
		var a []engine.RuleResult
		if i == 2 {
			a = []engine.RuleResult{alert("r1")}
		}
		s.Record(event(0, true, "injection", a, nil))
	}
	rep := s.Report()
	if rep.Confusion.TruePositives != 1 {
		t.Errorf("TruePositives = %d, want 1", rep.Confusion.TruePositives)
	}
	if rep.MaliciousTraces != 1 {
		t.Errorf("MaliciousTraces = %d, want 1", rep.MaliciousTraces)
	}
	approx(t, "recall", rep.Metrics.Recall, 1.0)
	// The diagnostic keeps the per-event view without calling it precision.
	if rep.EventDiagnostics.EventsInMaliciousTraces != 5 {
		t.Errorf("events = %d, want 5", rep.EventDiagnostics.EventsInMaliciousTraces)
	}
	approx(t, "alert rate per 100", rep.EventDiagnostics.AlertRatePer100EventsInMalicious, 20.0)
}

func TestScoring_ConfusionAndMetrics(t *testing.T) {
	s := newScoreAccumulator()
	hit := []engine.RuleResult{alert("r1")}

	s.Record(event(0, true, "a", hit, nil))       // TP
	s.Record(event(1, true, "a", hit, nil))       // TP
	s.Record(event(2, true, "b", nil, nil))       // FN
	s.Record(event(3, false, "benign", hit, nil)) // FP
	s.Record(event(4, false, "benign", nil, nil)) // TN
	s.Record(event(5, false, "benign", nil, nil)) // TN

	rep := s.Report()
	want := ConfusionMatrix{TruePositives: 2, FalseNegatives: 1, FalsePositives: 1, TrueNegatives: 2}
	if rep.Confusion != want {
		t.Errorf("confusion = %+v, want %+v", rep.Confusion, want)
	}
	approx(t, "precision", rep.Metrics.Precision, 2.0/3.0)
	approx(t, "recall", rep.Metrics.Recall, 2.0/3.0)
	approx(t, "f1", rep.Metrics.F1, 2.0/3.0)
	approx(t, "accuracy", rep.Metrics.Accuracy, 4.0/6.0)
	approx(t, "fpr", rep.Metrics.FalsePositiveRate, 1.0/3.0)
}

// TestScoring_ProductionReproducibleIsAStrictSubset is the provenance guarantee:
// a detection that only fires with replay-only fields must not be counted as
// production-reproducible recall.
func TestScoring_ProductionReproducibleIsAStrictSubset(t *testing.T) {
	s := newScoreAccumulator()
	hit := []engine.RuleResult{alert("r1")}

	// Detected overall and in production.
	s.Record(event(0, true, "a", hit, hit))
	// Detected only because of a replay-only field.
	s.Record(event(1, true, "a", hit, nil))
	// Missed by both.
	s.Record(event(2, true, "a", nil, nil))
	s.SetProductionScored(true)

	rep := s.Report()
	if rep.Production == nil {
		t.Fatal("production section should be present when production scoring ran")
	}
	if rep.Confusion.TruePositives != 2 {
		t.Errorf("overall TP = %d, want 2", rep.Confusion.TruePositives)
	}
	if rep.Production.Confusion.TruePositives != 1 {
		t.Errorf("production TP = %d, want 1", rep.Production.Confusion.TruePositives)
	}
	if rep.Production.DetectionsLostToReplayOnlyFields != 1 {
		t.Errorf("lost = %d, want 1", rep.Production.DetectionsLostToReplayOnlyFields)
	}
	approx(t, "recall", rep.Metrics.Recall, 2.0/3.0)
	approx(t, "production recall", rep.Production.Metrics.Recall, 1.0/3.0)
	if len(rep.Production.FieldProvenance.ReplayOnly) == 0 {
		t.Error("field provenance should list the replay-only fields")
	}
}

func TestScoring_RiskSourceBreakdownIsWorstFirst(t *testing.T) {
	s := newScoreAccumulator()
	hit := []engine.RuleResult{alert("r1")}

	// "good" detects 2/2, "bad" detects 0/2, "mid" detects 1/2.
	s.Record(event(0, true, "good", hit, nil))
	s.Record(event(1, true, "good", hit, nil))
	s.Record(event(2, true, "bad", nil, nil))
	s.Record(event(3, true, "bad", nil, nil))
	s.Record(event(4, true, "mid", hit, nil))
	s.Record(event(5, true, "mid", nil, nil))

	rep := s.Report()
	if len(rep.ByRiskSource) != 3 {
		t.Fatalf("risk sources = %d, want 3", len(rep.ByRiskSource))
	}
	wantOrder := []string{"bad", "mid", "good"}
	for i, want := range wantOrder {
		if rep.ByRiskSource[i].RiskSource != want {
			t.Errorf("position %d = %q, want %q", i, rep.ByRiskSource[i].RiskSource, want)
		}
	}
	approx(t, "bad rate", rep.ByRiskSource[0].DetectionRate, 0)
	approx(t, "good rate", rep.ByRiskSource[2].DetectionRate, 1)
}

func TestScoring_BenignAlertSpread(t *testing.T) {
	s := newScoreAccumulator()
	one := []engine.RuleResult{alert("r1")}
	three := []engine.RuleResult{alert("r1"), alert("r2"), alert("r3")}
	seven := make([]engine.RuleResult, 7)
	for i := range seven {
		seven[i] = alert("r")
	}

	s.Record(event(0, false, "benign", one, nil))
	s.Record(event(1, false, "benign", three, nil))
	s.Record(event(2, false, "benign", seven, nil))
	s.Record(event(3, false, "benign", nil, nil))

	rep := s.Report()
	got := rep.BenignAlertSpread
	if got.TracesWith1Alert != 1 || got.TracesWith2To5Alerts != 1 || got.TracesWith6PlusAlerts != 1 {
		t.Errorf("spread = %+v, want 1/1/1", got)
	}
	if got.MaxAlertsInOneTrace != 7 {
		t.Errorf("max = %d, want 7", got.MaxAlertsInOneTrace)
	}
	// Three benign traces alerted at all, so three false positives.
	if rep.Confusion.FalsePositives != 3 {
		t.Errorf("FalsePositives = %d, want 3", rep.Confusion.FalsePositives)
	}
}

// TestScoring_EmptyLabelledTraceCountsAsUndetected keeps the denominators honest
// when a trace parses to no events at all.
func TestScoring_EmptyLabelledTraceCountsAsUndetected(t *testing.T) {
	s := newScoreAccumulator()
	s.RecordEmptyTrace(0, TraceLabel{Present: true, Malicious: true, RiskSource: "a"})
	s.RecordEmptyTrace(1, TraceLabel{Present: true, Malicious: false, RiskSource: "benign"})

	rep := s.Report()
	if rep.TracesLabelled != 2 {
		t.Errorf("TracesLabelled = %d, want 2", rep.TracesLabelled)
	}
	if rep.Confusion.FalseNegatives != 1 || rep.Confusion.TrueNegatives != 1 {
		t.Errorf("confusion = %+v, want 1 FN and 1 TN", rep.Confusion)
	}
	approx(t, "recall", rep.Metrics.Recall, 0)
}

func TestCheckThresholds(t *testing.T) {
	report := &ReportData{Scoring: &ScoringReport{
		Metrics: ScoringMetrics{Recall: 0.06, Precision: 0.79},
	}}

	if got := CheckThresholds(report, RunConfig{}); got != nil {
		t.Errorf("no gates configured should yield no breaches, got %+v", got)
	}
	if got := CheckThresholds(report, RunConfig{FailUnderRecall: 0.05}); got != nil {
		t.Errorf("recall above gate should pass, got %+v", got)
	}
	got := CheckThresholds(report, RunConfig{FailUnderRecall: 0.5, FailUnderPrecision: 0.9})
	if len(got) != 2 {
		t.Fatalf("breaches = %d, want 2", len(got))
	}
	if got[0].Metric != "recall" || got[1].Metric != "precision" {
		t.Errorf("unexpected breach metrics: %+v", got)
	}
	// Gates do not apply to an unlabelled corpus.
	if b := CheckThresholds(&ReportData{}, RunConfig{FailUnderRecall: 0.9}); b != nil {
		t.Errorf("unlabelled corpus should not breach, got %+v", b)
	}
}

func TestScoring_WarnsOnSingleClassSample(t *testing.T) {
	// ATBench rows are ordered by label, so --max-traces 100 yields malicious
	// traces only and a precision of 1.0 that carries no information.
	s := newScoreAccumulator()
	hit := []engine.RuleResult{alert("r1")}
	for i := range 10 {
		s.Record(event(i, true, "a", hit, nil))
	}
	rep := s.Report()
	approx(t, "precision", rep.Metrics.Precision, 1.0)
	if len(rep.SampleWarnings) == 0 {
		t.Fatal("a sample with no benign traces must be flagged")
	}
	if rep.BenignTraces != 0 {
		t.Errorf("BenignTraces = %d, want 0", rep.BenignTraces)
	}
}

func TestScoring_BalancedSampleHasNoWarnings(t *testing.T) {
	s := newScoreAccumulator()
	hit := []engine.RuleResult{alert("r1")}
	for i := range 10 {
		s.Record(event(i, i%2 == 0, "a", hit, nil))
	}
	if got := s.Report().SampleWarnings; len(got) != 0 {
		t.Errorf("balanced sample should not warn, got %v", got)
	}
}

// TestScoring_ProductionSectionOmittedWhenNotScored is the safety net for the
// worst failure mode this report has. A zeroed production section reads as a
// full set of false negatives at recall 0.000, which is byte-identical to the
// genuine finding that no rule can fire on production fields. It must be absent
// rather than zero whenever the re-evaluation did not actually run.
func TestScoring_ProductionSectionOmittedWhenNotScored(t *testing.T) {
	s := newScoreAccumulator()
	hit := []engine.RuleResult{alert("r1")}
	s.Record(event(0, true, "a", hit, nil))
	s.Record(event(1, false, "benign", nil, nil))
	// SetProductionScored deliberately not called: the default must be safe.

	rep := s.Report()
	if rep == nil {
		t.Fatal("expected a scoring section")
	}
	if rep.Production != nil {
		t.Errorf("production section must be omitted when not scored, got %+v", rep.Production)
	}
	// The overall score is unaffected.
	approx(t, "recall", rep.Metrics.Recall, 1.0)
}

// TestScoring_ProductionSectionAbsentFromJSON checks the omission survives
// serialisation, since the report is consumed as JSON by CI and by humans.
func TestScoring_ProductionSectionAbsentFromJSON(t *testing.T) {
	s := newScoreAccumulator()
	s.Record(event(0, true, "a", []engine.RuleResult{alert("r1")}, nil))

	raw, err := json.Marshal(&ReportData{Scoring: s.Report()})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("production_reproducible")) {
		t.Errorf("unscored production section leaked into JSON: %s", raw)
	}

	s.SetProductionScored(true)
	raw, err = json.Marshal(&ReportData{Scoring: s.Report()})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte("production_reproducible")) {
		t.Error("scored production section missing from JSON")
	}
}
