package otlp

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// The session title (the first prompt of the session's earliest turn) is
// not part of any span's content: a back-dated prompt that becomes the
// session's first turn re-titles the session without re-storing the spans
// of its other turns, and a later batch of a long turn stores only the
// spans its own events changed.
func TestClaudeCodeLogsBackdatedOpenerDoesNotRewriteOtherTurns(t *testing.T) {
	h := newCCHarness(t)
	ctx := context.Background()
	const sess = "sess-backdate"
	sc := newScript(sess, "prompt-long")
	sc.add("user_prompt", map[string]any{"prompt": "the real first prompt"})
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("toolu_%02d", i)
		sc.add("tool_decision", map[string]any{"tool_name": "Bash", "decision": "accept", "tool_use_id": id})
		sc.add("tool_result", map[string]any{"tool_name": "Bash", "success": "true", "duration_ms": json1(5), "tool_use_id": id})
	}
	h.ingest(sc.events)
	if got := conversationNames(t, h, sess); len(got) != 1 || !got["the real first prompt"] {
		t.Fatalf("titles before = %v", got)
	}

	// A back-dated opener: a prompt an hour before the session started.
	old := sc.t0.Add(-time.Hour).Format("2006-01-02T15:04:05.000Z")
	opener := ccLogEvent("user_prompt", sess, "prompt-old", map[string]any{"session.id": sess, "prompt.id": "prompt-old",
		"event.name": "user_prompt", "event.timestamp": old, "prompt": "BACKDATED TITLE"}, nil, 0, 0)
	ctx1, st1 := store.WithIngestStats(ctx)
	if _, _, err := h.s.IngestLogEvents(ctx1, ccProject, LogSources[0], []store.LogEvent{opener}); err != nil {
		t.Fatal(err)
	}
	if st1.SpansStored > 2 {
		t.Fatalf("back-dated opener stored %d spans", st1.SpansStored)
	}

	// One more event of the long turn: only what it changes is stored.
	n := len(sc.events)
	sc.add("tool_decision", map[string]any{"tool_name": "Bash", "decision": "accept", "tool_use_id": "toolu_new"})
	ctx2, st2 := store.WithIngestStats(ctx)
	if _, _, err := h.s.IngestLogEvents(ctx2, ccProject, LogSources[0], sc.events[n:]); err != nil {
		t.Fatal(err)
	}
	if st2.SpansStored > 3 || st2.SpansUnchanged < 20 {
		t.Fatalf("one event of the long turn stored %d spans (%d unchanged)", st2.SpansStored, st2.SpansUnchanged)
	}
	// Every span of the session carries the session's title: the earliest prompt.
	if got := conversationNames(t, h, sess); len(got) != 1 || !got["BACKDATED TITLE"] {
		t.Fatalf("titles after = %v", got)
	}
}

func json1(n int) any { return n }

// conversationNames lists the conversation names of a session's spans.
func conversationNames(t *testing.T, h *ccHarness, sess string) map[string]bool {
	t.Helper()
	limit := 10000
	res, err := h.s.QueryAgentSpans(context.Background(), store.AgentSpansQueryReq{ProjectID: ccProject, Limit: &limit})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, sp := range res.Spans {
		if sp.ConversationID == nil || *sp.ConversationID != sess {
			continue
		}
		name := ""
		if sp.ConversationName != nil {
			name = *sp.ConversationName
		}
		out[name] = true
	}
	return out
}
