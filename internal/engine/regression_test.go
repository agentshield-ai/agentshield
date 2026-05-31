package engine

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	sigma "github.com/agentshield-ai/agentshield/pkg/sigma"
)

// TestMapSeverityFastPath verifies that the optimized fast path for
// standard sigma.Level constants returns the same results as the
// original slow path (which normalizes via strings.ToLower).
func TestMapSeverityFastPath(t *testing.T) {
	eng := &Engine{}

	tests := []struct {
		level    sigma.Level
		expected AlertSeverity
	}{
		{sigma.Informational, SeverityLow},
		{sigma.Low, SeverityLow},
		{sigma.Medium, SeverityMedium},
		{sigma.High, SeverityHigh},
		{sigma.Critical, SeverityCritical},
	}

	for _, tt := range tests {
		t.Run(string(tt.level), func(t *testing.T) {
			result := eng.mapSeverity(tt.level)
			if result != tt.expected {
				t.Errorf("mapSeverity(%s) = %v, want %v", tt.level, result, tt.expected)
			}
		})
	}
}

// TestMapSeveritySlowPathAbbreviations verifies that non-standard severity
// abbreviations still work via the slow path after the fast-path optimization.
func TestMapSeveritySlowPathAbbreviations(t *testing.T) {
	eng := &Engine{}

	tests := []struct {
		input    string
		expected AlertSeverity
	}{
		{"INFO", SeverityLow},
		{"Info", SeverityLow},
		{"MED", SeverityMedium},
		{"Med", SeverityMedium},
		{"CRIT", SeverityCritical},
		{"Crit", SeverityCritical},
		{"HIGH", SeverityHigh},
		{"MEDIUM", SeverityMedium},
		{"CRITICAL", SeverityCritical},
		{"unknown_level", SeverityMedium},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := eng.mapSeverity(sigma.Level(tt.input))
			if result != tt.expected {
				t.Errorf("mapSeverity(%s) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

// TestEvaluateConcurrentSafety verifies that Engine.Evaluate is safe for
// concurrent use after the refactor to snapshot the rules slice.
func TestEvaluateConcurrentSafety(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "engine_concurrent_test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	testRule := `
title: Concurrent Test Rule
id: concurrent-rule-1
status: experimental
description: Rule for concurrent access testing
logsource:
  category: test
detection:
  selection:
    command|contains: "dangerous"
  condition: selection
level: high
`
	if err := os.WriteFile(filepath.Join(tmpDir, "concurrent.yml"), []byte(testRule), 0644); err != nil {
		t.Fatal(err)
	}

	eng, err := NewEngine(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	const goroutines = 20
	const iterations = 100

	// Run Evaluate concurrently
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				results := eng.Evaluate(map[string]string{
					"command": "dangerous_command",
				})
				if len(results) != 1 {
					t.Errorf("expected 1 match, got %d", len(results))
					return
				}
			}
		}()
	}

	wg.Wait()
}

// TestEvaluateSnapshotIsolation verifies that the Evaluate method works
// with a snapshot of rules and is not affected by concurrent GetStats calls.
func TestEvaluateSnapshotIsolation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "engine_snapshot_test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	testRule := `
title: Snapshot Test Rule
id: snapshot-rule-1
status: experimental
description: Rule for snapshot isolation testing
logsource:
  category: test
detection:
  selection:
    event_type: "tool_call"
  condition: selection
level: medium
`
	if err := os.WriteFile(filepath.Join(tmpDir, "snapshot.yml"), []byte(testRule), 0644); err != nil {
		t.Fatal(err)
	}

	eng, err := NewEngine(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup

	// Run Evaluate and GetStats concurrently
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			eng.Evaluate(map[string]string{"event_type": "tool_call"})
		}()
		go func() {
			defer wg.Done()
			eng.GetStats()
		}()
	}

	wg.Wait()
}
