package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const benchRuleTemplate = `
title: Bench Test Rule %d
id: bench-rule-%d
status: experimental
description: Rule for benchmarking
logsource:
  category: test
detection:
  selection:
    command|contains: "%s"
    event_type: "tool_call"
  condition: selection
level: high
`

// writeNRules writes count synthetic rules to dir, each matching a unique
// command substring. The first rule matches "dangerous" so a single fixed
// payload can hit-or-miss deterministically across rule-set sizes.
func writeNRules(tb testing.TB, dir string, count int) {
	tb.Helper()
	for i := 0; i < count; i++ {
		needle := fmt.Sprintf("needle_%d", i)
		if i == 0 {
			needle = "dangerous"
		}
		path := filepath.Join(dir, fmt.Sprintf("rule_%04d.yml", i))
		body := fmt.Sprintf(benchRuleTemplate, i, i, needle)
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			tb.Fatal(err)
		}
	}
}

func newBenchEngine(tb testing.TB, ruleCount int) *Engine {
	tb.Helper()
	dir := tb.TempDir()
	writeNRules(tb, dir, ruleCount)
	eng, err := NewEngine(dir)
	if err != nil {
		tb.Fatal(err)
	}
	return eng
}

func BenchmarkEngine_Evaluate_Match(b *testing.B) {
	eng := newBenchEngine(b, 1)
	fields := map[string]string{
		"event_type": "tool_call",
		"command":    "dangerous_command",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		eng.Evaluate(fields)
	}
}

func BenchmarkEngine_Evaluate_NoMatch(b *testing.B) {
	eng := newBenchEngine(b, 1)
	fields := map[string]string{
		"event_type": "tool_call",
		"command":    "safe_command",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		eng.Evaluate(fields)
	}
}

// BenchmarkEngine_Evaluate_RuleScaling measures how evaluation cost scales
// with rule-set size. Validates the "sub-millisecond per event" claim
// (docs/architecture.md) across realistic rule counts.
func BenchmarkEngine_Evaluate_RuleScaling(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("rules=%d", n), func(b *testing.B) {
			eng := newBenchEngine(b, n)
			fields := map[string]string{
				"event_type": "tool_call",
				"command":    "dangerous_command",
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				eng.Evaluate(fields)
			}
		})
	}
}

func BenchmarkEngine_Evaluate_Parallel(b *testing.B) {
	eng := newBenchEngine(b, 100)
	fields := map[string]string{
		"event_type": "tool_call",
		"command":    "dangerous_command",
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			eng.Evaluate(fields)
		}
	})
}

// BenchmarkEngine_Evaluate_PayloadSize measures the effect of the input field
// map size on evaluation cost — relevant because the matched-fields shallow
// copy in engine.go scales with len(fields).
func BenchmarkEngine_Evaluate_PayloadSize(b *testing.B) {
	for _, n := range []int{4, 16, 64} {
		b.Run(fmt.Sprintf("fields=%d", n), func(b *testing.B) {
			eng := newBenchEngine(b, 10)
			fields := map[string]string{
				"event_type": "tool_call",
				"command":    "dangerous_command",
			}
			for i := 0; i < n-2; i++ {
				fields[fmt.Sprintf("field_%d", i)] = fmt.Sprintf("value_%d", i)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				eng.Evaluate(fields)
			}
		})
	}
}
