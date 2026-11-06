package otlp

// Pieces shared by the event-log mappings of Codex (codex_logs.go) and
// Gemini CLI (gemini_logs.go): event keys and times, and the span writer.
// Both produce the shape of the Claude Code mapping (claudecode.go):
//
//	invoke_agent <harness>       one per prompt (a turn); session = the harness's session id
//	├─ chat <model>              each model request, with tokens
//	├─ execute_tool <tool>       each tool call, gen_ai.tool.call.id = the harness's call id
//	└─ invoke_agent <sub-agent>  a sub-agent, under the tool call that ran it

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// logEventKey identifies a log record: a re-sent record has the same key,
// two records differ in at least one attribute or their time.
func logEventKey(name, session, ts string, raw map[string]any) string {
	canon, _ := json.Marshal(raw)
	h := sha256.Sum256([]byte(name + "\x00" + session + "\x00" + ts + "\x00" + string(canon)))
	return hex.EncodeToString(h[:16])
}

// logEventTime is a record's time (unix µs): its event.timestamp, else the
// record time, else the time it was observed, else now.
func logEventTime(raw map[string]any, timeNs, observedNs uint64) int64 {
	if t, err := time.Parse(time.RFC3339Nano, aStr(raw, "event.timestamp")); err == nil {
		return t.UnixMicro()
	}
	if timeNs > 0 {
		return int64(timeNs / 1000)
	}
	if observedNs > 0 {
		return int64(observedNs / 1000)
	}
	return time.Now().UnixMicro()
}

// spanWriter collects the spans of one rebuilt group.
type spanWriter struct {
	session, turnID, source, convName string
	out                               []store.CompleteCall
}

// common adds the session attributes every span carries.
func (w *spanWriter) common(attrs map[string]any) map[string]any {
	attrs["gen_ai.conversation.id"] = w.session
	if w.convName != "" {
		attrs["gen_ai.conversation.name"] = w.convName
	}
	return attrs
}

func (w *spanWriter) weft(kind string) map[string]any {
	return map[string]any{"span_kind": kind, "source": w.source}
}

// add appends a span of the turn; an unended span is left open (running).
func (w *spanWriter) add(id, parent, name, display string, startUS int64, attrs, inputs map[string]any, endUS int64, ended bool, output any, exc *string, summary map[string]any) {
	st := store.CallStart{ID: id, TraceID: w.turnID, OpName: name, StartedAt: usToTime(startUS), Attributes: attrs, Inputs: inputs,
		ReplaceDisplayName: true}
	if parent != "" {
		st.ParentID = &parent
	}
	if display != "" {
		st.DisplayName = &display
	}
	s, t := w.session, w.turnID
	st.ThreadID, st.TurnID = &s, &t
	c := store.CompleteCall{Start: st}
	if ended {
		if endUS < startUS {
			endUS = startUS
		}
		if summary == nil {
			summary = map[string]any{}
		}
		c.End = &store.CallEnd{EndedAt: usToTime(endUS), Output: output, Exception: exc, Summary: summary}
	}
	w.out = append(w.out, c)
}

// usageSummary is summary.usage for one model request. prompt is gross
// (cached tokens included), as Weft counts it.
func usageSummary(model string, prompt, completion, cacheRead, cacheWrite, reasoning float64) map[string]any {
	if prompt+completion == 0 {
		return nil
	}
	e := map[string]any{"requests": 1, "prompt_tokens": prompt, "completion_tokens": completion, "total_tokens": prompt + completion}
	if cacheRead > 0 {
		e["cache_read_input_tokens"] = cacheRead
	}
	if cacheWrite > 0 {
		e["cache_creation_input_tokens"] = cacheWrite
	}
	if reasoning > 0 {
		e["reasoning_tokens"] = reasoning
	}
	if model == "" {
		model = "unknown"
	}
	return map[string]any{"usage": map[string]any{model: e}}
}

func strPtr(s string) *string { return &s }

func compactJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
