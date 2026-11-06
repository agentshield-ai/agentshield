package otlp

import (
	"testing"

	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// testdata/codex/traces_json is a sample of a real Codex CLI 0.167.0 trace
// export (codex exec resume, the second process of the resume_json run):
// the turn's run_turn / codex.sampling / handle_responses (gen_ai.usage.*
// without a model) / exec_command spans, a few internal ones (auth,
// append_items, persist_rollout_items) and session-less roots
// (thread/items/list, session_loop). Paths are replaced.

func setServiceName(req *coltrace.ExportTraceServiceRequest, name string) {
	for _, rs := range req.GetResourceSpans() {
		for _, kv := range rs.GetResource().GetAttributes() {
			if kv.GetKey() == "service.name" {
				kv.GetValue().Value = &common.AnyValue_StringValue{StringValue: name}
			}
		}
	}
}

func spanCount(req *coltrace.ExportTraceServiceRequest) int {
	n := 0
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			n += len(ss.GetSpans())
		}
	}
	return n
}

func TestCodexTracesDropped(t *testing.T) {
	reqs := loadExports(t, "codex/traces_json")
	if len(reqs) != 1 || spanCount(reqs[0]) < 15 {
		t.Fatalf("fixture: %d requests", len(reqs))
	}
	tests := []struct {
		service string
		kept    bool
	}{
		{"codex_exec", false}, // as captured
		{"codex_cli_rs", false},
		{"codex-tui", false},
		{"codex", false},
		{"my-codex-app", true}, // only a codex prefix is Codex
		{"something-else", true},
	}
	for _, tc := range tests {
		t.Run(tc.service, func(t *testing.T) {
			req := proto.Clone(reqs[0]).(*coltrace.ExportTraceServiceRequest)
			setServiceName(req, tc.service)
			calls, err := ToCalls(req, ocProject)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.kept {
				if len(calls) != 0 {
					t.Fatalf("%s: %d calls stored, want none", tc.service, len(calls))
				}
				return
			}
			if len(calls) != spanCount(req) {
				t.Fatalf("%s: %d calls, want every span (%d)", tc.service, len(calls), spanCount(req))
			}
		})
	}

	t.Run("mixed export keeps the other resource", func(t *testing.T) {
		req := proto.Clone(reqs[0]).(*coltrace.ExportTraceServiceRequest)
		other := proto.Clone(req.GetResourceSpans()[0]).(*tracepb.ResourceSpans)
		req.ResourceSpans = append(req.ResourceSpans, other)
		for _, kv := range other.GetResource().GetAttributes() {
			if kv.GetKey() == "service.name" {
				kv.GetValue().Value = &common.AnyValue_StringValue{StringValue: "my-service"}
			}
		}
		calls, err := ToCalls(req, ocProject)
		if err != nil {
			t.Fatal(err)
		}
		if want := spanCount(req) / 2; len(calls) != want {
			t.Fatalf("calls = %d, want %d (the non-Codex resource only)", len(calls), want)
		}
	})

	t.Run("store stays empty", func(t *testing.T) {
		h := newTraceHarness(t)
		if n := h.ingest(loadExports(t, "codex/traces_json")...); n != 0 {
			t.Fatalf("%d calls ingested", n)
		}
		if calls := h.all(); len(calls) != 0 {
			t.Fatalf("%d calls stored", len(calls))
		}
	})
}
