package compat

import (
	"github.com/agentshield-ai/agentshield/internal/weft/compat/legacy"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// RewriteOTLP renames legacy attribute keys in an OTLP export request before
// it is mapped: span attributes (weave.eval.*, wandb.thread_id, ...) get
// their weft.* names, and a wandb.entity / wandb.project resource becomes
// weft.project.
func RewriteOTLP(req *coltrace.ExportTraceServiceRequest) {
	for _, rs := range req.GetResourceSpans() {
		if res := rs.GetResource(); res != nil {
			res.Attributes = rewriteResource(res.Attributes)
		}
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				sp.Attributes = renameKeys(sp.Attributes)
			}
		}
	}
}

func renameKeys(kvs []*commonpb.KeyValue) []*commonpb.KeyValue {
	have := make(map[string]bool, len(kvs))
	for _, kv := range kvs {
		have[kv.GetKey()] = true
	}
	out := kvs[:0]
	for _, kv := range kvs {
		if nk, ok := legacy.AttributeKey(kv.GetKey()); ok {
			if have[nk] {
				continue // Weft's own key wins
			}
			kv.Key, have[nk] = nk, true
		}
		out = append(out, kv)
	}
	return out
}

func rewriteResource(kvs []*commonpb.KeyValue) []*commonpb.KeyValue {
	var ent, proj string
	for _, kv := range kvs {
		switch kv.GetKey() {
		case "weft.project":
			return kvs
		case legacy.ResourceEntity:
			ent = kv.GetValue().GetStringValue()
		case legacy.ResourceProject:
			proj = kv.GetValue().GetStringValue()
		}
	}
	if proj == "" {
		return kvs
	}
	if ent == "" {
		ent = "default"
	}
	return append(kvs, &commonpb.KeyValue{Key: "weft.project",
		Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: ent + "/" + proj}}})
}
