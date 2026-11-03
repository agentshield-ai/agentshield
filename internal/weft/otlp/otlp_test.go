package otlp

import (
	"testing"

	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func strAttr(k, v string) *common.KeyValue {
	return &common.KeyValue{Key: k, Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: v}}}
}

func intAttr(k string, v int64) *common.KeyValue {
	return &common.KeyValue{Key: k, Value: &common.AnyValue{Value: &common.AnyValue_IntValue{IntValue: v}}}
}

func TestProtobufGenAISpan(t *testing.T) {
	req := &coltrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource: &resourcepb.Resource{Attributes: []*common.KeyValue{strAttr("service.name", "svc")}},
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{
			TraceId: []byte("0123456789abcdef"), SpanId: []byte("spanid01"), ParentSpanId: []byte("parent01"),
			Name: "chat gpt-4o", StartTimeUnixNano: 1_700_000_000_000_000_000, EndTimeUnixNano: 1_700_000_001_000_000_000,
			Attributes: []*common.KeyValue{
				strAttr("gen_ai.request.model", "gpt-4o"),
				strAttr("gen_ai.input.messages", `[{"role":"user","parts":[{"type":"text","content":"hi"}]}]`),
				strAttr("gen_ai.output.messages", `[{"role":"assistant","parts":[{"type":"text","content":"hello"}],"finish_reason":"stop"}]`),
				intAttr("gen_ai.usage.input_tokens", 3), intAttr("gen_ai.usage.output_tokens", 2),
				strAttr("gen_ai.conversation.id", "conv-1"), strAttr("weft.attributes.env", "test"),
			},
			Status: &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR, Message: "rate limited"},
		}}}},
	}}}
	b, _ := proto.Marshal(req)
	got, err := Decode(b, false)
	if err != nil {
		t.Fatal(err)
	}
	calls, err := ToCalls(got, "e/p")
	if err != nil || len(calls) != 1 {
		t.Fatalf("ToCalls: %v %d", err, len(calls))
	}
	c := calls[0]
	if c.Start.ProjectID != "e/p" || c.Start.OpName != "chat gpt-4o" || *c.Start.ParentID == "" || *c.Start.ThreadID != "conv-1" {
		t.Fatalf("start = %+v", c.Start)
	}
	msgs := c.Start.Inputs["messages"].([]any)
	if msgs[0].(map[string]any)["content"] != "hi" || c.Start.Inputs["model"] != "gpt-4o" {
		t.Fatalf("inputs = %v", c.Start.Inputs)
	}
	out := c.End.Output.(map[string]any)
	choice := out["choices"].([]any)[0].(map[string]any)
	if choice["message"].(map[string]any)["content"] != "hello" || choice["finish_reason"] != "stop" {
		t.Fatalf("output = %v", out)
	}
	if c.End.Exception == nil || *c.End.Exception != "rate limited" {
		t.Fatalf("exception = %v", c.End.Exception)
	}
	u := c.End.Summary["usage"].(map[string]any)["gpt-4o"].(map[string]any)
	if u["prompt_tokens"].(interface{ String() string }).String() != "3" || u["total_tokens"].(interface{ String() string }).String() != "5" {
		t.Fatalf("usage = %v", u)
	}
	if c.Start.Attributes["env"] != "test" || c.Start.Attributes["otel"] == nil {
		t.Fatalf("attributes = %v", c.Start.Attributes)
	}
}

func TestJSONHexIDsAndOpenInference(t *testing.T) {
	body := []byte(`{"resourceSpans":[{"resource":{"attributes":[{"key":"weft.project","value":{"stringValue":"proj"}}]},
	 "scopeSpans":[{"scope":{"name":"openinference"},"spans":[{"traceId":"5b8efff798038103d269b633813fc60c","spanId":"eee19b7ec3c1b174",
	  "name":"llm","kind":3,"startTimeUnixNano":"1700000000000000000","endTimeUnixNano":"1700000000500000000",
	  "attributes":[{"key":"llm.input_messages.0.message.role","value":{"stringValue":"user"}},
	                {"key":"llm.input_messages.0.message.content","value":{"stringValue":"2+2?"}},
	                {"key":"llm.output_messages.0.message.role","value":{"stringValue":"assistant"}},
	                {"key":"llm.output_messages.0.message.content","value":{"stringValue":"4"}},
	                {"key":"llm.model_name","value":{"stringValue":"claude-sonnet-4"}},
	                {"key":"llm.token_count.prompt","value":{"intValue":"5"}},
	                {"key":"session.id","value":{"stringValue":"s1"}}]}]}]}]}`)
	req, err := Decode(body, true)
	if err != nil {
		t.Fatal(err)
	}
	calls, err := ToCalls(req, "")
	if err != nil {
		t.Fatal(err)
	}
	c := calls[0]
	if c.Start.ID != "eee19b7ec3c1b174" || c.Start.TraceID != "5b8efff798038103d269b633813fc60c" {
		t.Fatalf("ids = %s %s", c.Start.ID, c.Start.TraceID)
	}
	if c.Start.ProjectID != "default/proj" || *c.Start.ThreadID != "s1" {
		t.Fatalf("project/thread = %s %v", c.Start.ProjectID, c.Start.ThreadID)
	}
	if c.Start.Inputs["messages"].([]any)[0].(map[string]any)["content"] != "2+2?" {
		t.Fatalf("inputs = %v", c.Start.Inputs)
	}
	if c.End.Summary["usage"].(map[string]any)["claude-sonnet-4"] == nil {
		t.Fatalf("usage = %v", c.End.Summary)
	}
	if resp, _ := EncodeResponse(true, 0, ""); string(resp) != "{}" {
		t.Fatalf("json response = %s", resp)
	}
}
