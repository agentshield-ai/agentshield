package store

import (
	"context"
	"strings"
	"testing"
)

// searchSession is reasoningSession plus a tool call with arguments and a
// second, unrelated session.
func searchSession() []gs {
	spans := reasoningSession()
	spans = append(spans,
		gs{id: "t2", parent: "A", name: "execute_tool bash", start: 150, dur: 20, attrs: map[string]any{"gen_ai.operation.name": "execute_tool",
			"gen_ai.tool.name": "bash"}, inputs: map[string]any{"command": "pytest -k test_exfil_guard"}},
		gs{id: "Z", trace: "other", name: "invoke_agent helper", start: 0, dur: 10, attrs: agentAttrs("helper", "rs2"),
			inputs: map[string]any{"messages": []any{map[string]any{"role": "user", "content": "Say hi"}}}},
	)
	return spans
}

func sessionIDs(t *testing.T, st *Store, pid, q string) string {
	t.Helper()
	l, _, err := st.QuerySessions(context.Background(), SessionsQuery{ProjectID: pid, Q: q, SortBy: "session_id"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range l {
		ids = append(ids, s.SessionID)
	}
	return strings.Join(ids, ",")
}

// The session list's q matches reasoning, message text and tool calls, not
// only the session's id, name, title, team and agents.
func TestSessionSearchMatchesReasoningMessagesAndTools(t *testing.T) {
	for oi, o := range ingestOrders {
		t.Run(o.name, func(t *testing.T) {
			st := openTestStore(t)
			pid := "acme/s" + string(rune('a'+oi))
			o.write(t, st, pid, searchSession())
			for q, want := range map[string]string{
				"writer should take":  "rs1", // reasoning step
				"SHORT AND VIVID":     "rs1", // reasoning, case-insensitive
				"oslo is wet":         "rs1", // assistant message
				"Handing over":        "rs1", // assistant message
				"about oslo weather":  "rs1", // first user request
				"get_weather":         "rs1", // tool name
				"test_exfil_guard":    "rs1", // tool arguments
				`"city"`:              "rs1", // arguments of a requested tool call
				"say hi":              "rs2",
				"helper":              "rs2", // agent name, as before
				"no such text at all": "",
				"100%":                "", // LIKE wildcards are literal
			} {
				if got := sessionIDs(t, st, pid, q); got != want {
					t.Errorf("q=%q: sessions %q, want %q", q, got, want)
				}
			}
			// Reasoning steps of a session can be searched too.
			steps, total, err := st.SessionReasoning(context.Background(), SessionReasoningQuery{ProjectID: pid, SessionID: "rs1", Q: "weather"})
			if err != nil || total != 2 || len(steps) != 2 {
				t.Fatalf("reasoning q: %d %v", total, err)
			}
		})
	}
}

// Databases written before the search index are backfilled once at Open.
func TestSessionSearchBackfill(t *testing.T) {
	st := openTestStore(t)
	pid := "acme/bf"
	ingestOrders[0].write(t, st, pid, searchSession())
	db := st.DB()
	for _, q := range []string{`DELETE FROM span_search`, `DELETE FROM weft_backfills WHERE name = 'span_search_v1'`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if got := sessionIDs(t, st, pid, "test_exfil_guard"); got != "" {
		t.Fatalf("index not cleared: %q", got)
	}
	if err := backfillSpanSearch(db); err != nil {
		t.Fatal(err)
	}
	if got := sessionIDs(t, st, pid, "test_exfil_guard"); got != "rs1" {
		t.Fatalf("after backfill: %q", got)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM weft_backfills WHERE name = 'span_search_v1'`).Scan(&n)
	if n != 1 {
		t.Fatal("backfill not recorded")
	}
}

func TestCapTextKeepsUTF8(t *testing.T) {
	s := capText([]string{strings.Repeat("é", searchTextCap)})
	if len(s) > searchTextCap || !strings.HasPrefix(strings.Repeat("é", 10), s[:20]) {
		t.Fatalf("len %d", len(s))
	}
	for _, r := range s {
		if r == '�' {
			t.Fatal("split character")
		}
	}
}
