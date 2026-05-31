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
		// HTTP mode: send requests to a running engine
		httpClient = &http.Client{Timeout: 30 * time.Second}
		slog.Info("Using HTTP mode", "endpoint", cfg.Endpoint)
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

	// Create fetcher and aggregator
	fetcher := NewHFFetcher(cfg.Dataset, cfg.PageSize)
	aggregator := NewReportAggregator(cfg.Dataset, cfg.Mode, loadedRules)

	// Stream pages and process
	offset := 0
	totalRows := 0
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
			aggregator.RecordTrace()

			events, err := adapter.Extract(row.Row)
			if err != nil {
				slog.Debug("Skipping unparseable row", "index", row.RowIdx, "error", err)
				aggregator.RecordSkip()
				continue
			}
			if len(events) == 0 {
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
						continue
					}
				} else {
					resp, evalErr := evaluator.Evaluate(req)
					if evalErr != nil {
						slog.Debug("Evaluation failed", "error", evalErr)
						continue
					}
					action = string(resp.Action)
					alerts = resp.Alerts
				}
				duration := time.Since(start)

				result := ReplayResult{
					TraceIndex:     row.RowIdx,
					EventIndex:     eventIdx,
					SessionID:      event.SessionID,
					ToolName:       event.ToolName,
					Command:        req.Fields["command"],
					Action:         action,
					Alerts:         alerts,
					EvalDurationNs: duration.Nanoseconds(),
				}
				aggregator.Record(result)
			}
		}

		offset += len(rows)
		processed := offset
		if cfg.MaxTraces > 0 && processed >= cfg.MaxTraces {
			slog.Info("Reached max traces limit", "limit", cfg.MaxTraces)
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

	report := aggregator.Report()

	slog.Info("Replay complete",
		"traces", report.Metadata.TracesProcessed,
		"events", report.Metadata.EventsEvaluated,
		"alerts", report.Summary.TotalAlerts,
		"rules_matched", report.Summary.UniqueRulesMatched,
		"coverage", fmt.Sprintf("%.1f%%", report.RuleCoverage.CoveragePercent))

	return report, nil
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
