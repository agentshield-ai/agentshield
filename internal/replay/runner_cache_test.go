package replay

import (
	"encoding/json"
	"testing"
)

// stubFetcher returns a single fixed page of trace rows for tests. The page
// is sized to the entire dataset so the runner exits cleanly after one call.
type stubFetcher struct {
	rows []TraceRow
}

func (s *stubFetcher) FetchPage(offset int) ([]TraceRow, int, error) {
	if offset >= len(s.rows) {
		return nil, len(s.rows), nil
	}
	return s.rows[offset:], len(s.rows), nil
}

// makeNlileRow constructs a single nlile-format trace row carrying one Bash
// tool_use with the given command, so we can stitch together a stream of
// repeating-versus-unique events.
func makeNlileRow(rowIdx int, command string) TraceRow {
	messages := []map[string]interface{}{
		{"role": "user", "content": "Run a command"},
		{
			"role": "assistant",
			"content": []interface{}{
				map[string]interface{}{
					"type":  "tool_use",
					"id":    "tu",
					"name":  "Bash",
					"input": map[string]interface{}{"command": command},
				},
			},
		},
	}
	mj, _ := json.Marshal(messages)
	return TraceRow{
		RowIdx: rowIdx,
		Row:    map[string]interface{}{"messages_json": string(mj)},
	}
}

// TestRun_CacheHitRate_Repeating: when the trace stream consists of one unique
// command repeated N times, every evaluation after the first should hit the
// cache. Validates that the verdict cache is wired up in library mode.
func TestRun_CacheHitRate_Repeating(t *testing.T) {
	rulesDir := findRulesDir(t)

	const repeats = 20
	rows := make([]TraceRow, repeats)
	for i := range rows {
		rows[i] = makeNlileRow(i, "ls -la")
	}

	cfg := RunConfig{
		Dataset:  "nlile/test-cache-repeating",
		RulesDir: rulesDir,
		Mode:     "audit",
		Fetcher:  &stubFetcher{rows: rows},
	}
	report, err := Run(cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := report.CacheStats
	if !got.Enabled {
		t.Fatalf("CacheStats.Enabled = false, want true")
	}
	if got.HitCount != repeats-1 {
		t.Errorf("HitCount = %d, want %d", got.HitCount, repeats-1)
	}
	if got.MissCount != 1 {
		t.Errorf("MissCount = %d, want 1", got.MissCount)
	}
	wantHitRate := float64(repeats-1) / float64(repeats) * 100
	if abs(got.HitRate-wantHitRate) > 0.01 {
		t.Errorf("HitRate = %.2f, want %.2f", got.HitRate, wantHitRate)
	}
}

// TestRun_CacheHitRate_AllUnique: when every event has a unique command,
// the cache never hits and the report reflects that.
func TestRun_CacheHitRate_AllUnique(t *testing.T) {
	rulesDir := findRulesDir(t)

	const n = 10
	rows := make([]TraceRow, n)
	for i := range rows {
		rows[i] = makeNlileRow(i, formatCmd(i))
	}

	cfg := RunConfig{
		Dataset:  "nlile/test-cache-unique",
		RulesDir: rulesDir,
		Mode:     "audit",
		Fetcher:  &stubFetcher{rows: rows},
	}
	report, err := Run(cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := report.CacheStats
	if got.HitCount != 0 {
		t.Errorf("HitCount = %d, want 0 (every event unique)", got.HitCount)
	}
	if got.MissCount != n {
		t.Errorf("MissCount = %d, want %d", got.MissCount, n)
	}
	if got.HitRate != 0 {
		t.Errorf("HitRate = %.2f, want 0", got.HitRate)
	}
}

// TestRun_NoCache: --no-cache disables wiring; HitCount stays 0 and Enabled
// is false in the report.
func TestRun_NoCache(t *testing.T) {
	rulesDir := findRulesDir(t)

	rows := []TraceRow{
		makeNlileRow(0, "ls -la"),
		makeNlileRow(1, "ls -la"),
		makeNlileRow(2, "ls -la"),
	}

	cfg := RunConfig{
		Dataset:      "nlile/test-no-cache",
		RulesDir:     rulesDir,
		Mode:         "audit",
		DisableCache: true,
		Fetcher:      &stubFetcher{rows: rows},
	}
	report, err := Run(cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := report.CacheStats
	if got.Enabled {
		t.Errorf("Enabled = true, want false (--no-cache set)")
	}
	if got.HitCount != 0 {
		t.Errorf("HitCount = %d, want 0 with cache disabled", got.HitCount)
	}
	if got.MissCount != 3 {
		t.Errorf("MissCount = %d, want 3", got.MissCount)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func formatCmd(i int) string {
	return "echo unique-" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [12]byte
	n := len(buf)
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[n:])
}
