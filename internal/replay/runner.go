package replay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/evaluate"
	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/agentshield-ai/agentshield/internal/session"
)

// Run executes the full replay pipeline: fetch → extract → evaluate → report.
func Run(cfg RunConfig) (*ReportData, error) {
	mode := parseMode(cfg.Mode)

	// Select adapter
	adapter, err := SelectAdapter(cfg.Dataset)
	if err != nil {
		return nil, fmt.Errorf("selecting adapter: %w", err)
	}
	slog.Info("Selected adapter", "adapter", adapter.Name(), "dataset", cfg.Dataset)

	var evaluator *evaluate.Evaluator
	var eng *engine.Engine
	var httpClient *http.Client

	if cfg.HTTPMode {
		// HTTP mode: send requests to a running engine. Each event costs two
		// requests, the full one and the production-shaped one, so the engine
		// sees twice the traffic a naive reading of --max-traces suggests.
		httpClient = &http.Client{Timeout: 30 * time.Second}
		slog.Info("Using HTTP mode", "endpoint", cfg.Endpoint,
			"requests_per_event", 2,
			"note", "each event is evaluated twice: full fields, then production-shaped")
	} else {
		// Library mode: load engine directly
		eng, err = engine.NewEngine(cfg.RulesDir)
		if err != nil {
			return nil, fmt.Errorf("loading rules: %w", err)
		}
		evaluator = evaluate.NewEvaluator(eng, mode, "", nil, nil)

		if cfg.EnableSessions {
			reg := session.NewRegistry(50, 15*time.Minute)
			evaluator.SetSessionRegistry(reg)
			slog.Info("Session tracking enabled")
		}
		slog.Info("Using library mode", "rules_loaded", len(eng.GetLoadedRules()))
	}

	// Get loaded rules for coverage tracking
	var loadedRules []engine.RuleResult
	if eng != nil {
		loadedRules = eng.GetLoadedRules()
	}

	// Clamp the page size to what was actually asked for, so --max-traces 10
	// fetches ten rows rather than a hundred.
	pageSize := cfg.PageSize
	if pageSize <= 0 || pageSize > maxPageSize {
		pageSize = defaultPageSize
	}
	if cfg.MaxTraces > 0 && cfg.MaxTraces < pageSize {
		pageSize = cfg.MaxTraces
	}

	// Create fetcher and aggregator
	fetcher := NewHFFetcherWithView(cfg.Dataset, pageSize, cfg.HFConfig, cfg.HFSplit)
	hfConfig, hfSplit := fetcher.View()
	slog.Info("Dataset view", "config", hfConfig, "split", hfSplit, "page_size", pageSize)
	aggregator := NewReportAggregator(cfg.Dataset, cfg.Mode, loadedRules)

	labelled, _ := adapter.(LabelledAdapter)

	dumper, err := newFieldDumper(cfg.DumpFieldsPath, cfg.DumpFieldsLimit)
	if err != nil {
		return nil, fmt.Errorf("opening field dump: %w", err)
	}
	defer dumper.Close()

	// Stream pages and process
	offset := 0
	totalRows := 0
	processed := 0
	limitReached := false
	prodEvalFailures := 0
	droppedEvents := 0
	for {
		rows, numTotal, err := fetcher.FetchPage(offset)
		if err != nil {
			return nil, fmt.Errorf("fetching page at offset %d: %w", offset, err)
		}
		if totalRows == 0 {
			totalRows = numTotal
			slog.Info("Dataset info", "total_rows", numTotal)
		}

		for _, row := range rows {
			// Enforce the trace limit per row, not per page, so the scoring
			// denominators match what was requested.
			if cfg.MaxTraces > 0 && processed >= cfg.MaxTraces {
				limitReached = true
				break
			}
			processed++
			aggregator.RecordTrace()

			var (
				label  TraceLabel
				events []ExtractedEvent
			)
			if labelled != nil {
				label, events, err = labelled.ExtractLabelled(row.Row)
			} else {
				events, err = adapter.Extract(row.Row)
			}
			if err != nil {
				slog.Debug("Skipping unparseable row", "index", row.RowIdx, "error", err)
				aggregator.RecordSkip()
				continue
			}
			if len(events) == 0 {
				// A labelled trace with no events is still a trace that was
				// asked about, and counts as undetected.
				aggregator.RecordLabelledTrace(row.RowIdx, label)
				aggregator.RecordSkip()
				continue
			}

			for eventIdx, event := range events {
				req := BuildEvaluationRequest(event)

				start := time.Now()
				var action string
				var alerts []engine.RuleResult

				if cfg.HTTPMode {
					action, alerts, err = evaluateHTTP(httpClient, cfg.Endpoint, cfg.AuthToken, req)
					if err != nil {
						slog.Debug("HTTP evaluation failed", "error", err)
						droppedEvents++
						continue
					}
				} else {
					resp, evalErr := evaluator.Evaluate(req)
					if evalErr != nil {
						slog.Debug("Evaluation failed", "error", evalErr)
						droppedEvents++
						continue
					}
					action = string(resp.Action)
					alerts = resp.Alerts
				}
				duration := time.Since(start)

				// Re-evaluate the production-shaped request. Whether a detection
				// survives this is what separates real-world recall from recall
				// that depends on fields no producer sends.
				//
				// This must happen in both modes. HTTP mode is precisely how
				// someone would point the tool at a deployed engine to ask the
				// production question, and skipping it there would report a full
				// set of false negatives at recall 0.000, which is
				// byte-identical to a real finding.
				var prodAlerts []engine.RuleResult
				var prodReq *models.EvaluationRequest
				if prodReq = BuildProductionRequest(event); prodReq != nil {
					if cfg.HTTPMode {
						_, pa, perr := evaluateHTTP(httpClient, cfg.Endpoint, cfg.AuthToken, prodReq)
						if perr != nil {
							// A partial production score understates detections
							// in exactly the direction that looks like a
							// finding, so one failure disqualifies the section.
							prodEvalFailures++
							slog.Debug("Production HTTP evaluation failed", "error", perr)
						} else {
							prodAlerts = matchedOnly(pa)
						}
					} else {
						prodAlerts = matchedOnly(eng.Evaluate(prodReq.Fields))
					}
				}

				result := ReplayResult{
					TraceIndex:     row.RowIdx,
					EventIndex:     eventIdx,
					SessionID:      event.SessionID,
					Kind:           kindOf(event),
					ToolName:       event.ToolName,
					Command:        req.Fields["command"],
					Action:         action,
					Alerts:         alerts,
					ProdAlerts:     prodAlerts,
					EvalDurationNs: duration.Nanoseconds(),
					Label:          label,
				}
				aggregator.Record(result)
				dumper.Write(result, req, prodReq)
			}
		}

		offset += len(rows)
		if limitReached || (cfg.MaxTraces > 0 && processed >= cfg.MaxTraces) {
			slog.Info("Reached max traces limit", "limit", cfg.MaxTraces, "traces", processed)
			break
		}
		if offset >= numTotal || len(rows) == 0 {
			break
		}

		if cfg.Verbose {
			slog.Info("Progress", "processed", processed, "total", numTotal,
				"pct", fmt.Sprintf("%.1f%%", float64(processed)/float64(numTotal)*100))
		}

		// Respect HF API rate limits
		time.Sleep(PageDelay())
	}

	// Production scoring is only claimed when every eligible event was actually
	// re-evaluated. Anything less is omitted rather than reported as zeroes.
	if prodEvalFailures > 0 {
		slog.Warn("Production scoring suppressed: some production-shaped evaluations failed",
			"failures", prodEvalFailures,
			"detail", "the production_reproducible section is omitted rather than under-reported")
	}
	aggregator.SetProductionScored(prodEvalFailures == 0)
	if droppedEvents > 0 {
		slog.Warn("Events dropped without evaluation",
			"dropped", droppedEvents,
			"detail", "a malicious trace whose events were dropped is scored as undetected, so recall is a lower bound")
	}
	aggregator.SetDroppedEvents(droppedEvents)

	report := aggregator.Report()

	slog.Info("Replay complete",
		"traces", report.Metadata.TracesProcessed,
		"events", report.Metadata.EventsEvaluated,
		"alerts", report.Summary.TotalAlerts,
		"rules_matched", report.Summary.UniqueRulesMatched,
		"coverage", fmt.Sprintf("%.1f%%", report.RuleCoverage.CoveragePercent))

	if s := report.Scoring; s != nil {
		// "not scored" rather than a number, because 0.000 here would read as
		// the finding this report exists to make.
		prodRecall := "not scored"
		if s.Production != nil {
			prodRecall = fmt.Sprintf("%.3f", s.Production.Metrics.Recall)
		}
		slog.Info("Scoring (per trace)",
			"malicious", s.MaliciousTraces,
			"benign", s.BenignTraces,
			"recall", fmt.Sprintf("%.3f", s.Metrics.Recall),
			"precision", fmt.Sprintf("%.3f", s.Metrics.Precision),
			"f1", fmt.Sprintf("%.3f", s.Metrics.F1),
			"recall_production_reproducible", prodRecall)
		for _, w := range s.SampleWarnings {
			slog.Warn("Scoring sample warning", "detail", w)
		}
	}

	return report, nil
}

// matchedOnly filters engine results down to actual matches, mirroring the
// filter the server applies to its own detection-only audit scan.
func matchedOnly(results []engine.RuleResult) []engine.RuleResult {
	var out []engine.RuleResult
	for _, r := range results {
		if r.Matched {
			out = append(out, r)
		}
	}
	return out
}

// ThresholdBreach describes a --fail-under-* gate that was not met.
type ThresholdBreach struct {
	Metric    string
	Threshold float64
	Actual    float64
}

// CheckThresholds reports any recall or precision gate the report failed. It
// returns nil for an unlabelled corpus, where the gates do not apply.
func CheckThresholds(report *ReportData, cfg RunConfig) []ThresholdBreach {
	if report == nil || report.Scoring == nil {
		return nil
	}
	var breaches []ThresholdBreach
	if cfg.FailUnderRecall > 0 && report.Scoring.Metrics.Recall < cfg.FailUnderRecall {
		breaches = append(breaches, ThresholdBreach{
			Metric: "recall", Threshold: cfg.FailUnderRecall, Actual: report.Scoring.Metrics.Recall,
		})
	}
	if cfg.FailUnderPrecision > 0 && report.Scoring.Metrics.Precision < cfg.FailUnderPrecision {
		breaches = append(breaches, ThresholdBreach{
			Metric: "precision", Threshold: cfg.FailUnderPrecision, Actual: report.Scoring.Metrics.Precision,
		})
	}
	return breaches
}

// evaluateHTTP sends an evaluation request to the engine HTTP API.
func evaluateHTTP(client *http.Client, endpoint, token string, req *models.EvaluationRequest) (string, []engine.RuleResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return "", nil, err
	}

	url := endpoint + "/api/v1/evaluate"
	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return "", nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", nil, fmt.Errorf("engine returned %d: %s", resp.StatusCode, string(respBody))
	}

	var evalResp evaluate.EvaluationResponse
	if err := json.NewDecoder(resp.Body).Decode(&evalResp); err != nil {
		return "", nil, fmt.Errorf("decoding response: %w", err)
	}

	return string(evalResp.Action), evalResp.Alerts, nil
}

func parseMode(s string) config.EvaluationMode {
	switch s {
	case "enforce":
		return config.ModeEnforce
	case "shadow":
		return config.ModeShadow
	default:
		return config.ModeAudit
	}
}
