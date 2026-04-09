package evaluate

import (
	"context"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/cache"
	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/agentshield-ai/agentshield/internal/session"
	"github.com/agentshield-ai/agentshield/internal/telemetry"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// mockEngine implements a simple mock for testing
type mockEngine struct {
	mockResults []engine.RuleResult
}

func (m *mockEngine) Evaluate(fields map[string]string) []engine.RuleResult {
	return m.mockResults
}

// fieldCapturingEngine captures the fields map passed to Evaluate for assertion.
type fieldCapturingEngine struct {
	captured map[string]string
	results  []engine.RuleResult
}

func (e *fieldCapturingEngine) Evaluate(fields map[string]string) []engine.RuleResult {
	for k, v := range fields {
		e.captured[k] = v
	}
	return e.results
}

type countingEngine struct {
	calls   int
	results []engine.RuleResult
}

func (e *countingEngine) Evaluate(fields map[string]string) []engine.RuleResult {
	e.calls++
	return e.results
}

func TestDetermineEffectiveMode(t *testing.T) {
	tests := []struct {
		name        string
		defaultMode config.EvaluationMode
		expected    config.EvaluationMode
	}{
		{name: "enforce uses server default", defaultMode: config.ModeEnforce, expected: config.ModeEnforce},
		{name: "audit uses server default", defaultMode: config.ModeAudit, expected: config.ModeAudit},
		{name: "shadow uses server default", defaultMode: config.ModeShadow, expected: config.ModeShadow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evaluator := &Evaluator{defaultMode: tt.defaultMode}
			result := evaluator.determineEffectiveMode()
			if result != tt.expected {
				t.Errorf("determineEffectiveMode() = %s, want %s", result, tt.expected)
			}
		})
	}
}

func TestDetermineAction(t *testing.T) {
	tests := []struct {
		name                string
		mode                config.EvaluationMode
		criticalCount       int
		highCount           int
		mediumCount         int
		expectedAction      models.Action
		expectedOverridable bool
	}{
		{
			name:                "enforce mode with critical alert blocks",
			mode:                config.ModeEnforce,
			criticalCount:       1,
			highCount:           0,
			mediumCount:         0,
			expectedAction:      models.ActionBlock,
			expectedOverridable: true,
		},
		{
			name:                "enforce mode with high alert blocks",
			mode:                config.ModeEnforce,
			criticalCount:       0,
			highCount:           1,
			mediumCount:         0,
			expectedAction:      models.ActionBlock,
			expectedOverridable: true,
		},
		{
			name:                "enforce mode with no high/critical allows",
			mode:                config.ModeEnforce,
			criticalCount:       0,
			highCount:           0,
			mediumCount:         0,
			expectedAction:      models.ActionAllow,
			expectedOverridable: false,
		},
		{
			name:                "enforce mode with medium alert requires approval",
			mode:                config.ModeEnforce,
			criticalCount:       0,
			highCount:           0,
			mediumCount:         1,
			expectedAction:      models.ActionRequireApproval,
			expectedOverridable: true,
		},
		{
			name:                "enforce mode critical takes precedence over medium",
			mode:                config.ModeEnforce,
			criticalCount:       1,
			highCount:           0,
			mediumCount:         3,
			expectedAction:      models.ActionBlock,
			expectedOverridable: true,
		},
		{
			name:                "enforce mode high takes precedence over medium",
			mode:                config.ModeEnforce,
			criticalCount:       0,
			highCount:           1,
			mediumCount:         5,
			expectedAction:      models.ActionBlock,
			expectedOverridable: true,
		},
		{
			name:                "audit mode always logs even with medium",
			mode:                config.ModeAudit,
			criticalCount:       5,
			highCount:           3,
			mediumCount:         2,
			expectedAction:      models.ActionLog,
			expectedOverridable: false,
		},
		{
			name:                "audit mode logs on medium only",
			mode:                config.ModeAudit,
			criticalCount:       0,
			highCount:           0,
			mediumCount:         1,
			expectedAction:      models.ActionLog,
			expectedOverridable: false,
		},
		{
			name:                "shadow mode always allows even with medium",
			mode:                config.ModeShadow,
			criticalCount:       5,
			highCount:           3,
			mediumCount:         2,
			expectedAction:      models.ActionAllow,
			expectedOverridable: false,
		},
		{
			name:                "shadow mode allows on medium only",
			mode:                config.ModeShadow,
			criticalCount:       0,
			highCount:           0,
			mediumCount:         1,
			expectedAction:      models.ActionAllow,
			expectedOverridable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evaluator := &Evaluator{}

			action, overridable := evaluator.determineAction(tt.mode, tt.criticalCount, tt.highCount, tt.mediumCount)

			if action != tt.expectedAction {
				t.Errorf("determineAction() action = %s, want %s", action, tt.expectedAction)
			}

			if overridable != tt.expectedOverridable {
				t.Errorf("determineAction() overridable = %t, want %t", overridable, tt.expectedOverridable)
			}
		})
	}
}

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name           string
		mockResults    []engine.RuleResult
		defaultMode    config.EvaluationMode
		expectedAction models.Action
		expectedAlerts int
	}{
		{
			name:           "no matched rules allows",
			mockResults:    []engine.RuleResult{},
			defaultMode:    config.ModeEnforce,
			expectedAction: models.ActionAllow,
			expectedAlerts: 0,
		},
		{
			name: "matched critical rule blocks in enforce mode",
			mockResults: []engine.RuleResult{
				{
					RuleID:   "test-rule-critical",
					RuleName: "Critical Test Rule",
					Severity: engine.SeverityCritical,
					Matched:  true,
				},
			},
			defaultMode:    config.ModeEnforce,
			expectedAction: models.ActionBlock,
			expectedAlerts: 1,
		},
		{
			name: "matched critical rule logs in audit mode",
			mockResults: []engine.RuleResult{
				{
					RuleID:   "test-rule-critical",
					RuleName: "Critical Test Rule",
					Severity: engine.SeverityCritical,
					Matched:  true,
				},
			},
			defaultMode:    config.ModeAudit,
			expectedAction: models.ActionLog,
			expectedAlerts: 1,
		},
		{
			name: "matched low severity allows in enforce mode",
			mockResults: []engine.RuleResult{
				{
					RuleID:   "test-rule-low",
					RuleName: "Low Severity Rule",
					Severity: engine.SeverityLow,
					Matched:  true,
				},
			},
			defaultMode:    config.ModeEnforce,
			expectedAction: models.ActionAllow,
			expectedAlerts: 1,
		},
		{
			name: "matched medium severity requires approval in enforce mode",
			mockResults: []engine.RuleResult{
				{
					RuleID:   "test-rule-medium",
					RuleName: "Medium Severity Rule",
					Severity: engine.SeverityMedium,
					Matched:  true,
				},
			},
			defaultMode:    config.ModeEnforce,
			expectedAction: models.ActionRequireApproval,
			expectedAlerts: 1,
		},
		{
			name: "matched medium severity logs in audit mode",
			mockResults: []engine.RuleResult{
				{
					RuleID:   "test-rule-medium",
					RuleName: "Medium Severity Rule",
					Severity: engine.SeverityMedium,
					Matched:  true,
				},
			},
			defaultMode:    config.ModeAudit,
			expectedAction: models.ActionLog,
			expectedAlerts: 1,
		},
		{
			name: "matched medium severity allows in shadow mode",
			mockResults: []engine.RuleResult{
				{
					RuleID:   "test-rule-medium",
					RuleName: "Medium Severity Rule",
					Severity: engine.SeverityMedium,
					Matched:  true,
				},
			},
			defaultMode:    config.ModeShadow,
			expectedAction: models.ActionAllow,
			expectedAlerts: 1,
		},
		// client mode override removed: server-side mode only
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockEng := &mockEngine{
				mockResults: tt.mockResults,
			}

			evaluator := NewEvaluator(mockEng, tt.defaultMode, "", nil, nil)

			req := &models.EvaluationRequest{
				EventID: "test-event-123",
				Fields: map[string]string{
					"test": "value",
				},
			}

			response, err := evaluator.Evaluate(req)
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}

			if response.Action != tt.expectedAction {
				t.Errorf("Evaluate() action = %s, want %s", response.Action, tt.expectedAction)
			}

			if len(response.Alerts) != tt.expectedAlerts {
				t.Errorf("Evaluate() alerts count = %d, want %d", len(response.Alerts), tt.expectedAlerts)
			}

			if response.EventID != req.EventID {
				t.Errorf("Evaluate() event_id = %s, want %s", response.EventID, req.EventID)
			}

			// Check that only matched rules are in alerts
			for _, alert := range response.Alerts {
				if !alert.Matched {
					t.Errorf("Alert should only contain matched rules, got unmatched rule: %s", alert.RuleID)
				}
			}
		})
	}
}

func TestEvaluateWithContext_EmitsSpan(t *testing.T) {
	spanRecorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	tracer := tp.Tracer("agentshield")

	mockEng := &mockEngine{mockResults: []engine.RuleResult{
		{RuleID: "test-1", RuleName: "Test Rule", Severity: engine.SeverityLow, Matched: true},
	}}
	evaluator := NewEvaluator(mockEng, config.ModeEnforce, "", nil, nil)
	evaluator.SetTracer(tracer)

	req := &models.EvaluationRequest{
		EventID:   "evt-001",
		SessionID: "sess-001",
		Tool:      "bash",
		Source:    "openclaw",
		Fields:    map[string]string{"tool": "bash"},
	}

	ctx := context.Background()
	resp, err := evaluator.EvaluateWithContext(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Action == "" {
		t.Fatal("expected non-empty action")
	}

	tp.ForceFlush(ctx)

	spans := spanRecorder.Ended()
	if len(spans) == 0 {
		t.Fatal("expected at least one span")
	}
	span := spans[0]
	if span.Name() != "evaluate" {
		t.Errorf("expected span name 'evaluate', got %q", span.Name())
	}

	attrs := span.Attributes()
	found := map[string]bool{}
	for _, a := range attrs {
		found[string(a.Key)] = true
	}
	for _, key := range []string{"event.id", "session.id", "tool.name", "source", "verdict.action", "alerts.count", "verdict.cached", "verdict.mode"} {
		if !found[key] {
			t.Errorf("expected span attribute %q", key)
		}
	}
}

func TestEvaluateWithContext_SpanEvents_RuleMatches(t *testing.T) {
	spanRecorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	tracer := tp.Tracer("agentshield")

	mockEng := &mockEngine{mockResults: []engine.RuleResult{
		{RuleID: "rule-1", RuleName: "Reverse Shell", Severity: engine.SeverityCritical, Matched: true},
		{RuleID: "rule-2", RuleName: "Data Exfil", Severity: engine.SeverityHigh, Matched: true},
	}}
	evaluator := NewEvaluator(mockEng, config.ModeEnforce, "", nil, nil)
	evaluator.SetTracer(tracer)

	req := &models.EvaluationRequest{
		EventID: "evt-002",
		Tool:    "bash",
		Fields:  map[string]string{"tool": "bash"},
	}

	ctx := context.Background()
	evaluator.EvaluateWithContext(ctx, req)
	tp.ForceFlush(ctx)

	spans := spanRecorder.Ended()
	if len(spans) == 0 {
		t.Fatal("expected span")
	}

	events := spans[0].Events()
	ruleEvents := 0
	for _, ev := range events {
		if ev.Name == "rule.matched" {
			ruleEvents++
			// Verify each rule.matched event has the expected attributes
			attrMap := make(map[string]string)
			for _, attr := range ev.Attributes {
				attrMap[string(attr.Key)] = attr.Value.AsString()
			}
			if _, ok := attrMap["rule.id"]; !ok {
				t.Error("rule.matched event missing rule.id attribute")
			}
			if _, ok := attrMap["rule.name"]; !ok {
				t.Error("rule.matched event missing rule.name attribute")
			}
			if _, ok := attrMap["rule.severity"]; !ok {
				t.Error("rule.matched event missing rule.severity attribute")
			}
		}
	}
	if ruleEvents != 2 {
		t.Errorf("expected 2 rule.matched events, got %d", ruleEvents)
	}
}

func TestEvaluateWithContext_SpanEvents_CacheHit(t *testing.T) {
	spanRecorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	tracer := tp.Tracer("agentshield")

	mockEng := &mockEngine{mockResults: []engine.RuleResult{
		{RuleID: "rule-1", RuleName: "Test Rule", Severity: engine.SeverityLow, Matched: true},
	}}
	evaluator := NewEvaluator(mockEng, config.ModeEnforce, "", nil, nil)
	evaluator.SetTracer(tracer)

	// Set up a verdict cache and pre-populate it
	vc := cache.NewVerdictCache(100, 5*time.Minute)
	evaluator.SetCache(vc)

	req := &models.EvaluationRequest{
		EventID: "evt-003",
		Tool:    "bash",
		Args:    map[string]string{"command": "ls"},
		Fields:  map[string]string{"tool": "bash"},
	}

	ctx := context.Background()

	// First call populates the cache
	evaluator.EvaluateWithContext(ctx, req)
	tp.ForceFlush(ctx)

	// Second call should hit the cache
	req.EventID = "evt-004" // different event ID, same tool+args
	evaluator.EvaluateWithContext(ctx, req)
	tp.ForceFlush(ctx)

	spans := spanRecorder.Ended()
	if len(spans) < 2 {
		t.Fatalf("expected at least 2 spans, got %d", len(spans))
	}

	// The second span (cache hit) should have a "cache.hit" event
	cacheHitSpan := spans[1]
	cacheHitFound := false
	for _, ev := range cacheHitSpan.Events() {
		if ev.Name == "cache.hit" {
			cacheHitFound = true
		}
	}
	if !cacheHitFound {
		t.Error("expected cache.hit span event on second evaluation (cache hit path)")
	}
}

func TestEvaluateWithContext_RecordsMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	meter := mp.Meter("test")

	metricsRec, err := telemetry.NewMetricsRecorder(meter)
	if err != nil {
		t.Fatal(err)
	}

	mockEng := &mockEngine{mockResults: []engine.RuleResult{
		{RuleID: "r1", RuleName: "Test Rule", Severity: engine.SeverityLow, Matched: true},
	}}
	evaluator := NewEvaluator(mockEng, config.ModeEnforce, "", nil, nil)
	evaluator.SetMetrics(metricsRec)

	req := &models.EvaluationRequest{
		EventID: "evt-m1",
		Tool:    "bash",
		Fields:  map[string]string{"tool": "bash"},
	}
	_, err = evaluator.EvaluateWithContext(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	if len(rm.ScopeMetrics) == 0 {
		t.Fatal("expected metrics to be recorded")
	}

	foundMetrics := make(map[string]bool)
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			foundMetrics[m.Name] = true
		}
	}

	expected := []string{
		"agentshield.evaluations.total",
		"agentshield.alerts.total",
		"agentshield.cache.misses",
	}
	for _, name := range expected {
		if !foundMetrics[name] {
			t.Errorf("expected metric %q to be recorded", name)
		}
	}
}

func TestEvaluateWithContext_RecordsMetrics_CacheHit(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	meter := mp.Meter("test")

	metricsRec, err := telemetry.NewMetricsRecorder(meter)
	if err != nil {
		t.Fatal(err)
	}

	mockEng := &mockEngine{mockResults: []engine.RuleResult{
		{RuleID: "r1", RuleName: "Test Rule", Severity: engine.SeverityLow, Matched: true},
	}}
	evaluator := NewEvaluator(mockEng, config.ModeEnforce, "", nil, nil)
	evaluator.SetMetrics(metricsRec)

	vc := cache.NewVerdictCache(100, 5*time.Minute)
	evaluator.SetCache(vc)

	req := &models.EvaluationRequest{
		EventID: "evt-m2",
		Tool:    "bash",
		Args:    map[string]string{"command": "ls"},
		Fields:  map[string]string{"tool": "bash"},
	}

	// First call populates cache
	_, err = evaluator.EvaluateWithContext(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	// Second call hits cache
	req.EventID = "evt-m3"
	resp, err := evaluator.EvaluateWithContext(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Cached {
		t.Fatal("expected second evaluation to be cached")
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}

	foundHits := false
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == "agentshield.cache.hits" {
				foundHits = true
			}
		}
	}
	if !foundHits {
		t.Error("expected agentshield.cache.hits metric after cache hit evaluation")
	}
}

func TestGetModeInfo(t *testing.T) {
	info := GetModeInfo()

	// Check that all expected keys exist
	expectedKeys := []string{"modes", "downgrade_policy", "blocking_severities", "approval_severities"}
	for _, key := range expectedKeys {
		if _, exists := info[key]; !exists {
			t.Errorf("GetModeInfo() missing key: %s", key)
		}
	}

	// Check that modes contains expected modes
	modes, ok := info["modes"].(map[string]string)
	if !ok {
		t.Fatalf("modes should be map[string]string")
	}

	expectedModes := []string{"enforce", "audit", "shadow"}
	for _, mode := range expectedModes {
		if _, exists := modes[mode]; !exists {
			t.Errorf("GetModeInfo() modes missing: %s", mode)
		}
	}
}

func TestEvaluateWithContext_InjectsSessionFields(t *testing.T) {
	captured := make(map[string]string)
	mockEng := &fieldCapturingEngine{captured: captured}

	registry := session.NewRegistry(10, 5*time.Minute)
	registry.Record("sess-inject", "ls", nil)
	registry.Record("sess-inject", "cat", nil)

	evaluator := NewEvaluator(mockEng, config.ModeEnforce, "", nil, nil)
	evaluator.SetSessionRegistry(registry)

	req := &models.EvaluationRequest{
		EventID:   "evt-inj",
		SessionID: "sess-inject",
		Tool:      "curl",
		Fields:    map[string]string{"tool": "curl"},
	}
	evaluator.EvaluateWithContext(context.Background(), req)

	if captured["session.recent_tools"] != "ls,cat" {
		t.Errorf("expected session.recent_tools='ls,cat', got %q", captured["session.recent_tools"])
	}
	if captured["session.tool_count"] != "2" {
		t.Errorf("expected session.tool_count='2', got %q", captured["session.tool_count"])
	}
}

func TestEvaluateWithContext_CacheMissWhenSessionStateChanges(t *testing.T) {
	mockEng := &countingEngine{}
	registry := session.NewRegistry(10, 5*time.Minute)
	registry.Record("sess-cache", "ls", nil)

	evaluator := NewEvaluator(mockEng, config.ModeEnforce, "", nil, nil)
	evaluator.SetSessionRegistry(registry)
	evaluator.SetCache(cache.NewVerdictCache(100, 5*time.Minute))

	req := &models.EvaluationRequest{
		EventID:   "evt-cache-1",
		SessionID: "sess-cache",
		Tool:      "bash",
		Args:      map[string]string{"command": "echo hi"},
		Fields:    map[string]string{"tool": "bash", "event_type": "tool_call", "command": "echo hi"},
	}

	resp1, err := evaluator.EvaluateWithContext(context.Background(), req)
	if err != nil {
		t.Fatalf("first evaluation failed: %v", err)
	}
	if resp1.Cached {
		t.Fatal("first evaluation should not be cached")
	}

	// Change the prior session state; identical command should no longer reuse
	// the cached verdict because behavioural rules can now evaluate differently.
	registry.Record("sess-cache", "cat", nil)
	req.EventID = "evt-cache-2"

	resp2, err := evaluator.EvaluateWithContext(context.Background(), req)
	if err != nil {
		t.Fatalf("second evaluation failed: %v", err)
	}
	if resp2.Cached {
		t.Fatal("second evaluation should miss cache after session state changes")
	}
	if mockEng.calls != 2 {
		t.Fatalf("expected engine to run twice, got %d calls", mockEng.calls)
	}
}

func TestEvaluateWithContext_SpanIncludesSessionFields(t *testing.T) {
	spanRecorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	tracer := tp.Tracer("agentshield")

	mockEng := &mockEngine{mockResults: nil}
	registry := session.NewRegistry(10, 5*time.Minute)
	registry.Record("sess-span", "ls", nil)
	registry.Record("sess-span", "cat", nil)

	evaluator := NewEvaluator(mockEng, config.ModeEnforce, "", nil, nil)
	evaluator.SetTracer(tracer)
	evaluator.SetSessionRegistry(registry)

	req := &models.EvaluationRequest{
		EventID:   "evt-sp",
		SessionID: "sess-span",
		Tool:      "curl",
		Fields:    map[string]string{"tool": "curl"},
	}
	evaluator.EvaluateWithContext(context.Background(), req)
	tp.ForceFlush(context.Background())

	spans := spanRecorder.Ended()
	if len(spans) == 0 {
		t.Fatal("expected span")
	}

	attrs := spans[0].Attributes()
	found := false
	for _, a := range attrs {
		if string(a.Key) == "session.tool_count" {
			found = true
			if a.Value.AsString() != "2" {
				t.Errorf("expected session.tool_count=2, got %q", a.Value.AsString())
			}
		}
	}
	if !found {
		t.Error("expected session.tool_count span attribute")
	}
}

func TestEvaluateWithContext_RecordsToSessionAfterEval(t *testing.T) {
	mockEng := &mockEngine{mockResults: nil}
	registry := session.NewRegistry(10, 5*time.Minute)

	evaluator := NewEvaluator(mockEng, config.ModeEnforce, "", nil, nil)
	evaluator.SetSessionRegistry(registry)

	req := &models.EvaluationRequest{
		EventID:   "evt-rec",
		SessionID: "sess-rec",
		Tool:      "bash",
		Fields:    map[string]string{"tool": "bash"},
	}
	evaluator.EvaluateWithContext(context.Background(), req)

	window := registry.Get("sess-rec")
	if window == nil {
		t.Fatal("expected session to be recorded")
	}
	if len(window.Events) != 1 {
		t.Errorf("expected 1 event, got %d", len(window.Events))
	}
	if window.Events[0].Tool != "bash" {
		t.Errorf("expected tool 'bash', got %q", window.Events[0].Tool)
	}
}

func TestEvaluateWithContext_NilFields_NoPanic(t *testing.T) {
	mockEng := &mockEngine{mockResults: nil}
	registry := session.NewRegistry(10, 5*time.Minute)

	evaluator := NewEvaluator(mockEng, config.ModeEnforce, "", nil, nil)
	evaluator.SetSessionRegistry(registry)

	// Pre-populate session so DeriveFields returns non-nil
	registry.Record("sess-nil", "prior-tool", nil)

	req := &models.EvaluationRequest{
		EventID:   "evt-nil",
		SessionID: "sess-nil",
		Tool:      "exec",
		Fields:    nil, // Deliberately nil
	}
	// Should not panic
	resp, err := evaluator.EvaluateWithContext(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}
