package evaluate

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/cache"
	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/agentshield-ai/agentshield/internal/session"
)

// newBenchEvaluator builds an evaluator returning one low-severity match,
// optionally with an attached verdict cache. Engine cost is benched separately;
// here we measure the evaluator's own work (field merging, key compute, cache,
// session enrichment, action derivation).
func newBenchEvaluator(withCache bool) *Evaluator {
	results := []engine.RuleResult{
		{
			RuleID:   "bench-rule-low",
			RuleName: "bench rule low",
			Severity: engine.SeverityLow,
			Matched:  true,
		},
	}
	ev := NewEvaluator(&mockEngine{mockResults: results}, config.ModeEnforce, "", nil, nil)
	if withCache {
		ev.SetCache(cache.NewVerdictCache(10000, 5*time.Minute))
	}
	return ev
}

func newBenchRequest(i int) *models.EvaluationRequest {
	return &models.EvaluationRequest{
		EventID: fmt.Sprintf("evt_%d", i),
		Tool:    "Bash",
		Args: map[string]string{
			"command": fmt.Sprintf("echo hello %d", i),
		},
		Fields: map[string]string{
			"event_type": "tool_call",
			"command":    fmt.Sprintf("echo hello %d", i),
		},
		Context: "prod",
	}
}

func BenchmarkEvaluator_EvaluateWithContext_CacheMiss(b *testing.B) {
	ev := newBenchEvaluator(true)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Unique args per iteration force a cache miss every time.
		req := newBenchRequest(i)
		if _, err := ev.EvaluateWithContext(ctx, req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEvaluator_EvaluateWithContext_CacheHit(b *testing.B) {
	ev := newBenchEvaluator(true)
	ctx := context.Background()
	req := newBenchRequest(0)
	if _, err := ev.EvaluateWithContext(ctx, req); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ev.EvaluateWithContext(ctx, req); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEvaluator_EvaluateWithContext_NoCache isolates evaluator work from
// cache lock contention — useful when interpreting the parallel benches.
func BenchmarkEvaluator_EvaluateWithContext_NoCache(b *testing.B) {
	ev := newBenchEvaluator(false)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := newBenchRequest(i)
		if _, err := ev.EvaluateWithContext(ctx, req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEvaluator_EvaluateWithContext_SessionEnrichment(b *testing.B) {
	ev := newBenchEvaluator(true)
	reg := session.NewRegistry(50, 15*time.Minute)
	ev.SetSessionRegistry(reg)
	const sessID = "bench-session"
	for i := 0; i < 10; i++ {
		reg.RecordWithVerdict(sessID, "Bash", fmt.Sprintf("e%d", i), nil, models.ActionAllow, false)
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := newBenchRequest(i)
		req.SessionID = sessID
		if _, err := ev.EvaluateWithContext(ctx, req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEvaluator_EvaluateWithContext_Parallel(b *testing.B) {
	ev := newBenchEvaluator(true)
	ctx := context.Background()
	var counter atomic.Uint64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			i := counter.Add(1)
			req := newBenchRequest(int(i))
			if _, err := ev.EvaluateWithContext(ctx, req); err != nil {
				b.Fatal(err)
			}
		}
	})
}
