package store

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"
)

// A builder that shows the grouping: one span per group (its id) holding
// one child per event, named after the event.
func groupingBuilder(g LogGroup) []CompleteCall {
	turn := "turn-" + g.SessionID + "-" + g.GroupID
	s := g.SessionID
	at := time.UnixMicro(g.Events[0].TimeUS).UTC().Format(time.RFC3339Nano)
	attrs := map[string]any{"weft": map[string]any{"source": "test-logs"}}
	out := []CompleteCall{{Start: CallStart{ID: turn, TraceID: turn, OpName: "turn", ThreadID: &s, StartedAt: at, Attributes: attrs}}}
	for _, e := range g.Events {
		p := turn
		out = append(out, CompleteCall{Start: CallStart{ID: "ev-" + s + "-" + e.Key, TraceID: turn, ParentID: &p, OpName: e.Name, ThreadID: &s, StartedAt: at, Attributes: attrs}})
	}
	return out
}

var groupingSource = LogSource{Name: "test", SpanSource: "test-logs", Build: groupingBuilder}

// tree lists a session's live event spans by group: "group:event,event".
func groupingTree(t *testing.T, s *Store, session string) string {
	t.Helper()
	calls, err := s.ListCalls(context.Background(), CallsQuery{ProjectID: "acme/logs", Filter: &CallsFilter{ThreadIDs: []string{session}}})
	if err != nil {
		t.Fatal(err)
	}
	groups := map[string][]string{}
	for _, c := range calls {
		if c.ParentID != nil {
			g := strings.TrimPrefix(*c.ParentID, "turn-"+session+"-")
			groups[g] = append(groups[g], c.OpName)
		}
	}
	var out []string
	for g, evs := range groups {
		sort.Strings(evs)
		out = append(out, g+":"+strings.Join(evs, ","))
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

func TestLogEventsGroupedByTime(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	t0 := time.Now().Add(-time.Hour).UnixMicro()
	ev := func(key, session string, sec int64, opens bool) LogEvent {
		e := LogEvent{Key: key, SessionID: session, Name: key, TimeUS: t0 + sec*1e6, Attrs: map[string]any{}}
		if opens {
			e.Opens, e.GroupID = true, "g-"+key
		}
		return e
	}
	put := func(evs ...LogEvent) {
		t.Helper()
		if _, _, err := s.IngestLogEvents(ctx, "acme/logs", groupingSource, evs); err != nil {
			t.Fatal(err)
		}
	}
	want := func(step, session, tree string) {
		t.Helper()
		if got := groupingTree(t, s, session); got != tree {
			t.Fatalf("%s: %q, want %q", step, got, tree)
		}
	}

	// Events before any prompt wait; the prompt that precedes them claims them.
	put(ev("a2", "S", 2, false), ev("a3", "S", 3, false))
	want("no prompt yet", "S", "")
	put(ev("p1", "S", 1, true))
	want("first prompt", "S", "g-p1:a2,a3,p1")
	// A later prompt takes the events after it, even ones stored before it.
	put(ev("b6", "S", 6, false))
	want("before the second prompt", "S", "g-p1:a2,a3,b6,p1")
	put(ev("p5", "S", 5, true), ev("b7", "S", 7, false))
	want("second prompt", "S", "g-p1:a2,a3,p1 g-p5:b6,b7,p5")
	// Re-sent events change nothing.
	if _, n, err := s.IngestLogEvents(ctx, "acme/logs", groupingSource, []LogEvent{ev("a2", "S", 2, false), ev("p5", "S", 5, true)}); err != nil || n != 0 {
		t.Fatalf("re-sent: %d new, %v", n, err)
	}

	// A linked session (a sub-agent): alone it is a session of its own...
	put(ev("q1", "C", 3, true), ev("c4", "C", 4, false))
	want("child alone", "C", "g-q1:c4,q1")
	// ...and once linked its events join the parent's group open at their
	// time, its own spans are retired, and later events follow.
	link := ev("spawn", "S", 3, false)
	link.LinkChild = "C"
	put(link, ev("c8", "C", 8, false))
	want("child linked", "C", "")
	want("parent after the link", "S", "g-p1:a2,a3,c4,p1,q1,spawn g-p5:b6,b7,c8,p5")
	// A session linked under the child lands in the parent too.
	link2 := ev("spawn2", "C", 9, false)
	link2.LinkChild = "D"
	put(link2, ev("d10", "D", 10, false))
	want("grandchild", "S", "g-p1:a2,a3,c4,p1,q1,spawn g-p5:b6,b7,c8,d10,p5,spawn2")
}

// Ranks hold within a harness: a Codex source never retires Claude Code
// spans of the same session id, nor the other way round.
func TestSessionSourcesPerHarness(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	at := time.Now().Add(-time.Minute).UTC()
	if _, err := s.UpsertComplete(ctx, ccSpans(SourceCodexLogs, "S", "codex-logs", "call_1", at)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertComplete(ctx, ccSpans(SourceClaudeCodePlugin, "S", "cc-plugin", "toolu_1", at)); err != nil {
		t.Fatal(err)
	}
	if got := liveIDs(t, s, "S"); len(got) != 6 {
		t.Fatalf("Claude Code's plugin retired Codex spans: %v", got)
	}
	if _, err := s.UpsertComplete(ctx, ccSpans(SourceCodexPlugin, "S", "codex-plugin", "call_1", at)); err != nil {
		t.Fatal(err)
	}
	got := liveIDs(t, s, "S")
	if len(got) != 6 || got["codex-logs-turn"] || !got["codex-plugin-turn"] || !got["cc-plugin-turn"] {
		t.Fatalf("after the Codex plugin: %v", got)
	}
	if SessionSourceRank(SourceGeminiLogs) <= SessionSourceRank(SourceGeminiTraces) {
		t.Fatal("Gemini CLI's log events should outrank its traces")
	}
}
