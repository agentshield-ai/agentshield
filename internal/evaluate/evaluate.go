package evaluate

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/agentshield-ai/agentshield/internal/cache"
	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/agentshield-ai/agentshield/internal/session"
	"github.com/agentshield-ai/agentshield/internal/telemetry"
	"github.com/agentshield-ai/agentshield/internal/triage"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
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
	Cached        bool                  `json:"cached"`
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
	cache           *cache.VerdictCache
	tracer          trace.Tracer
	metrics         *telemetry.MetricsRecorder
	sessionRegistry *session.Registry
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

// SetCache attaches a verdict cache to the evaluator. If nil, caching is disabled.
func (e *Evaluator) SetCache(c *cache.VerdictCache) {
	e.cache = c
}

// SetTracer sets the OpenTelemetry tracer for evaluation instrumentation.
func (e *Evaluator) SetTracer(t trace.Tracer) {
	e.tracer = t
}

// SetMetrics sets the OTel metrics recorder for evaluation instrumentation.
func (e *Evaluator) SetMetrics(m *telemetry.MetricsRecorder) {
	e.metrics = m
}

// SetSessionRegistry sets the session registry for behavioural sequencing.
func (e *Evaluator) SetSessionRegistry(r *session.Registry) {
	e.sessionRegistry = r
}

// Evaluate processes an evaluation request with a background context.
func (e *Evaluator) Evaluate(req *models.EvaluationRequest) (*EvaluationResponse, error) {
	return e.EvaluateWithContext(context.Background(), req)
}

// EvaluateWithContext processes an evaluation request and propagates caller
// cancellation/deadlines to triage providers.
func (e *Evaluator) EvaluateWithContext(ctx context.Context, req *models.EvaluationRequest) (response *EvaluationResponse, err error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if e.tracer != nil {
		var span trace.Span
		ctx, span = e.tracer.Start(ctx, "evaluate",
			trace.WithAttributes(
				attribute.String("event.id", req.EventID),
				attribute.String("session.id", req.SessionID),
				attribute.String("tool.name", req.Tool),
				attribute.String("source", req.Source),
			),
		)
		defer func() {
			if response != nil {
				span.SetAttributes(
					attribute.String("verdict.action", string(response.Action)),
					attribute.Int("alerts.count", len(response.Alerts)),
					attribute.Bool("verdict.cached", response.Cached),
					attribute.String("verdict.mode", string(response.EffectiveMode)),
				)
			}
			span.End()
		}()
	}

	// Determine effective evaluation mode (server-side only)
	effectiveMode := e.determineEffectiveMode()

	// Check verdict cache before running rule evaluation
	var cacheKey string
	if e.cache != nil {
		cacheContext := req.Context
		if cacheContext == "" && req.Fields != nil {
			cacheContext = req.Fields["context"]
		}
		cacheKey = cache.CacheKeyWithContext(req.Tool, req.Args, cacheContext)
		if cached, ok := e.cache.Get(cacheKey); ok {
			slog.Debug("Cache hit", "tool", req.Tool, "key", cacheKey)
			response = &EvaluationResponse{
				EventID:       req.EventID,
				Action:        cached.Action,
				Alerts:        cached.Alerts,
				TriageResults: cached.TriageResults,
				Overridable:   cached.Overridable,
				EffectiveMode: effectiveMode,
				Cached:        true,
				Timestamp:     time.Now(),
			}
			if e.tracer != nil {
				span := trace.SpanFromContext(ctx)
				span.AddEvent("cache.hit")
			}
			if e.metrics != nil {
				e.metrics.RecordEvaluation(ctx, req.Tool, string(response.Action), len(response.Alerts), true)
			}
			if len(cached.Alerts) > 0 && e.feedbackURLBase != "" {
				response.FeedbackURL = fmt.Sprintf("%s?event_id=%s", e.feedbackURLBase, req.EventID)
			}
			return response, nil
		}
	}

	// Inject session-derived fields for behavioural sequencing
	if e.sessionRegistry != nil && req.SessionID != "" {
		sessionFields := e.sessionRegistry.DeriveFields(req.SessionID)
		for k, v := range sessionFields {
			req.Fields[k] = v
		}
	}

	// Run rule evaluation (engine now only returns matched rules)
	alerts := e.engine.Evaluate(req.Fields)

	// Count severity levels for decision making
	var criticalCount, highCount, mediumCount int
	for _, result := range alerts {
		switch result.Severity {
		case engine.SeverityCritical:
			criticalCount++
		case engine.SeverityHigh:
			highCount++
		case engine.SeverityMedium:
			mediumCount++
		}
	}

	// Emit span events for each matched rule
	if e.tracer != nil {
		span := trace.SpanFromContext(ctx)
		for _, alert := range alerts {
			span.AddEvent("rule.matched", trace.WithAttributes(
				attribute.String("rule.id", alert.RuleID),
				attribute.String("rule.name", alert.RuleName),
				attribute.String("rule.severity", string(alert.Severity)),
			))
		}
	}

	// Run triage analysis if enabled and we have alerts
	var triageResults []triage.TriageResult
	if e.triager != nil && len(alerts) > 0 {
		triageRes, err := e.triager.TriageAlerts(ctx, alerts, req)
		if err != nil {
			slog.Error("triage failed, degrading gracefully", "error", err)
		} else if triageRes != nil {
			triageResults = triageRes
		}
	}

	// Determine final action based on rule severity counts and evaluation mode.
	// Security invariant: triage results are returned for observability but never
	// override rule-based enforcement decisions.
	action, overridable := e.determineAction(effectiveMode, criticalCount, highCount, mediumCount)

	// Build response once
	response = &EvaluationResponse{
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

	// Store result in verdict cache
	if e.cache != nil && cacheKey != "" {
		e.cache.Set(cacheKey, &cache.CachedVerdict{
			Action:        response.Action,
			Alerts:        response.Alerts,
			TriageResults: response.TriageResults,
			Overridable:   response.Overridable,
			CachedAt:      time.Now(),
		})
	}

	// Record evaluation metrics
	if e.metrics != nil {
		e.metrics.RecordEvaluation(ctx, req.Tool, string(response.Action), len(response.Alerts), false)
	}

	// Record this event in the session window (after evaluation, so the current
	// event is visible to the NEXT evaluation, not this one)
	if e.sessionRegistry != nil && req.SessionID != "" {
		e.sessionRegistry.Record(req.SessionID, req.Tool, alerts)
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

// determineAction determines what action to take based on mode and alert severity.
// Severity-to-action mapping (enforce mode):
//   - critical/high -> block
//   - medium        -> require_approval
//   - low/none      -> allow
//
// Mode interactions:
//   - audit:  require_approval downgrades to log (audit never blocks or asks)
//   - shadow: require_approval downgrades to allow (shadow is silent)
func (e *Evaluator) determineAction(mode config.EvaluationMode, criticalCount, highCount, mediumCount int) (models.Action, bool) {
	switch mode {
	case config.ModeEnforce:
		if criticalCount > 0 || highCount > 0 {
			return models.ActionBlock, true // Overridable in enforce mode
		}
		if mediumCount > 0 {
			return models.ActionRequireApproval, true
		}
		return models.ActionAllow, false

	case config.ModeAudit:
		// Log everything, never block or require approval
		return models.ActionLog, false

	case config.ModeShadow:
		// Allow everything silently (evaluation runs in background)
		return models.ActionAllow, false

	default:
		// Default to enforce mode behavior
		if criticalCount > 0 || highCount > 0 {
			return models.ActionBlock, true
		}
		if mediumCount > 0 {
			return models.ActionRequireApproval, true
		}
		return models.ActionAllow, false
	}
}

// GetModeInfo returns information about evaluation modes
func GetModeInfo() map[string]interface{} {
	return map[string]interface{}{
		"modes": map[string]string{
			"enforce": "Block on critical/high, require approval on medium, allow others",
			"audit":   "Log all alerts, never block or require approval",
			"shadow":  "Allow everything silently, evaluate in background",
		},
		"downgrade_policy":    "Mode is server-side only (no client override)",
		"blocking_severities": []string{"critical", "high"},
		"approval_severities": []string{"medium"},
	}
}
