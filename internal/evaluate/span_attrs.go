package evaluate

import (
	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/models"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Attribute names the evaluate span carries for Weft (docs/weft/DESIGN.md §5.1).
// The gen_ai.* names follow the OpenTelemetry GenAI semantic conventions so a
// trace backend can join the verdict to the agent's own tool span.
const (
	AttrConversationID  = "gen_ai.conversation.id"
	AttrToolName        = "gen_ai.tool.name"
	AttrToolCallID      = "gen_ai.tool.call.id"
	AttrAgentName       = "gen_ai.agent.name"
	AttrVerdictAction   = "agentshield.verdict.action"
	AttrVerdictMode     = "agentshield.verdict.mode"
	AttrVerdictSeverity = "agentshield.verdict.severity"
	AttrRuleIDs         = "agentshield.rule_ids"
	// AttrReplay marks the evaluation of a replayed tool call; Weft does not
	// turn such spans into detections or sessions.
	AttrReplay       = "agentshield.replay"
	EventRuleMatched = "agentshield.rule.matched"
)

// evaluateSpanAttributes are the attributes known before evaluation runs. The
// legacy event.id / session.id / tool.name / source attributes are kept for
// existing dashboards.
func evaluateSpanAttributes(req *models.EvaluationRequest) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("event.id", req.EventID),
		attribute.String("session.id", req.SessionID),
		attribute.String("tool.name", req.Tool),
		attribute.String("source", req.Source),
	}
	if req.SessionID != "" {
		attrs = append(attrs, attribute.String(AttrConversationID, req.SessionID))
	}
	if req.Tool != "" {
		attrs = append(attrs, attribute.String(AttrToolName, req.Tool))
	}
	if req.ToolCallID != "" {
		attrs = append(attrs, attribute.String(AttrToolCallID, req.ToolCallID))
	}
	if req.AgentName != "" {
		attrs = append(attrs, attribute.String(AttrAgentName, req.AgentName))
	}
	if req.Replay {
		attrs = append(attrs, attribute.Bool(AttrReplay, true))
	}
	return attrs
}

// recordVerdictOnSpan adds the verdict attributes and one
// agentshield.rule.matched event per matched rule (cached verdicts included).
func recordVerdictOnSpan(span trace.Span, resp *EvaluationResponse) {
	ruleIDs := make([]string, 0, len(resp.Alerts))
	for _, a := range resp.Alerts {
		ruleIDs = append(ruleIDs, a.RuleID)
	}
	span.SetAttributes(
		attribute.String(AttrVerdictAction, string(resp.Action)),
		attribute.String(AttrVerdictMode, string(resp.EffectiveMode)),
		attribute.String(AttrVerdictSeverity, MaxSeverity(resp.Alerts)),
		attribute.StringSlice(AttrRuleIDs, ruleIDs),
	)
	for _, a := range resp.Alerts {
		tags := a.Tags
		if tags == nil {
			tags = []string{}
		}
		span.AddEvent(EventRuleMatched, trace.WithAttributes(
			attribute.String("rule.id", a.RuleID),
			attribute.String("rule.title", a.RuleName),
			attribute.String("rule.level", string(a.Severity)),
			attribute.StringSlice("rule.tags", tags),
		))
	}
}

// severityRank orders severities; unknown and empty rank 0.
func severityRank(s engine.AlertSeverity) int {
	switch s {
	case engine.SeverityLow:
		return 1
	case engine.SeverityMedium:
		return 2
	case engine.SeverityHigh:
		return 3
	case engine.SeverityCritical:
		return 4
	}
	return 0
}

// MaxSeverity is the highest severity among alerts, or "none" without alerts.
func MaxSeverity(alerts []engine.RuleResult) string {
	best := engine.AlertSeverity("")
	for _, a := range alerts {
		if severityRank(a.Severity) > severityRank(best) {
			best = a.Severity
		}
	}
	if best == "" {
		return "none"
	}
	return string(best)
}

// EnforceAction is the action enforce mode takes for these alerts, whatever
// mode produced them: critical/high block, medium requires approval, anything
// else is allowed. Weft replay uses it to compare a shadow evaluation with
// the verdicts the engine originally gave.
func EnforceAction(alerts []engine.RuleResult) models.Action {
	var e Evaluator
	var crit, high, med int
	for _, a := range alerts {
		switch a.Severity {
		case engine.SeverityCritical:
			crit++
		case engine.SeverityHigh:
			high++
		case engine.SeverityMedium:
			med++
		}
	}
	action, _ := e.determineAction(config.ModeEnforce, crit, high, med)
	return action
}
