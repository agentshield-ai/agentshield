package store

import (
	"strings"
	"testing"
)

func TestAttrKeys(t *testing.T) {
	for key, want := range map[string]string{
		AttrSpanName:             "weft.span.name,weft.op_name",
		AttrSessionID:            "weft.session_id,weft.thread_id",
		AttrParentSpanID:         "weft.parent_span.id,weft.parent_call.id",
		AttrParentSpanTraceID:    "weft.parent_span.trace_id,weft.parent_call.trace_id",
		AttrParentSpan:           "weft.parent_span,weft.parent_call",
		AttrEvalPredictionSpanID: "weft.eval.prediction_span_id,weft.eval.predict_and_score_call_id",
		AttrLinkedSpans:          "weft.linked_spans,weft.linked_calls",
		AttrEvalRunID:            "weft.eval.run_id",
		"weft.turn_id":           "weft.turn_id",
	} {
		if got := strings.Join(AttrKeys(key), ","); got != want {
			t.Errorf("AttrKeys(%s) = %s, want %s", key, got, want)
		}
	}
}

func TestRenamedAttrPath(t *testing.T) {
	for _, c := range []struct {
		path  string
		toOld bool
		want  string // "" when nothing is renamed
	}{
		{"weft.parent_span.id", true, "weft|parent_call|id"},
		{"weft.parent_call.trace_id", false, "weft|parent_span|trace_id"},
		{"weft.parent_span", true, "weft|parent_call"},
		{"weft.linked_spans.0.span_id", true, "weft|linked_calls|0|call_id"},
		{"weft.linked_calls.1.call_id", false, "weft|linked_spans|1|span_id"},
		{"weft.linked_spans.0.trace_id", true, "weft|linked_calls|0|trace_id"},
		{"weft.span.name", true, "weft|op_name"},
		{"weft.eval.prediction_span_id", true, "weft|eval|predict_and_score_call_id"},
		{"[weft.session_id]", true, "weft.thread_id"},
		{"otel.attributes.[weft.parent_span.id]", true, "otel|attributes|weft.parent_call.id"},
		{"otel.attributes.[weft.thread_id]", false, "otel|attributes|weft.session_id"},
		{"[weft.linked_spans].0.span_id", true, "weft.linked_calls|0|call_id"},
		{"weft.eval.run_id", true, ""},
		{"weft.parent_call.id", true, ""},
		{"gen_ai.agent.name", true, ""},
	} {
		got, ok := renamedAttrPath(splitField(c.path), c.toOld)
		if s := strings.Join(got, "|"); (c.want == "") == ok || s != c.want {
			t.Errorf("renamedAttrPath(%s, %v) = %q %v, want %q", c.path, c.toOld, s, ok, c.want)
		}
	}
}
