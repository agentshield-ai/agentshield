package server

import (
	"context"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// N2/A6: a decoy plugin span hides a session's native spans; deleting it
// shows them again, and a project admin can restore a session and stop
// honouring plugin claims for it. Members and viewers cannot.
func TestSourceHidingUndone(t *testing.T) {
	e := newSecEnv(t, Config{})
	member := e.creds["member"]
	ctx := context.Background()
	st := e.s.store
	native := func(id, sess string) {
		s := sess
		if _, err := st.UpsertComplete(ctx, []store.CompleteCall{{Start: store.CallStart{ProjectID: "m/own", ID: id, TraceID: id, OpName: "op",
			ThreadID: &s, StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Inputs: map[string]any{"a": 1},
			Attributes: map[string]any{"weft": map[string]any{"source": store.SourceClaudeCodeLogs}}}}}); err != nil {
			t.Fatal(err)
		}
	}
	decoy := func(id, sess string) {
		t.Helper()
		span := map[string]any{"span": map[string]any{"id": id, "trace_id": id, "name": "turn", "session_id": sess,
			"attributes": map[string]any{"weft": map[string]any{"source": store.SourceClaudeCodePlugin}}}}
		if code, out := e.send("POST", "/api/v1/spans/batch", member, mustJSON(map[string]any{"project_id": "m/own", "items": []any{span}}),
			map[string]string{"X-Weft-Source": store.SourceClaudeCodePlugin}); code != 200 {
			t.Fatalf("decoy -> %d %s", code, out)
		}
	}
	live := func(sess string) map[string]bool {
		out := map[string]bool{}
		for _, x := range e.admJSON("POST", "/api/v1/spans/query", "root", map[string]any{"project_id": "m/own",
			"filter": map[string]any{"session_ids": []any{sess}}})["items"].([]any) {
			out[x.(map[string]any)["id"].(string)] = true
		}
		return out
	}

	// (a) Deleting the spans that outrank the native ones shows them again.
	native("a-native1", "SA")
	native("a-native2", "SA")
	decoy("a-decoy", "SA")
	if l := live("SA"); l["a-native1"] || !l["a-decoy"] {
		t.Fatalf("decoy did not hide (setup): %v", l)
	}
	e.mustCode(200, "POST", "/api/v1/spans/delete", member, map[string]any{"project_id": "m/own", "ids": []any{"a-decoy"}})
	if l := live("SA"); !l["a-native1"] || !l["a-native2"] || l["a-decoy"] {
		t.Fatalf("after deleting the decoy: %v", l)
	}
	// Hidden spans a deletion names stay deleted.
	decoy("a-decoy2", "SA")
	e.mustCode(200, "POST", "/api/v1/spans/delete", member, map[string]any{"project_id": "m/own", "ids": []any{"a-native2"}})
	e.mustCode(200, "POST", "/api/v1/spans/delete", member, map[string]any{"project_id": "m/own", "ids": []any{"a-decoy2"}})
	if l := live("SA"); !l["a-native1"] || l["a-native2"] {
		t.Fatalf("after deleting a hidden span and the decoy: %v", l)
	}

	// (b) A project admin restores a session and turns plugin claims off.
	native("b-native", "SB")
	decoy("b-decoy", "SB")
	req := map[string]any{"project_id": "m/own", "session_id": "SB"}
	info := e.admJSON("POST", "/api/v1/spans/sessions/sources", e.creds["viewer"], req)
	if srcs := info["sources"].([]any); len(srcs) != 2 || srcs[0].(map[string]any)["source"] != store.SourceClaudeCodePlugin ||
		srcs[1].(map[string]any)["hidden"].(float64) != 1 || info["ignore_plugin_claims"] != false {
		t.Fatalf("session sources = %v", info)
	}
	for _, who := range []string{"member", "viewer"} {
		e.mustCode(403, "POST", "/api/v1/spans/sessions/sources/restore", e.creds[who], req)
	}
	res := e.admJSON("POST", "/api/v1/spans/sessions/sources/restore", "root", req)
	if res["restored"].(float64) != 1 || res["ignore_plugin_claims"] != true {
		t.Fatalf("restore = %v", res)
	}
	if l := live("SB"); !l["b-native"] || !l["b-decoy"] {
		t.Fatalf("after restore: %v", l)
	}
	// Later claims for the session are not honoured.
	decoy("b-decoy2", "SB")
	native("b-native2", "SB")
	if l := live("SB"); !l["b-native"] || !l["b-native2"] || !l["b-decoy2"] {
		t.Fatalf("a claim after the restore: %v", l)
	}
	sp := e.admJSON("GET", "/api/v1/spans/b-decoy2?project_id=m/own", "root", nil)
	if w, _ := sp["attributes"].(map[string]any)["weft"].(map[string]any); w["claimed_source"] != store.SourceClaudeCodePlugin || w["source"] != nil {
		t.Fatalf("claim after restore: %v", sp["attributes"])
	}
	// Other sessions are unaffected.
	native("c-native", "SC")
	decoy("c-decoy", "SC")
	if l := live("SC"); l["c-native"] {
		t.Fatalf("other session: %v", l)
	}
}
