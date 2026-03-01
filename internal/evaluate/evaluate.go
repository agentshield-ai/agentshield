package evaluate

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/agentshield-ai/agentshield/internal/reputation"
	"github.com/agentshield-ai/agentshield/internal/triage"
)

// RuleEvaluator is the interface for rule evaluation
type RuleEvaluator interface {
	Evaluate(fields map[string]string) []engine.RuleResult
}

// TriageService is the interface for triage services
type TriageService interface {
	ShouldTriage(alert engine.RuleResult) bool
	TriageAlerts(ctx context.Context, alerts []engine.RuleResult, req *models.EvaluationRequest) ([]triage.TriageResult, error)
	HealthCheck(ctx context.Context) error
}

// EvaluationResponse represents the response from evaluation
type EvaluationResponse struct {
	EventID       string                `json:"event_id"`
	Action        models.Action         `json:"action"`
	Alerts        []engine.RuleResult   `json:"alerts"`
	TriageResults []triage.TriageResult `json:"triage_results,omitempty"`
	Overridable   bool                  `json:"overridable"`
	EffectiveMode config.EvaluationMode `json:"effective_mode"`
	FeedbackURL   string                `json:"feedback_url,omitempty"`
	Timestamp     time.Time             `json:"timestamp"`
}

// DeepTriageService is the interface for async deep triage
type DeepTriageService interface {
	ShouldDeepTriage(alert engine.RuleResult) bool
	InvestigateAsync(alerts []engine.RuleResult, req *models.EvaluationRequest, fastResults []triage.TriageResult)
}

// Evaluator handles the evaluation of events according to different modes
type Evaluator struct {
	engine          RuleEvaluator
	defaultMode     config.EvaluationMode
	feedbackURLBase string
	triager         TriageService
	deepTriager     DeepTriageService
	reputation      *reputation.Checker
}

// NewEvaluator creates a new evaluator
func NewEvaluator(eng RuleEvaluator, defaultMode config.EvaluationMode, feedbackURLBase string, triager TriageService, deepTriager DeepTriageService) *Evaluator {
	return &Evaluator{
		engine:          eng,
		defaultMode:     defaultMode,
		feedbackURLBase: feedbackURLBase,
		triager:         triager,
		deepTriager:     deepTriager,
	}
}

// SetReputation configures an optional reputation checker. When set, URLs
// found in tool call arguments are checked after Sigma rule evaluation.
func (e *Evaluator) SetReputation(checker *reputation.Checker) {
	e.reputation = checker
}

// Evaluate processes an evaluation request and returns the appropriate response
func (e *Evaluator) Evaluate(req *models.EvaluationRequest) (*EvaluationResponse, error) {
	// Determine effective evaluation mode (server-side only)
	effectiveMode := e.determineEffectiveMode()

	// Run rule evaluation (engine now only returns matched rules)
	alerts := e.engine.Evaluate(req.Fields)

	// Run reputation checks on any URLs found in the request fields.
	// Failures are logged but never block the request (fail-open).
	if e.reputation != nil && req.Fields != nil {
		reputationAlerts := e.checkReputation(req)
		alerts = append(alerts, reputationAlerts...)
	}

	// Count severity levels for decision making
	var criticalCount, highCount int
	for _, result := range alerts {
		switch result.Severity {
		case engine.SeverityCritical:
			criticalCount++
		case engine.SeverityHigh:
			highCount++
		}
	}

	// Run triage analysis if enabled and we have alerts
	var triageResults []triage.TriageResult
	if e.triager != nil && len(alerts) > 0 {
		ctx := context.Background() // TODO: Pass context from caller

		triageRes, err := e.triager.TriageAlerts(ctx, alerts, req)
		if err != nil {
			slog.Error("triage failed, degrading gracefully", "error", err)
		} else if triageRes != nil {
			triageResults = triageRes
		}
	}

	// Determine final action (with or without triage input)
	var action models.Action
	var overridable bool
	if len(triageResults) > 0 {
		// Use triage-informed decision
		action, overridable = e.incorporateTriageResults(effectiveMode, criticalCount, highCount, triageResults)
	} else {
		// Fallback to rule-only decision if triage is disabled or failed
		action, overridable = e.determineAction(effectiveMode, criticalCount, highCount)
	}

	// Build response once
	response := &EvaluationResponse{
		EventID:       req.EventID,
		Action:        action,
		Alerts:        alerts,
		TriageResults: triageResults,
		Overridable:   overridable,
		EffectiveMode: effectiveMode,
		Timestamp:     time.Now(),
	}

	// Add feedback URL if there are alerts
	if len(alerts) > 0 && e.feedbackURLBase != "" {
		response.FeedbackURL = fmt.Sprintf("%s?event_id=%s", e.feedbackURLBase, req.EventID)
	}

	// Fire deep triage async once at the end
	if len(alerts) > 0 {
		e.fireDeepTriage(alerts, req, triageResults)
	}

	return response, nil
}

// fireDeepTriage kicks off async deep investigation if configured
func (e *Evaluator) fireDeepTriage(alerts []engine.RuleResult, req *models.EvaluationRequest, fastResults []triage.TriageResult) {
	if e.deepTriager == nil {
		return
	}
	e.deepTriager.InvestigateAsync(alerts, req, fastResults)
}

// determineEffectiveMode determines the effective evaluation mode.
// Security-critical: mode is server-side configuration only.
func (e *Evaluator) determineEffectiveMode() config.EvaluationMode {
	return e.defaultMode
}

// determineAction determines what action to take based on mode and alert severity
func (e *Evaluator) determineAction(mode config.EvaluationMode, criticalCount, highCount int) (models.Action, bool) {
	switch mode {
	case config.ModeEnforce:
		// Block on critical or high severity alerts
		if criticalCount > 0 || highCount > 0 {
			return models.ActionBlock, true // Overridable in enforce mode
		}
		return models.ActionAllow, false

	case config.ModeAudit:
		// Log everything, never block
		return models.ActionLog, false

	case config.ModeShadow:
		// Allow everything silently (evaluation runs in background)
		return models.ActionAllow, false

	default:
		// Default to enforce mode behavior
		if criticalCount > 0 || highCount > 0 {
			return models.ActionBlock, true
		}
		return models.ActionAllow, false
	}
}

// incorporateTriageResults adjusts the action based on triage analysis
func (e *Evaluator) incorporateTriageResults(mode config.EvaluationMode, criticalCount, highCount int, triageResults []triage.TriageResult) (models.Action, bool) {
	// Start with rule-only decision
	baseAction, baseOverridable := e.determineAction(mode, criticalCount, highCount)

	// In audit and shadow modes, triage doesn't change the action
	if mode != config.ModeEnforce {
		return baseAction, baseOverridable
	}

	// Security-critical: triage must never downgrade a block in enforce mode.
	if baseAction == models.ActionBlock {
		return baseAction, baseOverridable
	}

	return baseAction, baseOverridable
}

// checkReputation extracts URLs from request fields and checks each against
// the reputation provider. Matched URLs produce high-severity alerts.
// Errors are logged but never cause the request to fail (fail-open).
func (e *Evaluator) checkReputation(req *models.EvaluationRequest) []engine.RuleResult {
	urls := reputation.ExtractURLs(req.Fields)
	if len(urls) == 0 {
		return nil
	}

	ctx := context.Background()
	var results []engine.RuleResult

	for _, rawURL := range urls {
		result, err := e.reputation.CheckURL(ctx, rawURL)
		if err != nil {
			slog.Warn("Reputation check failed (fail-open)", "url", rawURL, "error", err)
			continue
		}
		if !result.Matched {
			continue
		}

		severity := engine.SeverityHigh
		if result.Severity == "critical" {
			severity = engine.SeverityCritical
		}

		results = append(results, engine.RuleResult{
			RuleID:      "reputation-url-match",
			RuleName:    "Malicious URL Detected (Reputation)",
			Severity:    severity,
			Description: fmt.Sprintf("URL matched reputation database: %s (severity: %s)", rawURL, result.Severity),
			Matched:     true,
			MatchedFields: map[string]string{
				"url":      rawURL,
				"hash":     result.Hash,
				"severity": result.Severity,
			},
		})
	}

	return results
}

// GetModeInfo returns information about evaluation modes
func GetModeInfo() map[string]interface{} {
	return map[string]interface{}{
		"modes": map[string]string{
			"enforce": "Block on critical/high alerts, allow others",
			"audit":   "Log all alerts, never block",
			"shadow":  "Allow everything silently, evaluate in background",
		},
		"downgrade_policy":    "Mode is server-side only (no client override)",
		"blocking_severities": []string{"critical", "high"},
	}
}
