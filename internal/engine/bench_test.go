package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkEvaluate(b *testing.B) {
	tmpDir, err := os.MkdirTemp("", "engine_bench")
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	testRule := `
title: Bench Test Rule
id: bench-rule-1
status: experimental
description: Rule for benchmarking
logsource:
  category: test
detection:
  selection:
    command|contains: "dangerous"
    event_type: "tool_call"
  condition: selection
level: high
`
	if err := os.WriteFile(filepath.Join(tmpDir, "bench_rule.yml"), []byte(testRule), 0644); err != nil {
		b.Fatal(err)
	}

	eng, err := NewEngine(tmpDir)
	if err != nil {
		b.Fatal(err)
	}

	fields := map[string]string{
		"event_type": "tool_call",
		"command":    "dangerous_command",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		eng.Evaluate(fields)
	}
}

func BenchmarkEvaluateNoMatch(b *testing.B) {
	tmpDir, err := os.MkdirTemp("", "engine_bench_nomatch")
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	testRule := `
title: Bench Test Rule
id: bench-rule-1
status: experimental
description: Rule for benchmarking
logsource:
  category: test
detection:
  selection:
    command|contains: "dangerous"
    event_type: "tool_call"
  condition: selection
level: high
`
	if err := os.WriteFile(filepath.Join(tmpDir, "bench_rule.yml"), []byte(testRule), 0644); err != nil {
		b.Fatal(err)
	}

	eng, err := NewEngine(tmpDir)
	if err != nil {
		b.Fatal(err)
	}

	fields := map[string]string{
		"event_type": "tool_call",
		"command":    "safe_command",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		eng.Evaluate(fields)
	}
}
