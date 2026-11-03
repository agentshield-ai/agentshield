// Package otlp converts OTLP/HTTP trace exports into Weft calls. It
// understands the OpenTelemetry GenAI semantic conventions, OpenInference
// and OpenLLMetry (Traceloop) attribute layouts, plus weft.* attributes.
package otlp

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// MaxBodyBytes bounds a decompressed export request.
const MaxBodyBytes = 64 << 20

// IsJSON reports whether a content type is OTLP JSON.
func IsJSON(contentType string) bool {
	mt, _, _ := mime.ParseMediaType(contentType)
	return mt == "application/json"
}

// ReadBody reads a possibly gzip-encoded body with a size limit.
func ReadBody(r io.Reader, encoding string) ([]byte, error) {
	if strings.EqualFold(encoding, "gzip") {
		zr, err := gzip.NewReader(r)
		if err != nil {
			return nil, fmt.Errorf("invalid gzip body: %w", err)
		}
		defer zr.Close()
		r = zr
	}
	b, err := io.ReadAll(io.LimitReader(r, MaxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxBodyBytes {
		return nil, fmt.Errorf("request body too large")
	}
	return b, nil
}

// Decode parses an ExportTraceServiceRequest.
func Decode(body []byte, isJSON bool) (*coltrace.ExportTraceServiceRequest, error) {
	req := &coltrace.ExportTraceServiceRequest{}
	if !isJSON {
		if err := proto.Unmarshal(body, req); err != nil {
			return nil, fmt.Errorf("invalid OTLP protobuf: %w", err)
		}
		return req, nil
	}
	// OTLP JSON encodes trace and span ids as hex, while protojson expects
	// base64 for bytes fields. Rewrite them before decoding.
	var generic any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&generic); err != nil {
		return nil, fmt.Errorf("invalid OTLP JSON: %w", err)
	}
	fixIDs(generic)
	fixed, err := json.Marshal(generic)
	if err != nil {
		return nil, err
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(fixed, req); err != nil {
		return nil, fmt.Errorf("invalid OTLP JSON: %w", err)
	}
	return req, nil
}

var idKeys = map[string]bool{"traceId": true, "spanId": true, "parentSpanId": true, "trace_id": true, "span_id": true, "parent_span_id": true}

func fixIDs(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			if s, ok := x.(string); ok && idKeys[k] {
				if b, err := hex.DecodeString(s); err == nil && (len(b) == 8 || len(b) == 16) {
					t[k] = base64.StdEncoding.EncodeToString(b)
				}
				continue
			}
			fixIDs(x)
		}
	case []any:
		for _, x := range t {
			fixIDs(x)
		}
	}
}

// EncodeResponse returns an empty ExportTraceServiceResponse.
func EncodeResponse(isJSON bool, rejected int64, msg string) ([]byte, string) {
	resp := &coltrace.ExportTraceServiceResponse{}
	if rejected > 0 {
		resp.PartialSuccess = &coltrace.ExportTracePartialSuccess{RejectedSpans: rejected, ErrorMessage: msg}
	}
	if isJSON {
		b, _ := protojson.Marshal(resp)
		return b, "application/json"
	}
	b, _ := proto.Marshal(resp)
	return b, "application/x-protobuf"
}

// AnyValue converts an OTLP AnyValue to a plain Go value.
func AnyValue(v *common.AnyValue) any {
	if v == nil {
		return nil
	}
	switch x := v.Value.(type) {
	case *common.AnyValue_StringValue:
		return x.StringValue
	case *common.AnyValue_BoolValue:
		return x.BoolValue
	case *common.AnyValue_IntValue:
		return json.Number(strconv.FormatInt(x.IntValue, 10))
	case *common.AnyValue_DoubleValue:
		return json.Number(strconv.FormatFloat(x.DoubleValue, 'g', -1, 64))
	case *common.AnyValue_ArrayValue:
		out := make([]any, 0, len(x.ArrayValue.GetValues()))
		for _, e := range x.ArrayValue.GetValues() {
			out = append(out, AnyValue(e))
		}
		return out
	case *common.AnyValue_KvlistValue:
		return KeyValues(x.KvlistValue.GetValues())
	case *common.AnyValue_BytesValue:
		return base64.StdEncoding.EncodeToString(x.BytesValue)
	}
	return nil
}

// KeyValues converts OTLP attributes to a flat map.
func KeyValues(kvs []*common.KeyValue) map[string]any {
	out := make(map[string]any, len(kvs))
	for _, kv := range kvs {
		out[kv.GetKey()] = AnyValue(kv.GetValue())
	}
	return out
}

func nanosToTime(ns uint64) string {
	if ns == 0 {
		return time.Now().UTC().Format(time.RFC3339Nano)
	}
	return time.Unix(0, int64(ns)).UTC().Format(time.RFC3339Nano)
}

var spanKinds = map[tracepb.Span_SpanKind]string{
	tracepb.Span_SPAN_KIND_INTERNAL: "INTERNAL", tracepb.Span_SPAN_KIND_SERVER: "SERVER", tracepb.Span_SPAN_KIND_CLIENT: "CLIENT",
	tracepb.Span_SPAN_KIND_PRODUCER: "PRODUCER", tracepb.Span_SPAN_KIND_CONSUMER: "CONSUMER",
}

// ProjectFor picks the target project for a resource.
func ProjectFor(headerProject string, resource map[string]any) string {
	if headerProject != "" {
		return headerProject
	}
	if p, ok := resource["weft.project"].(string); ok && p != "" {
		return p
	}
	ent, _ := resource["wandb.entity"].(string)
	p, _ := resource["wandb.project"].(string)
	if p != "" {
		if ent == "" {
			ent = "default"
		}
		return ent + "/" + p
	}
	return "default/default"
}

// ToCalls converts an export request into calls.
func ToCalls(req *coltrace.ExportTraceServiceRequest, headerProject string) ([]store.CompleteCall, error) {
	var out []store.CompleteCall
	for _, rs := range req.GetResourceSpans() {
		resource := KeyValues(rs.GetResource().GetAttributes())
		project, err := refs.NormalizeProject(ProjectFor(headerProject, resource))
		if err != nil {
			return nil, err
		}
		for _, ss := range rs.GetScopeSpans() {
			scope := map[string]any{"name": ss.GetScope().GetName(), "version": ss.GetScope().GetVersion()}
			for _, sp := range ss.GetSpans() {
				c, err := spanToCall(project, sp, resource, scope)
				if err != nil {
					return nil, err
				}
				out = append(out, c)
			}
		}
	}
	return out, nil
}

func spanToCall(project string, sp *tracepb.Span, resource, scope map[string]any) (store.CompleteCall, error) {
	if len(sp.GetSpanId()) == 0 || len(sp.GetTraceId()) == 0 {
		return store.CompleteCall{}, fmt.Errorf("span without trace or span id")
	}
	attrs := KeyValues(sp.GetAttributes())
	events := make([]map[string]any, 0, len(sp.GetEvents()))
	for _, ev := range sp.GetEvents() {
		events = append(events, map[string]any{
			"name": ev.GetName(), "time": nanosToTime(ev.GetTimeUnixNano()), "attributes": KeyValues(ev.GetAttributes()),
		})
	}
	links := make([]map[string]any, 0, len(sp.GetLinks()))
	for _, l := range sp.GetLinks() {
		links = append(links, map[string]any{
			"trace_id": hex.EncodeToString(l.GetTraceId()), "span_id": hex.EncodeToString(l.GetSpanId()), "attributes": KeyValues(l.GetAttributes()),
		})
	}
	id := hex.EncodeToString(sp.GetSpanId())
	st := store.CallStart{
		ProjectID: project,
		ID:        id,
		TraceID:   hex.EncodeToString(sp.GetTraceId()),
		OpName:    firstString(attrs, "weft.op_name", "weave.op_name"),
		StartedAt: nanosToTime(sp.GetStartTimeUnixNano()),
	}
	if st.OpName == "" {
		st.OpName = sp.GetName()
	}
	if st.OpName == "" {
		st.OpName = "span"
	}
	if len(sp.GetParentSpanId()) > 0 {
		p := hex.EncodeToString(sp.GetParentSpanId())
		st.ParentID = &p
	}
	st.DisplayName = optString(attrs, "weft.display_name", "wandb.display_name", "weave.display_name")
	if st.DisplayName == nil {
		st.DisplayName = frameworkDisplayName(attrs)
	}
	st.ThreadID = optString(attrs, "weft.thread_id", "wandb.thread_id", "gen_ai.conversation.id", "session.id",
		"gcp.vertex.agent.session_id", "ai.telemetry.metadata.sessionId", "ai.telemetry.metadata.threadId", "ai.telemetry.metadata.conversationId")
	st.TurnID = optString(attrs, "weft.turn_id", "wandb.turn_id")
	if st.TurnID == nil && (isTrue(attrs["wandb.is_turn"]) || isTrue(attrs["weft.is_turn"])) {
		st.TurnID = &id
	}
	st.WBUserID = optString(attrs, "user.id", "enduser.id", "ai.telemetry.metadata.userId")

	user := map[string]any{}
	for k, v := range attrs {
		if strings.HasPrefix(k, "weft.attributes.") {
			user[strings.TrimPrefix(k, "weft.attributes.")] = v
		}
	}
	status := map[string]any{"code": sp.GetStatus().GetCode().String(), "message": sp.GetStatus().GetMessage()}
	user["otel"] = map[string]any{
		"span_name": sp.GetName(), "kind": spanKinds[sp.GetKind()], "status": status, "resource": resource,
		"scope": scope, "attributes": attrs, "events": events, "links": links,
	}
	applySpanMeta(attrs, &st, user) // langfuse.go
	st.Attributes = user
	st.Inputs = extractInputs(attrs, events)

	if sp.GetEndTimeUnixNano() == 0 {
		return store.CompleteCall{Start: st}, nil
	}
	en := &store.CallEnd{EndedAt: nanosToTime(sp.GetEndTimeUnixNano()), Summary: map[string]any{}}
	en.Output = extractOutput(attrs, events)
	if model, u := extractUsage(attrs); model != "" && len(u) > 0 {
		en.Summary["usage"] = map[string]any{model: u}
	}
	if sp.GetStatus().GetCode() == tracepb.Status_STATUS_CODE_ERROR {
		msg := exceptionFromEvents(events)
		if msg == "" {
			msg = sp.GetStatus().GetMessage()
		}
		if msg == "" {
			msg = "error"
		}
		en.Exception = &msg
	}
	if en.Exception == nil {
		en.Exception = langfuseError(attrs) // langfuse.go
	}
	return store.CompleteCall{Start: st, End: en}, nil
}

func firstString(attrs map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := attrs[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func optString(attrs map[string]any, keys ...string) *string {
	if s := firstString(attrs, keys...); s != "" {
		return &s
	}
	return nil
}

func exceptionFromEvents(events []map[string]any) string {
	for _, ev := range events {
		if ev["name"] != "exception" {
			continue
		}
		a, _ := ev["attributes"].(map[string]any)
		typ, _ := a["exception.type"].(string)
		msg, _ := a["exception.message"].(string)
		stack, _ := a["exception.stacktrace"].(string)
		out := msg
		if typ != "" {
			out = typ + ": " + msg
		}
		if stack != "" {
			out += "\n\n" + stack
		}
		return out
	}
	return ""
}

// parseMaybeJSON decodes s when it holds a JSON object or array.
func parseMaybeJSON(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	t := strings.TrimSpace(s)
	if len(t) > 1 && (t[0] == '{' || t[0] == '[') {
		if d, err := refs.Decode([]byte(t)); err == nil {
			return d
		}
	}
	return s
}

// indexedMessages collects flattened prefix.N.<field...> attributes.
func indexedMessages(attrs map[string]any, prefix string, fieldPrefix string) []map[string]any {
	byIdx := map[int]map[string]any{}
	for k, v := range attrs {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		rest := k[len(prefix):]
		dot := strings.IndexByte(rest, '.')
		if dot < 0 {
			continue
		}
		n, err := strconv.Atoi(rest[:dot])
		if err != nil {
			continue
		}
		field := strings.TrimPrefix(rest[dot+1:], fieldPrefix)
		m := byIdx[n]
		if m == nil {
			m = map[string]any{}
			byIdx[n] = m
		}
		m[field] = v
	}
	if len(byIdx) == 0 {
		return nil
	}
	idxs := make([]int, 0, len(byIdx))
	for i := range byIdx {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	out := make([]map[string]any, 0, len(idxs))
	for _, i := range idxs {
		out = append(out, normalizeFlatMessage(byIdx[i]))
	}
	return out
}

// normalizeFlatMessage turns flat fields (role, content, tool_calls.0.tool_call.function.name ...) into a chat message.
func normalizeFlatMessage(f map[string]any) map[string]any {
	msg := map[string]any{}
	toolCalls := map[int]map[string]any{}
	contents := map[int]*flatPart{}
	for k, v := range f {
		switch {
		case k == "role" || k == "content" || k == "name" || k == "tool_call_id" || k == "finish_reason":
			msg[k] = v
		case strings.HasPrefix(k, "contents."):
			addFlatPart(contents, strings.TrimPrefix(k, "contents."), v)
		case strings.HasPrefix(k, "tool_calls."):
			rest := strings.TrimPrefix(k, "tool_calls.")
			dot := strings.IndexByte(rest, '.')
			if dot < 0 {
				continue
			}
			n, err := strconv.Atoi(rest[:dot])
			if err != nil {
				continue
			}
			field := strings.TrimPrefix(rest[dot+1:], "tool_call.")
			tc := toolCalls[n]
			if tc == nil {
				tc = map[string]any{"type": "function", "function": map[string]any{}}
				toolCalls[n] = tc
			}
			fn := tc["function"].(map[string]any)
			switch field {
			case "id":
				tc["id"] = v
			case "function.name", "name":
				fn["name"] = v
			case "function.arguments", "arguments":
				fn["arguments"] = v
			case "function", "type":
				if f, ok := decodeAny(v).(map[string]any); ok && field == "function" {
					if n, ok := f["name"]; ok {
						fn["name"] = n
					}
					if a, ok := f["arguments"]; ok {
						fn["arguments"] = a
					}
				} else if field == "type" {
					if t, ok := v.(string); ok && t != "" {
						tc["type"] = t
					}
				}
			}
		}
	}
	if _, ok := msg["content"]; !ok && len(contents) > 0 {
		msg["content"] = flatContent(contents)
	}
	if len(toolCalls) > 0 {
		idxs := make([]int, 0, len(toolCalls))
		for i := range toolCalls {
			idxs = append(idxs, i)
		}
		sort.Ints(idxs)
		var tcs []any
		for _, i := range idxs {
			tcs = append(tcs, toolCalls[i])
		}
		msg["tool_calls"] = tcs
	}
	return msg
}

// flatPart is one OpenInference message content part
// (contents.N.message_content.type / .text).
type flatPart struct{ typ, text string }

func addFlatPart(parts map[int]*flatPart, rest string, v any) {
	dot := strings.IndexByte(rest, '.')
	if dot < 0 {
		return
	}
	n, err := strconv.Atoi(rest[:dot])
	if err != nil {
		return
	}
	field := strings.TrimPrefix(rest[dot+1:], "message_content.")
	s, ok := v.(string)
	if !ok {
		return
	}
	p := parts[n]
	if p == nil {
		p = &flatPart{}
		parts[n] = p
	}
	switch field {
	case "text":
		p.text = s
	case "type":
		p.typ = s
	}
}

// flatContent joins text parts in order; reasoning parts make the content a
// parts list so the reasoning stays separate from the reply.
func flatContent(parts map[int]*flatPart) any {
	idxs := make([]int, 0, len(parts))
	reasoning := false
	for i, p := range parts {
		idxs = append(idxs, i)
		if p.typ == "reasoning" || p.typ == "thinking" {
			reasoning = true
		}
	}
	sort.Ints(idxs)
	if !reasoning {
		var texts []string
		for _, i := range idxs {
			if p := parts[i]; p.text != "" && (p.typ == "" || p.typ == "text") {
				texts = append(texts, p.text)
			}
		}
		return strings.Join(texts, "\n")
	}
	var out []any
	for _, i := range idxs {
		p := parts[i]
		switch p.typ {
		case "reasoning", "thinking":
			out = append(out, map[string]any{"type": "thinking", "thinking": p.text})
		case "", "text":
			if p.text != "" {
				out = append(out, map[string]any{"type": "text", "text": p.text})
			}
		}
	}
	return out
}

// partsMessages converts GenAI semconv messages with "parts" into chat
// messages. Text parts are joined; media (blob/uri/file) and reasoning parts
// are kept as content parts; tool calls become OpenAI tool_calls.
func partsMessages(v any) []any {
	list, ok := parseMaybeJSON(v).([]any)
	if !ok {
		return nil
	}
	out := make([]any, 0, len(list))
	for _, x := range list {
		m, ok := x.(map[string]any)
		if !ok {
			out = append(out, x)
			continue
		}
		parts, ok := m["parts"].([]any)
		if !ok {
			out = append(out, m)
			continue
		}
		role := m["role"]
		if r, ok := role.(string); ok {
			role = strings.ToLower(r) // Koog sends Kotlin enum names ("User", "Assistant")
		}
		if len(parts) == 0 && role == "tool" {
			continue // Koog emits an empty tool entry before each user message
		}
		msg := map[string]any{"role": role}
		if fr, ok := m["finish_reason"]; ok {
			msg["finish_reason"] = fr
		}
		if n, ok := m["name"]; ok {
			msg["name"] = n
		}
		var texts []string
		var tcs []any
		var content []any // non-text parts, in order, with text parts interleaved
		responses := 0
		for _, p := range parts {
			pm, ok := p.(map[string]any)
			if !ok {
				continue
			}
			switch pm["type"] {
			case "text":
				if s, ok := pm["content"].(string); ok {
					texts = append(texts, s)
					content = append(content, map[string]any{"type": "text", "text": s})
				}
			case "tool_call":
				args := pm["arguments"]
				if _, isStr := args.(string); !isStr && args != nil {
					b, _ := json.Marshal(args)
					args = string(b)
				}
				tcs = append(tcs, map[string]any{"id": pm["id"], "type": "function",
					"function": map[string]any{"name": pm["name"], "arguments": args}})
			case "tool_call_response":
				responses++
				msg["tool_call_id"] = pm["id"]
				r := firstNonNil(pm["response"], pm["result"])
				if _, isStr := r.(string); !isStr && r != nil {
					b, _ := json.Marshal(r)
					r = string(b)
				}
				texts = append(texts, fmt.Sprint(r))
				content = append(content, map[string]any{"type": "text", "text": fmt.Sprint(r)})
			default:
				cp, _ := genAIPart(pm)
				content = append(content, cp)
			}
		}
		if responses > 0 && responses == len(parts) {
			msg["role"] = "tool"
		}
		if len(content) > len(texts) {
			msg["content"] = content
		} else {
			msg["content"] = strings.Join(texts, "\n")
		}
		if len(tcs) > 0 {
			msg["tool_calls"] = tcs
		}
		out = append(out, msg)
	}
	return out
}

func messagesToAny(ms []map[string]any) []any {
	out := make([]any, len(ms))
	for i, m := range ms {
		out[i] = m
	}
	return out
}

var requestParams = map[string]string{
	"gen_ai.request.temperature": "temperature", "gen_ai.request.max_tokens": "max_tokens", "gen_ai.request.top_p": "top_p",
	"gen_ai.request.top_k": "top_k", "gen_ai.request.frequency_penalty": "frequency_penalty",
	"gen_ai.request.presence_penalty": "presence_penalty", "gen_ai.request.stop_sequences": "stop", "gen_ai.request.seed": "seed",
}

func extractInputs(attrs map[string]any, events []map[string]any) map[string]any {
	inputs := map[string]any{}
	setFrom := func(v any) bool {
		v = parseMaybeJSON(v)
		if m, ok := v.(map[string]any); ok {
			for k, x := range m {
				inputs[k] = x
			}
			return true
		}
		if v != nil {
			inputs["input"] = v
			return true
		}
		return false
	}
	found := false
	if v, ok := attrs["weft.inputs"]; ok {
		found = setFrom(v)
	}
	if !found {
		if in := preferredInputs(attrs); in != nil {
			for k, x := range in {
				inputs[k] = x
			}
			found = true
		}
	}
	if !found {
		if v, ok := attrs["input.value"]; ok {
			found = setFrom(normalizeLangChain(parseMaybeJSON(v)))
		}
	}
	if !found {
		if in := frameworkInputs(attrs); in != nil {
			for k, x := range in {
				inputs[k] = x
			}
			found = true
		}
	}
	if !found {
		if v, ok := attrs["gen_ai.input.messages"]; ok {
			if msgs := partsMessages(v); msgs != nil {
				if sys, ok := attrs["gen_ai.system_instructions"]; ok && !startsWithSystem(msgs) {
					if s := partsText(sys); s != "" {
						msgs = append([]any{map[string]any{"role": "system", "content": s}}, msgs...)
					}
				}
				inputs["messages"] = msgs
				found = true
			}
		}
	}
	if !found {
		if msgs := indexedMessages(attrs, "llm.input_messages.", "message."); msgs != nil {
			inputs["messages"] = messagesToAny(msgs)
			found = true
		}
	}
	if !found {
		if msgs := indexedMessages(attrs, "gen_ai.prompt.", ""); msgs != nil {
			inputs["messages"] = messagesToAny(msgs)
			found = true
		}
	}
	if !found {
		if v, ok := attrs["gen_ai.prompt"]; ok {
			pv := parseMaybeJSON(v)
			if l, ok := pv.([]any); ok {
				inputs["messages"] = l
			} else {
				inputs["prompt"] = pv
			}
			found = true
		}
	}
	if !found {
		if v, ok := attrs["traceloop.entity.input"]; ok {
			found = setFrom(v)
		}
	}
	if !found {
		var msgs []any
		for _, ev := range events {
			name, _ := ev["name"].(string)
			a, _ := ev["attributes"].(map[string]any)
			switch name {
			case "gen_ai.system.message", "gen_ai.user.message", "gen_ai.assistant.message", "gen_ai.tool.message":
				role := strings.TrimSuffix(strings.TrimPrefix(name, "gen_ai."), ".message")
				m := map[string]any{"role": role, "content": parseMaybeJSON(firstNonNil(a["content"], a["gen_ai.event.content"]))}
				if r, ok := a["role"].(string); ok {
					m["role"] = r
				}
				msgs = append(msgs, m)
			case "gen_ai.content.prompt":
				if p, ok := a["gen_ai.prompt"]; ok {
					inputs["prompt"] = parseMaybeJSON(p)
					found = true
				}
			}
		}
		if len(msgs) > 0 {
			inputs["messages"] = msgs
			found = true
		}
	}
	if found || hasPrefix(attrs, "gen_ai.") || hasPrefix(attrs, "llm.") || hasPrefix(attrs, "ai.model.") {
		if m := firstString(attrs, "gen_ai.request.model", "llm.model_name", "ai.model.id"); m != "" {
			if _, exists := inputs["model"]; !exists {
				inputs["model"] = m
			}
		}
		for k, name := range requestParams {
			if v, ok := attrs[k]; ok {
				if _, exists := inputs[name]; !exists {
					inputs[name] = v
				}
			}
		}
		if v, ok := attrs["llm.invocation_parameters"]; ok {
			if m, ok := parseMaybeJSON(v).(map[string]any); ok {
				for k, x := range m {
					if _, exists := inputs[k]; !exists {
						inputs[k] = x
					}
				}
			}
		}
	}
	return inputs
}

func partsText(v any) string {
	list, ok := parseMaybeJSON(v).([]any)
	if !ok {
		s, _ := v.(string)
		return s
	}
	var texts []string
	for _, p := range list {
		if m, ok := p.(map[string]any); ok {
			if s, ok := m["content"].(string); ok {
				texts = append(texts, s)
			}
		}
	}
	return strings.Join(texts, "\n")
}

func firstNonNil(vs ...any) any {
	for _, v := range vs {
		if v != nil {
			return v
		}
	}
	return nil
}

func hasPrefix(attrs map[string]any, p string) bool {
	for k := range attrs {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// choices wraps assistant messages in an OpenAI chat completion shape.
func choices(msgs []any, attrs map[string]any) map[string]any {
	cs := make([]any, 0, len(msgs))
	for i, m := range msgs {
		mm, _ := m.(map[string]any)
		fr := any(nil)
		if mm != nil {
			if f, ok := mm["finish_reason"]; ok {
				fr = f
				delete(mm, "finish_reason")
			}
			if _, ok := mm["role"]; !ok {
				mm["role"] = "assistant"
			}
		}
		cs = append(cs, map[string]any{"index": json.Number(strconv.Itoa(i)), "message": m, "finish_reason": fr})
	}
	out := map[string]any{"object": "chat.completion", "choices": cs}
	if m := firstString(attrs, "gen_ai.response.model", "gen_ai.request.model", "llm.model_name"); m != "" {
		out["model"] = m
	}
	if id := firstString(attrs, "gen_ai.response.id"); id != "" {
		out["id"] = id
	}
	return out
}

func extractOutput(attrs map[string]any, events []map[string]any) any {
	if v, ok := attrs["weft.output"]; ok {
		return parseMaybeJSON(v)
	}
	if out := preferredOutput(attrs); out != nil {
		return out
	}
	if v, ok := attrs["output.value"]; ok {
		return normalizeLangChain(parseMaybeJSON(v))
	}
	if out, ok := frameworkOutput(attrs); ok {
		return out
	}
	if v, ok := attrs["gen_ai.output.messages"]; ok {
		if msgs := partsMessages(v); msgs != nil {
			return choices(msgs, attrs)
		}
	}
	if msgs := indexedMessages(attrs, "llm.output_messages.", "message."); msgs != nil {
		return choices(messagesToAny(msgs), attrs)
	}
	if msgs := indexedMessages(attrs, "gen_ai.completion.", ""); msgs != nil {
		return choices(messagesToAny(msgs), attrs)
	}
	if v, ok := attrs["gen_ai.completion"]; ok {
		return parseMaybeJSON(v)
	}
	if v, ok := attrs["traceloop.entity.output"]; ok {
		return parseMaybeJSON(v)
	}
	var msgs []any
	for _, ev := range events {
		a, _ := ev["attributes"].(map[string]any)
		switch ev["name"] {
		case "gen_ai.choice":
			m := map[string]any{"role": "assistant"}
			if msg, ok := parseMaybeJSON(a["message"]).(map[string]any); ok {
				for k, v := range msg {
					m[k] = v
				}
			} else if c, ok := a["content"]; ok {
				m["content"] = c
			}
			if fr, ok := a["finish_reason"]; ok {
				m["finish_reason"] = fr
			}
			msgs = append(msgs, m)
		case "gen_ai.content.completion":
			if c, ok := a["gen_ai.completion"]; ok {
				return parseMaybeJSON(c)
			}
		}
	}
	if len(msgs) > 0 {
		return choices(msgs, attrs)
	}
	return nil
}

func extractUsage(attrs map[string]any) (string, map[string]any) {
	model := firstString(attrs, "gen_ai.response.model", "gen_ai.request.model", "llm.model_name")
	if model == "" {
		model = frameworkModel(attrs)
	}
	u := map[string]any{}
	set := func(name string, keys ...string) {
		for _, k := range keys {
			if v, ok := attrs[k]; ok {
				if f, ok := store.ToFloat(v); ok {
					u[name] = json.Number(strconv.FormatFloat(f, 'f', -1, 64))
					return
				}
			}
		}
	}
	set("prompt_tokens", "gen_ai.usage.input_tokens", "gen_ai.usage.prompt_tokens", "llm.token_count.prompt", "llm.usage.prompt_tokens")
	set("completion_tokens", "gen_ai.usage.output_tokens", "gen_ai.usage.completion_tokens", "llm.token_count.completion", "llm.usage.completion_tokens")
	set("total_tokens", "gen_ai.usage.total_tokens", "llm.token_count.total", "llm.usage.total_tokens")
	frameworkUsage(attrs, set)
	structuredUsage(attrs, u)
	if len(u) == 0 {
		return "", nil
	}
	if _, ok := u["total_tokens"]; !ok {
		p, _ := store.ToFloat(u["prompt_tokens"])
		c, _ := store.ToFloat(u["completion_tokens"])
		u["total_tokens"] = json.Number(strconv.FormatFloat(p+c, 'f', -1, 64))
	}
	if model == "" {
		model = "unknown"
	}
	u["requests"] = json.Number("1")
	return model, u
}
