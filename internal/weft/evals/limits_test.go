package evals

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/llm"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// LevenshteinScorer refuses texts over MaxLevenshteinRunes at once (a pair
// of 50k-character texts took 6.6 s before), and stops when ctx is done.
func TestLevenshteinBounded(t *testing.T) {
	long := strings.Repeat("a", 1_000_000)
	val := map[string]any{"_class_name": "LevenshteinScorer"}
	start := time.Now()
	_, err := scoreBuiltin(context.Background(), "LevenshteinScorer", val, map[string]any{"expected": long + "b"}, long)
	if err == nil {
		t.Fatal("1M-character texts scored")
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("refused after %v", d)
	}
	got, err := scoreBuiltin(context.Background(), "LevenshteinScorer", val, map[string]any{"expected": "kitten"}, "sitting")
	if err != nil || fmt.Sprintf("%.4f", got) != "0.5714" {
		t.Fatalf("kitten/sitting = %v, %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := []rune(strings.Repeat("ab", 5000))
	if _, err := levenshtein(ctx, a, a); err == nil {
		t.Fatal("cancelled distance ran")
	}
}

// A project whose monitors are slow holds at most monitorsPerProject
// workers: another project's spans are still evaluated. And Close does not
// wait for stuck monitors beyond its grace period.
func TestMonitorWorkersSharedAcrossProjects(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	defer func(g, w time.Duration) { closeGrace, closeWait = g, w }(closeGrace, closeWait)
	closeGrace, closeWait = 200*time.Millisecond, 200*time.Millisecond
	release := make(chan struct{})
	bDone := make(chan struct{}, 1)
	monitorRunHook = func(ctx context.Context, c store.EndedCall) {
		switch c.ProjectID {
		case "a/slow":
			<-release // a stuck judge: ignores its context
		case "b/fast":
			bDone <- struct{}{}
		}
	}
	defer func() { monitorRunHook = nil }()
	e := New(st, llm.New(nil, nil), nil)
	var calls []store.EndedCall
	for i := 0; i < 100; i++ {
		calls = append(calls, store.EndedCall{ProjectID: "a/slow", ID: fmt.Sprint("s", i)})
	}
	e.Notify(calls)
	time.Sleep(50 * time.Millisecond)
	e.Notify([]store.EndedCall{{ProjectID: "b/fast", ID: "f"}})
	select {
	case <-bDone:
	case <-time.After(2 * time.Second):
		t.Fatal("project b's span waited behind project a's stuck monitors")
	}
	start := time.Now()
	e.Close()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Close took %v", d)
	}
	close(release)
}

// A span is scored once per monitor and scorer, however often it is
// re-sent or rebuilt (each rebuild used to add a feedback row, and an LLM
// judge call).
func TestMonitorScoresSpanOnce(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := New(st, llm.New(nil, nil), nil)
	defer e.Close()
	ctx := context.Background()
	pid := "acme/mon"
	d, _, err := st.CreateObject(ctx, pid, "exact", map[string]any{"_class_name": "ExactMatchScorer", "_bases": []any{"Scorer", "Object"}}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	scorerRef := "weft:///acme/mon/object/exact:" + d
	if _, _, err := st.CreateObject(ctx, pid, "mon", map[string]any{"_class_name": "Monitor", "_bases": []any{"Object"}, "name": "mon",
		"active": true, "sampling_rate": 1.0, "scorers": []any{scorerRef}}, "", nil); err != nil {
		t.Fatal(err)
	}
	span := store.CompleteCall{Start: store.CallStart{ProjectID: pid, ID: "s1", TraceID: "s1", OpName: "answer", StartedAt: "2026-01-01T00:00:00Z",
		Inputs: map[string]any{"expected": "x"}}, End: &store.CallEnd{EndedAt: "2026-01-01T00:00:01Z", Output: "x"}}
	if _, err := st.UpsertComplete(ctx, []store.CompleteCall{span}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := e.runMonitors(ctx, store.EndedCall{ProjectID: pid, ID: "s1"}); err != nil {
			t.Fatal(err)
		}
	}
	fbs, err := st.QueryFeedback(ctx, pid, nil, nil, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range fbs {
		if f.FeedbackType == "weft.scorer.exact" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d feedback rows for one span, want 1", n)
	}
}
