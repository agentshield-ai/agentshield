package replay

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/evaluate"
	"github.com/agentshield-ai/agentshield/internal/models"
)

// fakeHFServer serves the ATBench fixtures through the Dataset Viewer shape and
// repoints hfBaseURL at itself for the duration of the test.
func fakeHFServer(t *testing.T) {
	t.Helper()
	raw, err := os.ReadFile("testdata/atbench_rows.json")
	if err != nil {
		t.Fatalf("reading fixtures: %v", err)
	}
	var fixtures []map[string]interface{}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatalf("decoding fixtures: %v", err)
	}
	rows := make([]TraceRow, 0, len(fixtures))
	for i, f := range fixtures {
		rows = append(rows, TraceRow{RowIdx: i, Row: f})
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(HFRowsResponse{Rows: rows, NumRows: len(rows)}); err != nil {
			t.Errorf("encoding rows: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	original := hfBaseURL
	hfBaseURL = srv.URL
	t.Cleanup(func() { hfBaseURL = original })
}

// fakeEngine stands in for a deployed engine over HTTP. decide is called with
// each request's fields and returns the alerts to reply with, so a test can make
// detection depend on a replay-only field.
type fakeEngine struct {
	mu       sync.Mutex
	requests []map[string]string
	decide   func(fields map[string]string) ([]engine.RuleResult, error)
}

func (f *fakeEngine) start(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req models.EvaluationRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.requests = append(f.requests, req.Fields)
		f.mu.Unlock()

		alerts, err := f.decide(req.Fields)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if encErr := json.NewEncoder(w).Encode(evaluate.EvaluationResponse{
			EventID: req.EventID,
			Action:  models.ActionLog,
			Alerts:  alerts,
		}); encErr != nil {
			t.Errorf("encoding response: %v", encErr)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (f *fakeEngine) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func httpRunConfig(endpoint string) RunConfig {
	return RunConfig{
		Dataset:   "AI45Research/ATBench",
		Mode:      "audit",
		PageSize:  10,
		HTTPMode:  true,
		Endpoint:  endpoint,
		MaxTraces: 3,
	}
}

// TestRun_HTTPMode_ProductionScoreIsRealNotZero is the regression test for the
// worst failure mode this report has. The production re-evaluation used to be
// guarded on the library-mode engine being non-nil, so under --http it never
// ran and the production section reported a full set of false negatives at
// recall 0.000, which is byte-identical to the genuine finding that no rule can
// fire on production fields. HTTP mode is exactly how someone would ask a
// deployed engine that question.
func TestRun_HTTPMode_ProductionScoreIsRealNotZero(t *testing.T) {
	fakeHFServer(t)

	// Detection depends on `description`, a field no producer sends, so the
	// production-shaped request must come back clean.
	fake := &fakeEngine{decide: func(fields map[string]string) ([]engine.RuleResult, error) {
		if fields["description"] != "" {
			return []engine.RuleResult{{RuleID: "r1", RuleName: "replay-only rule", Matched: true}}, nil
		}
		return nil, nil
	}}
	report, err := Run(httpRunConfig(fake.start(t)))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	s := report.Scoring
	if s == nil {
		t.Fatal("expected a scoring section for a labelled corpus")
	}
	if s.Production == nil {
		t.Fatal("production section absent: HTTP mode must still score production")
	}
	if s.Confusion.TruePositives == 0 {
		t.Fatal("fixture should produce at least one overall detection")
	}
	if s.Production.Confusion.TruePositives != 0 {
		t.Errorf("production TP = %d, want 0: detection depends on a replay-only field",
			s.Production.Confusion.TruePositives)
	}

	// The zero above must come from real evaluations, not from the section
	// never having run. The request count is the proof: every event costs one
	// full evaluation, plus a second production-shaped one for those that have
	// a production analogue at all. Synthetic tool_description events have none,
	// so they cost a single request.
	events := report.Metadata.EventsEvaluated
	if events == 0 {
		t.Fatal("no events evaluated")
	}
	want := events + prodEligibleEvents(t)
	if got := fake.count(); got != want {
		t.Errorf("engine saw %d requests for %d events, want %d (one per event plus one per production-eligible event)",
			got, events, want)
	}
}

// prodEligibleEvents counts the fixture events that have a production analogue,
// derived from the fixtures rather than hardcoded so it tracks the data.
func prodEligibleEvents(t *testing.T) int {
	t.Helper()
	rows := loadATBenchFixtures(t)
	n := 0
	for _, row := range rows {
		_, events, err := (&ATBenchAdapter{}).ExtractLabelled(row)
		if err != nil {
			t.Fatalf("extracting fixture: %v", err)
		}
		for _, e := range events {
			if BuildProductionRequest(e) != nil {
				n++
			}
		}
	}
	return n
}

// TestRun_HTTPMode_ProductionScoreCountsRealDetections is the other half: when
// the production-shaped request does match, the score must reflect it, so the
// zero in the test above cannot be hardcoded behaviour.
func TestRun_HTTPMode_ProductionScoreCountsRealDetections(t *testing.T) {
	fakeHFServer(t)

	// Detection depends only on `command`, which production supplies.
	fake := &fakeEngine{decide: func(fields map[string]string) ([]engine.RuleResult, error) {
		if fields["command"] != "" {
			return []engine.RuleResult{{RuleID: "r1", RuleName: "production-visible rule", Matched: true}}, nil
		}
		return nil, nil
	}}
	report, err := Run(httpRunConfig(fake.start(t)))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	s := report.Scoring
	if s.Production == nil {
		t.Fatal("production section absent")
	}
	if s.Production.Confusion.TruePositives == 0 {
		t.Error("production TP = 0, but the rule matches on a field production supplies")
	}
	if s.Production.Confusion.TruePositives != s.Confusion.TruePositives {
		t.Errorf("production TP = %d, overall TP = %d; a production-visible rule should score the same",
			s.Production.Confusion.TruePositives, s.Confusion.TruePositives)
	}
	if s.Production.DetectionsLostToReplayOnlyFields != 0 {
		t.Errorf("lost = %d, want 0", s.Production.DetectionsLostToReplayOnlyFields)
	}
}

// TestRun_HTTPMode_ProductionSectionOmittedOnEvaluationFailure covers the third
// state. A partial production score understates detections in exactly the
// direction that looks like a finding, so any failure must omit the section
// rather than report what it managed to collect.
func TestRun_HTTPMode_ProductionSectionOmittedOnEvaluationFailure(t *testing.T) {
	fakeHFServer(t)

	// Succeed on the full request, fail on the production-shaped one. The full
	// request is distinguishable by carrying replay-only enrichment.
	fake := &fakeEngine{decide: func(fields map[string]string) ([]engine.RuleResult, error) {
		replayOnly := fields["description"] != "" || fields["arguments"] != "" || fields["response_text"] != ""
		if !replayOnly {
			return nil, errEngineUnavailable
		}
		return []engine.RuleResult{{RuleID: "r1", RuleName: "replay-only rule", Matched: true}}, nil
	}}
	report, err := Run(httpRunConfig(fake.start(t)))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	s := report.Scoring
	if s == nil {
		t.Fatal("expected a scoring section")
	}
	if s.Production != nil {
		t.Errorf("production section must be omitted when its evaluations failed, got %+v", s.Production)
	}
	// The overall score is unaffected by the production path failing.
	if s.Confusion.TruePositives == 0 {
		t.Error("overall detections should survive a production-path failure")
	}

	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "production_reproducible") {
		t.Error("suppressed production section leaked into the report JSON")
	}
}

var errEngineUnavailable = errors.New("engine unavailable")
