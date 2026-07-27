package replay

import (
	"sort"
	"strings"
)

// maxMissedTraces bounds the sample of undetected malicious traces in the report.
const maxMissedTraces = 50

// traceScore accumulates one trace's evidence while its events stream past.
type traceScore struct {
	label      TraceLabel
	traceIndex int
	events     int
	alerts     int
	prodAlerts int
	tools      map[string]bool
}

// scoreAccumulator builds the labelled-corpus scoring section.
type scoreAccumulator struct {
	// traces is keyed by trace index, which is stable across a run.
	traces map[int]*traceScore
	order  []int
	seen   bool // any labelled trace observed at all
}

func newScoreAccumulator() *scoreAccumulator {
	return &scoreAccumulator{traces: map[int]*traceScore{}}
}

// Record folds one evaluated event into its trace's tally.
func (s *scoreAccumulator) Record(result ReplayResult) {
	if !result.Label.Present {
		return
	}
	s.seen = true
	t, ok := s.traces[result.TraceIndex]
	if !ok {
		t = &traceScore{
			label:      result.Label,
			traceIndex: result.TraceIndex,
			tools:      map[string]bool{},
		}
		s.traces[result.TraceIndex] = t
		s.order = append(s.order, result.TraceIndex)
	}
	t.events++
	t.alerts += len(result.Alerts)
	t.prodAlerts += len(result.ProdAlerts)
	if result.ToolName != "" && result.Kind == EventKindCall {
		t.tools[result.ToolName] = true
	}
}

// RecordEmptyTrace registers a labelled trace that yielded no evaluable events,
// so scoring denominators match the traces actually requested rather than only
// those that parsed into events.
func (s *scoreAccumulator) RecordEmptyTrace(traceIndex int, label TraceLabel) {
	if !label.Present {
		return
	}
	s.seen = true
	if _, ok := s.traces[traceIndex]; ok {
		return
	}
	s.traces[traceIndex] = &traceScore{
		label:      label,
		traceIndex: traceIndex,
		tools:      map[string]bool{},
	}
	s.order = append(s.order, traceIndex)
}

// Report produces the scoring section, or nil for an unlabelled corpus.
func (s *scoreAccumulator) Report() *ScoringReport {
	if !s.seen || len(s.traces) == 0 {
		return nil
	}

	rep := &ScoringReport{TracesLabelled: len(s.traces)}

	var (
		diag         EventAlertDiagnostics
		spread       AlertSpread
		byRisk       = map[string]*RiskSourceScore{}
		benignRisk   = map[string]*RiskSourceScore{}
		lostToReplay int
	)

	for _, idx := range s.order {
		t := s.traces[idx]
		detected := t.alerts > 0
		prodDetected := t.prodAlerts > 0

		if t.label.Malicious {
			rep.MaliciousTraces++
			diag.EventsInMaliciousTraces += t.events
			diag.AlertsInMaliciousTraces += t.alerts

			if detected {
				rep.Confusion.TruePositives++
			} else {
				rep.Confusion.FalseNegatives++
				if len(rep.MissedMalicious) < maxMissedTraces {
					rep.MissedMalicious = append(rep.MissedMalicious, MissedTrace{
						TraceIndex:  t.traceIndex,
						TraceID:     t.label.TraceID,
						RiskSource:  t.label.RiskSource,
						FailureMode: t.label.FailureMode,
						Tools:       joinSorted(t.tools, 6),
					})
				}
			}
			if prodDetected {
				rep.Production.Confusion.TruePositives++
			} else {
				rep.Production.Confusion.FalseNegatives++
			}
			if detected && !prodDetected {
				lostToReplay++
			}

			bucket(byRisk, t.label.RiskSource, detected)
		} else {
			rep.BenignTraces++
			diag.EventsInBenignTraces += t.events
			diag.AlertsInBenignTraces += t.alerts

			if detected {
				rep.Confusion.FalsePositives++
				switch {
				case t.alerts == 1:
					spread.TracesWith1Alert++
				case t.alerts <= 5:
					spread.TracesWith2To5Alerts++
				default:
					spread.TracesWith6PlusAlerts++
				}
				if t.alerts > spread.MaxAlertsInOneTrace {
					spread.MaxAlertsInOneTrace = t.alerts
				}
			} else {
				rep.Confusion.TrueNegatives++
			}
			if prodDetected {
				rep.Production.Confusion.FalsePositives++
			} else {
				rep.Production.Confusion.TrueNegatives++
			}

			bucket(benignRisk, t.label.RiskSource, detected)
		}
	}

	diag.AlertRatePer100EventsInMalicious = per100(diag.AlertsInMaliciousTraces, diag.EventsInMaliciousTraces)
	diag.AlertRatePer100EventsInBenign = per100(diag.AlertsInBenignTraces, diag.EventsInBenignTraces)

	rep.Metrics = deriveMetrics(rep.Confusion)
	rep.Production.Metrics = deriveMetrics(rep.Production.Confusion)
	rep.Production.DetectionsLostToReplayOnlyFields = lostToReplay
	rep.Production.FieldProvenance = FieldProvenanceReport()
	rep.EventDiagnostics = diag
	rep.BenignAlertSpread = spread
	rep.ByRiskSource = sortedRiskScores(byRisk)
	rep.BenignByRiskSource = sortedRiskScores(benignRisk)
	rep.SampleWarnings = sampleWarnings(rep)

	return rep
}

// minClassShare is the share of traces each class must reach before the metrics
// are treated as safe to read at face value.
const minClassShare = 0.10

// sampleWarnings flags sampling conditions that invalidate a metric rather than
// merely widening its error bars.
func sampleWarnings(rep *ScoringReport) []string {
	var out []string
	total := rep.MaliciousTraces + rep.BenignTraces
	if total == 0 {
		return nil
	}
	switch {
	case rep.BenignTraces == 0:
		out = append(out, "no benign traces in this sample: precision, accuracy and "+
			"false-positive rate are undefined or trivially 1.0. Some corpora, ATBench "+
			"included, order rows by label, so --max-traces takes a single-class prefix.")
	case float64(rep.BenignTraces)/float64(total) < minClassShare:
		out = append(out, "fewer than 10% of sampled traces are benign: precision and "+
			"false-positive rate are unreliable. Check whether the corpus is ordered by label.")
	}
	switch {
	case rep.MaliciousTraces == 0:
		out = append(out, "no malicious traces in this sample: recall is undefined.")
	case float64(rep.MaliciousTraces)/float64(total) < minClassShare:
		out = append(out, "fewer than 10% of sampled traces are malicious: recall is unreliable.")
	}
	return out
}

func bucket(m map[string]*RiskSourceScore, key string, detected bool) {
	if key == "" {
		key = "unspecified"
	}
	r, ok := m[key]
	if !ok {
		r = &RiskSourceScore{RiskSource: key}
		m[key] = r
	}
	r.Traces++
	if detected {
		r.Detected++
	}
}

// sortedRiskScores orders worst-first: lowest detection rate, then largest
// cohort, then name, so the ordering is total and stable.
func sortedRiskScores(m map[string]*RiskSourceScore) []RiskSourceScore {
	out := make([]RiskSourceScore, 0, len(m))
	for _, r := range m {
		if r.Traces > 0 {
			r.DetectionRate = float64(r.Detected) / float64(r.Traces)
		}
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DetectionRate != out[j].DetectionRate {
			return out[i].DetectionRate < out[j].DetectionRate
		}
		if out[i].Traces != out[j].Traces {
			return out[i].Traces > out[j].Traces
		}
		return out[i].RiskSource < out[j].RiskSource
	})
	return out
}

func deriveMetrics(c ConfusionMatrix) ScoringMetrics {
	var m ScoringMetrics
	if d := c.TruePositives + c.FalsePositives; d > 0 {
		m.Precision = float64(c.TruePositives) / float64(d)
	}
	if d := c.TruePositives + c.FalseNegatives; d > 0 {
		m.Recall = float64(c.TruePositives) / float64(d)
	}
	if m.Precision+m.Recall > 0 {
		m.F1 = 2 * m.Precision * m.Recall / (m.Precision + m.Recall)
	}
	if d := c.TruePositives + c.TrueNegatives + c.FalsePositives + c.FalseNegatives; d > 0 {
		m.Accuracy = float64(c.TruePositives+c.TrueNegatives) / float64(d)
	}
	if d := c.FalsePositives + c.TrueNegatives; d > 0 {
		m.FalsePositiveRate = float64(c.FalsePositives) / float64(d)
	}
	return m
}

func per100(alerts, events int) float64 {
	if events == 0 {
		return 0
	}
	return float64(alerts) / float64(events) * 100
}

func joinSorted(set map[string]bool, limit int) string {
	names := make([]string, 0, len(set))
	for k := range set {
		names = append(names, k)
	}
	sort.Strings(names)
	if len(names) > limit {
		names = append(names[:limit:limit], "...")
	}
	return strings.Join(names, ", ")
}
