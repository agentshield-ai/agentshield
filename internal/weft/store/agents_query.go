package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// maxAgentSpans bounds how many spans one agents query aggregates in memory.
const maxAgentSpans = 200000

// agentSpan is an index row joined with the call's timing, status and usage.
type agentSpan struct {
	ID, TraceID, ParentID                       string
	Op, SpanName, OpName, DisplayName           string
	OwnAgent                                    bool
	AgentName, AgentID, AgentVersion, AgentDesc string
	ConvID, ConvName                            string
	IsTurn                                      bool
	ToolName, ToolCallID                        string
	RequestModel, ResponseModel, Provider       string
	StartedUS, EndedUS                          int64
	Ended                                       bool
	Exception                                   *string
	InTok, OutTok, ReasonTok, CacheRead, CacheW float64
	InCost, OutCost                             float64 // InCost includes the cache costs below
	CacheReadCost, CacheWCost                   float64
	HasCost                                     bool
	UserID, RunID                               string
	ParentCallID, ParentCallTraceID             string // weft.parent_call.*: the op this span was emitted from
	IsInvocation                                bool
	attrsJSON                                   string
	custom                                      map[string]any
}

// parentCallCol reads weft.parent_call.<field> (DESIGN.md §4): the op a span
// was emitted from, stamped on spans that start their own trace inside an
// op. It is read from SDK attributes (nested) or OTLP span attributes (flat
// keys).
func parentCallCol(field string) string { return parentCallColOf("c", field) }

type agentSpanFilter struct {
	After, Before   int64
	AgentName       string
	ConversationIDs []string
	TraceIDs        []string
	IDs             []string
	WithAttrs       bool
	// AllCalls includes calls with no agent, conversation or operation.
	AllCalls bool
	// Eval keeps the spans of an evaluation run (applied by filteredSpans).
	Eval *AgentEvalFilter
}

func (s *Store) loadAgentSpans(ctx context.Context, projectID string, f agentSpanFilter) ([]*agentSpan, error) {
	pb, err := s.LoadPriceBook(ctx, projectID)
	if err != nil {
		return nil, err
	}
	cols := `s.id, s.trace_id, COALESCE(s.parent_id, ''), s.operation_name, s.span_name, s.own_agent, s.agent_name, s.agent_id,
		s.agent_version, s.agent_description, s.conversation_id, s.conversation_name, s.is_turn, s.tool_name, s.tool_call_id,
		s.request_model, s.response_model, s.provider_name, c.op_name, COALESCE(c.display_name, ''), c.started_at, c.ended_at,
		c.exception, c.summary, c.rollup, c.cumulative, COALESCE(c.user_id, ''), COALESCE(c.run_id, ''),
		` + parentCallCol("id") + `, ` + parentCallCol("trace_id")
	if f.WithAttrs {
		cols += ", c.attributes"
	}
	where := []string{"s.project_id = ?", "c.deleted_at IS NULL"}
	args := []any{projectID}
	if !f.AllCalls {
		where = append(where, "(s.agent_name != '' OR s.conversation_id != '' OR s.operation_name != '')",
			// Judge, monitor and signal LLM calls Weft makes itself are not agent activity.
			"COALESCE(json_extract(c.attributes, '$.weft.internal'), 0) = 0")
	}
	if f.After > 0 {
		where = append(where, "c.started_at >= ?")
		args = append(args, f.After)
	}
	if f.Before > 0 {
		where = append(where, "c.started_at < ?")
		args = append(args, f.Before)
	}
	if f.AgentName != "" {
		where = append(where, "s.agent_name = ?")
		args = append(args, f.AgentName)
	}
	for _, l := range []struct {
		col  string
		vals []string
	}{{"s.conversation_id", f.ConversationIDs}, {"s.trace_id", f.TraceIDs}, {"s.id", f.IDs}} {
		if l.vals == nil {
			continue
		}
		if len(l.vals) == 0 {
			return nil, nil
		}
		c, a := inList(l.col, l.vals)
		where = append(where, c)
		args = append(args, a...)
	}
	rows, err := s.r.QueryContext(ctx, `SELECT `+cols+` FROM agent_spans s JOIN calls c ON c.project_id = s.project_id AND c.id = s.id
		WHERE `+strings.Join(where, " AND ")+` ORDER BY c.started_at LIMIT `+strconv.Itoa(maxAgentSpans), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*agentSpan
	for rows.Next() {
		sp := &agentSpan{}
		var started, ended sql.NullInt64
		var exc sql.NullString
		var summary, rollup string
		var cum bool
		dest := []any{&sp.ID, &sp.TraceID, &sp.ParentID, &sp.Op, &sp.SpanName, &sp.OwnAgent, &sp.AgentName, &sp.AgentID,
			&sp.AgentVersion, &sp.AgentDesc, &sp.ConvID, &sp.ConvName, &sp.IsTurn, &sp.ToolName, &sp.ToolCallID,
			&sp.RequestModel, &sp.ResponseModel, &sp.Provider, &sp.OpName, &sp.DisplayName, &started, &ended,
			&exc, &summary, &rollup, &cum, &sp.UserID, &sp.RunID, &sp.ParentCallID, &sp.ParentCallTraceID}
		if f.WithAttrs {
			dest = append(dest, &sp.attrsJSON)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		sp.StartedUS = started.Int64
		sp.EndedUS, sp.Ended = ended.Int64, ended.Valid
		sp.Exception = strPtr(exc)
		ru := ParseContribution(rollup)
		u := TotalContribution(decodeMap(summary), ended.Valid, exc.Valid, cum, ru)
		u.Sub(ru) // own usage only, so nested spans are not counted twice
		single := ""
		for model, m := range u.Usage {
			p, c, _ := UsageTokens(m)
			p, c = math.Max(p, 0), math.Max(c, 0)
			sp.InTok += p
			sp.OutTok += c
			sp.ReasonTok += math.Max(m["reasoning_tokens"], 0)
			sp.CacheRead += math.Max(m["cache_read_input_tokens"], 0)
			sp.CacheW += math.Max(m["cache_creation_input_tokens"], 0)
			if cost, _, ok := pb.ModelCost(model, m, sp.StartedUS); ok {
				sp.InCost += cost.Input()
				sp.OutCost += cost.Completion
				sp.CacheReadCost += cost.CacheRead
				sp.CacheWCost += cost.CacheCreation
				sp.HasCost = true
			}
			if p+c > 0 {
				single = model
			}
		}
		if sp.RequestModel == "" && len(u.Usage) == 1 && single != "unknown" {
			sp.RequestModel = single
		}
		out = append(out, sp)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	markInvocations(out)
	return out, nil
}

// markInvocations flags agent invocations: invoke_agent spans, and the
// top-most span of each agent run when an agent never emits invoke_agent.
func markInvocations(spans []*agentSpan) {
	byID := make(map[string]*agentSpan, len(spans))
	for _, sp := range spans {
		byID[sp.ID] = sp
	}
	for _, sp := range spans {
		if sp.AgentName == "" {
			continue
		}
		if sp.Op == OpInvokeAgent && sp.OwnAgent {
			sp.IsInvocation = true
			continue
		}
		p := byID[sp.ParentID]
		sp.IsInvocation = sp.OwnAgent && (p == nil || p.AgentName != sp.AgentName) &&
			(sp.Op != OpExecuteTool && sp.Op != OpChat || p == nil)
	}
}

func (sp *agentSpan) durationMS() (float64, bool) {
	if !sp.Ended {
		return 0, false
	}
	return float64(sp.EndedUS-sp.StartedUS) / 1000, true
}

func (sp *agentSpan) statusCode() string {
	switch {
	case sp.Exception != nil:
		return "ERROR"
	case sp.Ended:
		return "OK"
	}
	return "UNSET"
}

func (sp *agentSpan) errorType() string {
	if sp.Exception == nil {
		return ""
	}
	first := strings.SplitN(*sp.Exception, "\n", 2)[0]
	if i := strings.Index(first, ": "); i > 0 && i < 80 && !strings.Contains(first[:i], " ") {
		return first[:i]
	}
	return ""
}

func (sp *agentSpan) displayName() string {
	if sp.DisplayName != "" {
		return sp.DisplayName
	}
	return sp.SpanName
}

// ---------------------------------------------------------------- fields

// agentFieldAliases maps semantic names onto span columns.
var agentFieldAliases = map[string]string{
	"id": "span_id", "parent_id": "parent_span_id", "weft.parent_call.id": "parent_call_id", "weft.parent_call.trace_id": "parent_call_trace_id",
	"gen_ai.operation.name": "operation_name", "operation.name": "operation_name", "op": "operation_name", "name": "span_name",
	"agent.name": "agent_name", "gen_ai.agent.name": "agent_name", "agent.id": "agent_id", "gen_ai.agent.id": "agent_id",
	"agent.version": "agent_version", "gen_ai.agent.version": "agent_version", "agent.description": "agent_description",
	"conversation.id": "conversation_id", "gen_ai.conversation.id": "conversation_id", "thread_id": "conversation_id",
	"conversation.name": "conversation_name", "gen_ai.conversation.name": "conversation_name",
	"tool.name": "tool_name", "gen_ai.tool.name": "tool_name", "tool.call.id": "tool_call_id", "gen_ai.tool.call.id": "tool_call_id",
	"request.model": "request_model", "model": "request_model", "gen_ai.request.model": "request_model",
	"response.model": "response_model", "gen_ai.response.model": "response_model",
	"provider.name": "provider_name", "gen_ai.provider.name": "provider_name", "provider": "provider_name",
	"status": "status_code", "exception": "status_message", "latency_ms": "duration_ms",
	"usage.input_tokens": "input_tokens", "usage.output_tokens": "output_tokens", "cost": "total_cost_usd",
}

var agentNumericFields = map[string]bool{"duration_ms": true, "input_tokens": true, "output_tokens": true, "total_tokens": true,
	"reasoning_tokens": true, "cache_read_input_tokens": true, "cache_creation_input_tokens": true,
	"input_cost_usd": true, "output_cost_usd": true, "total_cost_usd": true, "cache_read_cost_usd": true, "cache_creation_cost_usd": true}

var agentStringFields = map[string]bool{"span_id": true, "trace_id": true, "parent_span_id": true, "operation_name": true,
	"span_name": true, "display_name": true, "agent_name": true, "agent_id": true, "agent_version": true, "agent_description": true,
	"conversation_id": true, "conversation_name": true, "tool_name": true, "tool_call_id": true, "request_model": true,
	"response_model": true, "provider_name": true, "status_code": true, "status_message": true, "error_type": true,
	"user_id": true, "run_id": true, "started_at": true, "ended_at": true, "parent_call_id": true, "parent_call_trace_id": true}

var agentBoolFields = map[string]bool{"is_turn": true, "is_error": true, "is_invocation": true, "is_agent": true, "is_llm": true, "is_tool": true}

func canonicalAgentField(key string) string {
	if a, ok := agentFieldAliases[key]; ok {
		return a
	}
	return key
}

func validAgentField(key string) bool {
	k := canonicalAgentField(key)
	if agentNumericFields[k] || agentStringFields[k] || agentBoolFields[k] {
		return true
	}
	for _, p := range []string{"custom_attrs_string.", "custom_attrs_int.", "custom_attrs_float.", "custom_attrs_bool.", "custom_attrs.", "attributes."} {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

func optStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// field returns a span value for queries, grouping and sorting (nil when absent).
func (sp *agentSpan) field(key string) any {
	k := canonicalAgentField(key)
	switch k {
	case "span_id":
		return sp.ID
	case "trace_id":
		return sp.TraceID
	case "parent_span_id":
		return optStr(sp.ParentID)
	case "parent_call_id":
		return optStr(sp.ParentCallID)
	case "parent_call_trace_id":
		return optStr(sp.ParentCallTraceID)
	case "operation_name":
		return optStr(sp.Op)
	case "span_name":
		return sp.SpanName
	case "display_name":
		return sp.displayName()
	case "agent_name":
		return optStr(sp.AgentName)
	case "agent_id":
		return optStr(sp.AgentID)
	case "agent_version":
		return optStr(sp.AgentVersion)
	case "agent_description":
		return optStr(sp.AgentDesc)
	case "conversation_id":
		return optStr(sp.ConvID)
	case "conversation_name":
		return optStr(sp.ConvName)
	case "tool_name":
		return optStr(sp.ToolName)
	case "tool_call_id":
		return optStr(sp.ToolCallID)
	case "request_model":
		return optStr(sp.RequestModel)
	case "response_model":
		return optStr(sp.ResponseModel)
	case "provider_name":
		return optStr(sp.Provider)
	case "status_code":
		return sp.statusCode()
	case "status_message":
		if sp.Exception == nil {
			return nil
		}
		return *sp.Exception
	case "error_type":
		return optStr(sp.errorType())
	case "started_at":
		return FormatMicros(sp.StartedUS)
	case "ended_at":
		if !sp.Ended {
			return nil
		}
		return FormatMicros(sp.EndedUS)
	case "duration_ms":
		if d, ok := sp.durationMS(); ok {
			return d
		}
		return nil
	case "input_tokens":
		return sp.InTok
	case "output_tokens":
		return sp.OutTok
	case "total_tokens":
		return sp.InTok + sp.OutTok
	case "reasoning_tokens":
		return sp.ReasonTok
	case "cache_read_input_tokens":
		return sp.CacheRead
	case "cache_creation_input_tokens":
		return sp.CacheW
	case "input_cost_usd", "output_cost_usd", "total_cost_usd", "cache_read_cost_usd", "cache_creation_cost_usd":
		if !sp.HasCost {
			return nil
		}
		switch k {
		case "input_cost_usd":
			return sp.InCost
		case "output_cost_usd":
			return sp.OutCost
		case "cache_read_cost_usd":
			return sp.CacheReadCost
		case "cache_creation_cost_usd":
			return sp.CacheWCost
		}
		return sp.InCost + sp.OutCost
	case "is_turn":
		return sp.IsTurn
	case "is_error":
		return sp.Exception != nil
	case "is_invocation":
		return sp.IsInvocation
	case "is_agent":
		return sp.Op == OpInvokeAgent
	case "is_llm":
		return sp.Op == OpChat
	case "is_tool":
		return sp.Op == OpExecuteTool
	case "user_id":
		return optStr(sp.UserID)
	case "run_id":
		return optStr(sp.RunID)
	}
	for _, src := range []string{"custom_attrs_string", "custom_attrs_int", "custom_attrs_float", "custom_attrs_bool"} {
		if strings.HasPrefix(key, src+".") {
			return sp.customTyped(src, strings.TrimPrefix(key, src+"."))
		}
	}
	for _, p := range []string{"custom_attrs.", "attributes."} {
		if strings.HasPrefix(key, p) {
			return sp.customAttrs()[strings.TrimPrefix(key, p)]
		}
	}
	return nil
}

// AgentValueRef points at a span field or a typed custom attribute.
type AgentValueRef struct {
	Key    string `json:"key"`
	Source string `json:"source"`
	Alias  string `json:"alias,omitempty"`
}

func (r AgentValueRef) name() string {
	if r.Alias != "" {
		return r.Alias
	}
	return r.Key
}

func (r AgentValueRef) validate() error {
	switch r.Source {
	case "", "field", "column", "derived":
		if !validAgentField(r.Key) {
			return Invalidf("unknown span field %q", r.Key)
		}
	case "custom_attrs_string", "custom_attrs_int", "custom_attrs_float", "custom_attrs_bool":
		if r.Key == "" {
			return Invalidf("custom attribute key is required")
		}
	default:
		return Invalidf("unknown value source %q", r.Source)
	}
	return nil
}

func (sp *agentSpan) ref(r AgentValueRef) any {
	switch r.Source {
	case "custom_attrs_string", "custom_attrs_int", "custom_attrs_float", "custom_attrs_bool":
		return sp.customTyped(r.Source, r.Key)
	}
	return sp.field(r.Key)
}

// ---------------------------------------------------------------- custom attributes

var reservedAttrPrefixes = []string{"gen_ai.", "weft.", "llm.", "input.", "output.", "openinference.",
	"traceloop.", "ai.", "mlflow.", "gcp.vertex.agent.", "pydantic_ai.", "logfire.", "otel.", "telemetry.", "session.",
	"tool.", "tool_call.", "message.", "embedding.", "retrieval.", "reranker.", "document.", "exception.", "code.", "thread.",
	"agent.", "graph.", "operation.", "resource.", "user.id", "enduser.id", "gen_ai"}

var reservedAttrKeys = map[string]bool{"otel": true, "weft": true,
	"agent_name": true, "model_name": true, "all_messages_events": true, "final_result": true, "tool_arguments": true,
	"tool_response": true, "model_request_parameters": true, "logfire.msg": true, "logfire.json_schema": true}

func reservedAttr(k string) bool {
	if reservedAttrKeys[k] {
		return true
	}
	for _, p := range reservedAttrPrefixes {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

func typedAttr(v any) any {
	switch t := v.(type) {
	case string, bool:
		return t
	case json.Number:
		s := t.String()
		if !strings.ContainsAny(s, ".eE") {
			if i, err := strconv.ParseInt(s, 10, 64); err == nil {
				return i
			}
		}
		f, _ := t.Float64()
		return f
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1<<53 {
			return int64(t)
		}
		return t
	case nil:
		return nil
	}
	return jsonString(v)
}

func flattenAttrs(prefix string, v any, out map[string]any, depth int) {
	if m, ok := v.(map[string]any); ok && depth < 4 {
		for k, x := range m {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			flattenAttrs(key, x, out, depth+1)
		}
		return
	}
	if prefix == "" || reservedAttr(prefix) {
		return
	}
	if t := typedAttr(v); t != nil {
		out[prefix] = t
	}
}

// customAttrs are user-defined attributes: OTel span attributes outside the
// reserved conventions, plus the call's own attributes.
func (sp *agentSpan) customAttrs() map[string]any {
	if sp.custom != nil {
		return sp.custom
	}
	sp.custom = map[string]any{}
	attrs := decodeMap(sp.attrsJSON)
	for k, v := range attrs {
		if reservedAttrKeys[k] {
			continue
		}
		flattenAttrs(k, v, sp.custom, 0)
	}
	if o := asMap(attrs["otel"]); o != nil {
		for k, v := range asMap(o["attributes"]) {
			if k == "metadata" { // OpenInference: JSON object of user metadata
				if s, ok := v.(string); ok {
					var m map[string]any
					if d := json.NewDecoder(strings.NewReader(s)); d != nil {
						d.UseNumber()
						if d.Decode(&m) == nil {
							flattenAttrs("metadata", m, sp.custom, 0)
							continue
						}
					}
				}
			}
			if reservedAttr(k) {
				continue
			}
			if t := typedAttr(v); t != nil {
				sp.custom[k] = t
			}
		}
	}
	return sp.custom
}

func attrType(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case bool:
		return "bool"
	case int64:
		return "int"
	case float64:
		return "float"
	}
	return ""
}

func (sp *agentSpan) customTyped(source, key string) any {
	v, ok := sp.customAttrs()[key]
	if !ok {
		return nil
	}
	if "custom_attrs_"+attrType(v) == source {
		return v
	}
	if source == "custom_attrs_float" { // ints are valid floats
		if i, ok := v.(int64); ok {
			return float64(i)
		}
	}
	return nil
}

func (sp *agentSpan) customByType() (s map[string]string, i map[string]int64, f map[string]float64, b map[string]bool) {
	s, i, f, b = map[string]string{}, map[string]int64{}, map[string]float64{}, map[string]bool{}
	for k, v := range sp.customAttrs() {
		switch t := v.(type) {
		case string:
			s[k] = t
		case int64:
			i[k] = t
		case float64:
			f[k] = t
		case bool:
			b[k] = t
		}
	}
	return
}

// ---------------------------------------------------------------- expressions

// queryNeedsAttrs reports whether an expression reads custom attributes.
func queryNeedsAttrs(q any) bool {
	b, _ := json.Marshal(q)
	s := string(b)
	return strings.Contains(s, "custom_attrs") || strings.Contains(s, `"attributes.`)
}

func exprOf(query any) any {
	if m := asMap(query); m != nil {
		if e, ok := m["$expr"]; ok {
			return e
		}
	}
	return query
}

func matchAgentQuery(query any, sp *agentSpan) (bool, error) {
	e := exprOf(query)
	if e == nil {
		return true, nil
	}
	v, err := evalAgentExpr(e, sp)
	if err != nil {
		return false, err
	}
	return truthy(v), nil
}

func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	}
	if f, ok := toFloat(v); ok {
		return f != 0
	}
	return true
}

func evalAgentExpr(e any, sp *agentSpan) (any, error) {
	m := asMap(e)
	if m == nil {
		return e, nil // bare literal
	}
	if len(m) != 1 {
		return nil, Invalidf("expression must have exactly one operator")
	}
	for op, arg := range m {
		list, _ := arg.([]any)
		switch op {
		case "$literal":
			return arg, nil
		case "$getField":
			k, _ := arg.(string)
			return sp.field(k), nil
		case "$and", "$or":
			for _, x := range list {
				v, err := evalAgentExpr(x, sp)
				if err != nil {
					return nil, err
				}
				if op == "$and" && !truthy(v) {
					return false, nil
				}
				if op == "$or" && truthy(v) {
					return true, nil
				}
			}
			return op == "$and", nil
		case "$not":
			if len(list) != 1 {
				return nil, Invalidf("$not takes one operand")
			}
			v, err := evalAgentExpr(list[0], sp)
			return !truthy(v), err
		case "$eq", "$gt", "$gte", "$lt", "$lte":
			if len(list) != 2 {
				return nil, Invalidf("%s takes two operands", op)
			}
			a, err := evalAgentExpr(list[0], sp)
			if err != nil {
				return nil, err
			}
			b, err := evalAgentExpr(list[1], sp)
			if err != nil {
				return nil, err
			}
			if op == "$eq" {
				return looseEqual(a, b), nil
			}
			if a == nil || b == nil {
				return false, nil
			}
			c := compareValues(a, b)
			switch op {
			case "$gt":
				return c > 0, nil
			case "$gte":
				return c >= 0, nil
			case "$lt":
				return c < 0, nil
			}
			return c <= 0, nil
		case "$in":
			if len(list) != 2 {
				return nil, Invalidf("$in takes two operands")
			}
			a, err := evalAgentExpr(list[0], sp)
			if err != nil {
				return nil, err
			}
			var cands []any
			if l, ok := list[1].([]any); ok {
				cands = l
			} else {
				v, err := evalAgentExpr(list[1], sp)
				if err != nil {
					return nil, err
				}
				cands, _ = v.([]any)
			}
			for _, c := range cands {
				cv, err := evalAgentExpr(c, sp)
				if err != nil {
					return nil, err
				}
				if looseEqual(a, cv) {
					return true, nil
				}
			}
			return false, nil
		case "$contains":
			cm := asMap(arg)
			in, err := evalAgentExpr(cm["input"], sp)
			if err != nil {
				return nil, err
			}
			sub, err := evalAgentExpr(cm["substr"], sp)
			if err != nil {
				return nil, err
			}
			hs, ns := fmt.Sprint(in), fmt.Sprint(sub)
			if in == nil {
				return false, nil
			}
			if ci, _ := cm["case_insensitive"].(bool); ci {
				hs, ns = strings.ToLower(hs), strings.ToLower(ns)
			}
			return strings.Contains(hs, ns), nil
		case "$convert":
			cm := asMap(arg)
			v, err := evalAgentExpr(cm["input"], sp)
			if err != nil {
				return nil, err
			}
			switch cm["to"] {
			case "double", "int":
				f, ok := toFloat(v)
				if !ok {
					if s, isStr := v.(string); isStr {
						f, err = strconv.ParseFloat(s, 64)
						ok = err == nil
					}
				}
				if !ok {
					return nil, nil
				}
				if cm["to"] == "int" {
					return math.Trunc(f), nil
				}
				return f, nil
			case "string":
				if v == nil {
					return nil, nil
				}
				return fmt.Sprint(v), nil
			case "bool":
				return truthy(v), nil
			case "exists":
				return v != nil, nil
			}
			return v, nil
		default:
			return nil, Invalidf("unsupported operator %s", op)
		}
	}
	return nil, nil
}

func looseEqual(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return compareValues(a, b) == 0
}

// compareValues orders numbers numerically, timestamps chronologically and
// everything else as strings.
func compareValues(a, b any) int {
	fa, oka := toFloat(a)
	fb, okb := toFloat(b)
	if oka && okb {
		switch {
		case fa < fb:
			return -1
		case fa > fb:
			return 1
		}
		return 0
	}
	sa, sb := fmt.Sprint(a), fmt.Sprint(b)
	if len(sa) >= 10 && len(sb) >= 10 && sa[4] == '-' && sb[4] == '-' {
		if ta, err := ParseTime(sa); err == nil {
			if tb, err := ParseTime(sb); err == nil {
				switch {
				case ta < tb:
					return -1
				case ta > tb:
					return 1
				}
				return 0
			}
		}
	}
	return strings.Compare(sa, sb)
}

// sortAgentValues compares with nils last.
func sortLess(a, b any, desc bool) (less bool, equal bool) {
	if a == nil || b == nil {
		if a == nil && b == nil {
			return false, true
		}
		return b == nil, false // nils last in both directions
	}
	c := compareValues(a, b)
	if c == 0 {
		return false, true
	}
	if desc {
		return c > 0, false
	}
	return c < 0, false
}

// ---------------------------------------------------------------- aggregation

type spanAgg struct {
	spans, errors, invocations, invErrors int64
	turns                                 int64
	first, last                           int64
	inTok, outTok, reason, cacheR, cacheW float64
	inCost, outCost                       float64
	hasCost                               bool
	topDur                                float64
	topDurs                               []float64
	members                               []*agentSpan
	convs, convNames, agents, versions    map[string]bool
	providers, models                     map[string]bool
}

func newSpanAgg() *spanAgg {
	return &spanAgg{convs: map[string]bool{}, convNames: map[string]bool{}, agents: map[string]bool{}, versions: map[string]bool{},
		providers: map[string]bool{}, models: map[string]bool{}}
}

func (g *spanAgg) add(sp *agentSpan) {
	g.members = append(g.members, sp)
	g.spans++
	if sp.Exception != nil {
		g.errors++
	}
	if sp.IsTurn {
		g.turns++
	}
	if sp.IsInvocation {
		g.invocations++
		if sp.Exception != nil {
			g.invErrors++
		}
	}
	if g.first == 0 || sp.StartedUS < g.first {
		g.first = sp.StartedUS
	}
	end := sp.StartedUS
	if sp.Ended {
		end = sp.EndedUS
	}
	if end > g.last {
		g.last = end
	}
	g.inTok += sp.InTok
	g.outTok += sp.OutTok
	g.reason += sp.ReasonTok
	g.cacheR += sp.CacheRead
	g.cacheW += sp.CacheW
	if sp.HasCost {
		g.hasCost = true
		g.inCost += sp.InCost
		g.outCost += sp.OutCost
	}
	for set, v := range map[*map[string]bool]string{&g.convs: sp.ConvID, &g.convNames: sp.ConvName, &g.agents: sp.AgentName,
		&g.versions: sp.AgentVersion, &g.providers: sp.Provider, &g.models: sp.RequestModel} {
		if v != "" {
			(*set)[v] = true
		}
	}
}

// finish computes durations of the group's top-level spans (those whose
// parent is not in the group), so nested spans are not double counted.
func (g *spanAgg) finish() {
	ids := make(map[string]bool, len(g.members))
	for _, sp := range g.members {
		ids[sp.ID] = true
	}
	for _, sp := range g.members {
		if ids[sp.ParentID] {
			continue
		}
		if d, ok := sp.durationMS(); ok {
			g.topDur += d
			g.topDurs = append(g.topDurs, d)
		}
	}
	sort.Float64s(g.topDurs)
}

func (g *spanAgg) cost() *float64 {
	if !g.hasCost {
		return nil
	}
	v := g.inCost + g.outCost
	return &v
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func microsPtr(us int64) *string {
	if us == 0 {
		return nil
	}
	s := FormatMicros(us)
	return &s
}

func pctPtr(sorted []float64, p float64) *float64 {
	v := percentile(sorted, p)
	if v == nil {
		return nil
	}
	r := math.Round(*v*1000) / 1000
	return &r
}

// ---------------------------------------------------------------- /agents/query

// AgentSortBy is a sort key for agent endpoints.
type AgentSortBy = SortBy

// AgentsQueryReq is the body of /agents/query.
type AgentsQueryReq struct {
	ProjectID string `json:"project_id"`
	Filters   *struct {
		AgentName *string `json:"agent_name"`
	} `json:"filters"`
	IncludeCosts bool     `json:"include_costs"`
	Limit        *int     `json:"limit"`
	Offset       int      `json:"offset"`
	SortBy       []SortBy `json:"sort_by"`
	// Weft extensions: restrict to a time window.
	StartedAfter  *string `json:"started_after,omitempty"`
	StartedBefore *string `json:"started_before,omitempty"`
}

// AgentStats is one row of /agents/query (an agent) or, with
// AgentVersion set, of /agents/agent-versions/query.
type AgentStats struct {
	AgentName         string   `json:"agent_name"`
	AgentVersion      *string  `json:"agent_version,omitempty"`
	ProjectID         string   `json:"project_id"`
	ErrorCount        int64    `json:"error_count"`
	FirstSeen         *string  `json:"first_seen"`
	LastSeen          *string  `json:"last_seen"`
	InvocationCount   int64    `json:"invocation_count"`
	SpanCount         int64    `json:"span_count"`
	TotalCostUSD      *float64 `json:"total_cost_usd"`
	TotalDurationMS   int64    `json:"total_duration_ms"`
	TotalInputTokens  int64    `json:"total_input_tokens"`
	TotalOutputTokens int64    `json:"total_output_tokens"`
	// Weft extensions.
	VersionCount         int      `json:"version_count"`
	Versions             []string `json:"versions"`
	ConversationCount    int      `json:"conversation_count"`
	InvocationErrorCount int64    `json:"invocation_error_count"`
	LatencyMSP50         *float64 `json:"latency_ms_p50"`
	LatencyMSP95         *float64 `json:"latency_ms_p95"`
	LLMCallCount         int64    `json:"llm_call_count"`
	ToolCallCount        int64    `json:"tool_call_count"`
	Models               []string `json:"models"`
}

func timeRange(after, before *string) (int64, int64, error) {
	var a, b int64
	var err error
	if after != nil && *after != "" {
		if a, err = ParseTime(*after); err != nil {
			return 0, 0, err
		}
	}
	if before != nil && *before != "" {
		if b, err = ParseTime(*before); err != nil {
			return 0, 0, err
		}
	}
	return a, b, nil
}

func agentStatsFrom(projectID, name string, g *spanAgg, includeCosts bool) *AgentStats {
	g.finish()
	var llm, tools int64
	invDurs := []float64{}
	for _, sp := range g.members {
		switch sp.Op {
		case OpChat:
			llm++
		case OpExecuteTool:
			tools++
		}
		if sp.IsInvocation {
			if d, ok := sp.durationMS(); ok {
				invDurs = append(invDurs, d)
			}
		}
	}
	sort.Float64s(invDurs)
	lat := invDurs
	if len(lat) == 0 {
		lat = g.topDurs
	}
	var invDur float64
	for _, d := range invDurs {
		invDur += d
	}
	if len(invDurs) == 0 {
		invDur = g.topDur
	}
	a := &AgentStats{AgentName: name, ProjectID: projectID, ErrorCount: g.errors, FirstSeen: microsPtr(g.first), LastSeen: microsPtr(g.last),
		InvocationCount: g.invocations, SpanCount: g.spans, TotalDurationMS: int64(math.Round(invDur)),
		TotalInputTokens: int64(g.inTok), TotalOutputTokens: int64(g.outTok), Versions: sortedKeys(g.versions),
		ConversationCount: len(g.convs), InvocationErrorCount: g.invErrors, LatencyMSP50: pctPtr(lat, 0.5), LatencyMSP95: pctPtr(lat, 0.95),
		LLMCallCount: llm, ToolCallCount: tools, Models: sortedKeys(g.models)}
	a.VersionCount = len(a.Versions)
	if includeCosts {
		a.TotalCostUSD = g.cost()
	}
	return a
}

func agentStatsField(a *AgentStats, field string) any {
	switch field {
	case "agent_name":
		return a.AgentName
	case "agent_version":
		if a.AgentVersion == nil {
			return nil
		}
		return *a.AgentVersion
	case "error_count":
		return a.ErrorCount
	case "first_seen":
		return derefStr(a.FirstSeen)
	case "last_seen":
		return derefStr(a.LastSeen)
	case "invocation_count":
		return a.InvocationCount
	case "span_count":
		return a.SpanCount
	case "total_cost_usd":
		if a.TotalCostUSD == nil {
			return nil
		}
		return *a.TotalCostUSD
	case "total_duration_ms":
		return a.TotalDurationMS
	case "total_input_tokens":
		return a.TotalInputTokens
	case "total_output_tokens":
		return a.TotalOutputTokens
	case "conversation_count":
		return int64(a.ConversationCount)
	case "version_count":
		return int64(a.VersionCount)
	case "latency_ms_p50":
		return derefF(a.LatencyMSP50)
	case "latency_ms_p95":
		return derefF(a.LatencyMSP95)
	}
	return nil
}

func derefStr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func derefF(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

var agentSortFields = map[string]bool{"agent_name": true, "agent_version": true, "error_count": true, "first_seen": true, "last_seen": true,
	"invocation_count": true, "span_count": true, "total_cost_usd": true, "total_duration_ms": true, "total_input_tokens": true,
	"total_output_tokens": true, "conversation_count": true, "version_count": true, "latency_ms_p50": true, "latency_ms_p95": true}

func sortAgentStats(rows []*AgentStats, sorts []SortBy) error {
	if len(sorts) == 0 {
		sorts = []SortBy{{Field: "last_seen", Direction: "desc"}}
	}
	for _, s := range sorts {
		if !agentSortFields[s.Field] {
			return Invalidf("cannot sort agents by %q", s.Field)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		for _, s := range sorts {
			less, eq := sortLess(agentStatsField(rows[i], s.Field), agentStatsField(rows[j], s.Field), s.Direction == "desc")
			if !eq {
				return less
			}
		}
		return false
	})
	return nil
}

func page[T any](rows []T, limit *int, offset, def int) []T {
	n := def
	if limit != nil && *limit > 0 {
		n = *limit
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(rows) {
		return []T{}
	}
	end := offset + n
	if end > len(rows) {
		end = len(rows)
	}
	return rows[offset:end]
}

// QueryAgents aggregates spans per agent name.
func (s *Store) QueryAgents(ctx context.Context, req AgentsQueryReq) ([]*AgentStats, int, error) {
	pid, err := refs.NormalizeProject(req.ProjectID)
	if err != nil {
		return nil, 0, &InvalidError{Msg: err.Error()}
	}
	after, before, err := timeRange(req.StartedAfter, req.StartedBefore)
	if err != nil {
		return nil, 0, err
	}
	spans, err := s.loadAgentSpans(ctx, pid, agentSpanFilter{After: after, Before: before})
	if err != nil {
		return nil, 0, err
	}
	name := ""
	if req.Filters != nil && req.Filters.AgentName != nil {
		name = strings.ToLower(strings.TrimSpace(*req.Filters.AgentName))
	}
	groups := map[string]*spanAgg{}
	for _, sp := range spans {
		if sp.AgentName == "" || (name != "" && !strings.Contains(strings.ToLower(sp.AgentName), name)) {
			continue
		}
		g := groups[sp.AgentName]
		if g == nil {
			g = newSpanAgg()
			groups[sp.AgentName] = g
		}
		g.add(sp)
	}
	rows := make([]*AgentStats, 0, len(groups))
	for n, g := range groups {
		rows = append(rows, agentStatsFrom(pid, n, g, req.IncludeCosts))
	}
	if err := sortAgentStats(rows, req.SortBy); err != nil {
		return nil, 0, err
	}
	return page(rows, req.Limit, req.Offset, 100), len(rows), nil
}

// AgentVersionsQueryReq is the body of /agents/agent-versions/query.
type AgentVersionsQueryReq struct {
	ProjectID    string   `json:"project_id"`
	AgentName    string   `json:"agent_name"`
	IncludeCosts bool     `json:"include_costs"`
	Limit        *int     `json:"limit"`
	Offset       int      `json:"offset"`
	SortBy       []SortBy `json:"sort_by"`
}

// QueryAgentVersions aggregates one agent's spans per agent_version. Spans
// without a version are reported under the empty version "".
func (s *Store) QueryAgentVersions(ctx context.Context, req AgentVersionsQueryReq) ([]*AgentStats, int, error) {
	pid, err := refs.NormalizeProject(req.ProjectID)
	if err != nil {
		return nil, 0, &InvalidError{Msg: err.Error()}
	}
	if req.AgentName == "" {
		return nil, 0, Invalidf("agent_name is required")
	}
	spans, err := s.loadAgentSpans(ctx, pid, agentSpanFilter{AgentName: req.AgentName})
	if err != nil {
		return nil, 0, err
	}
	groups := map[string]*spanAgg{}
	for _, sp := range spans {
		g := groups[sp.AgentVersion]
		if g == nil {
			g = newSpanAgg()
			groups[sp.AgentVersion] = g
		}
		g.add(sp)
	}
	rows := make([]*AgentStats, 0, len(groups))
	for v, g := range groups {
		a := agentStatsFrom(pid, req.AgentName, g, req.IncludeCosts)
		ver := v
		a.AgentVersion = &ver
		a.Versions, a.VersionCount = nil, 0
		rows = append(rows, a)
	}
	if err := sortAgentStats(rows, req.SortBy); err != nil {
		return nil, 0, err
	}
	return page(rows, req.Limit, req.Offset, 100), len(rows), nil
}

// ---------------------------------------------------------------- /agents/spans/query

// AgentMeasureSpec is one aggregate computed over the spans of a group.
type AgentMeasureSpec struct {
	Aggregation string         `json:"aggregation"`
	Alias       string         `json:"alias"`
	Filter      any            `json:"filter"`
	Value       *AgentValueRef `json:"value"`
	ValueType   string         `json:"value_type"`
}

// AgentGroupFilter keeps groups whose measure lies in [min, max].
type AgentGroupFilter struct {
	Measure AgentMeasureSpec `json:"measure"`
	GroupBy []AgentValueRef  `json:"group_by"`
	Min     any              `json:"min"`
	Max     any              `json:"max"`
}

// AgentDistributionSpec asks for a per-group distribution of a custom attribute.
type AgentDistributionSpec struct {
	Alias string        `json:"alias"`
	Value AgentValueRef `json:"value"`
	Bins  int           `json:"bins"`
	TopN  int           `json:"top_n"`
}

// AgentSpansQueryReq is the body of /agents/spans/query.
type AgentSpansQueryReq struct {
	ProjectID string `json:"project_id"`
	AgentEvalFilter
	CustomAttrColumns  []AgentValueRef         `json:"custom_attr_columns"`
	GroupBy            []AgentValueRef         `json:"group_by"`
	GroupDistributions []AgentDistributionSpec `json:"group_distributions"`
	GroupFilters       []AgentGroupFilter      `json:"group_filters"`
	IncludeCosts       bool                    `json:"include_costs"`
	IncludeDetails     bool                    `json:"include_details"`
	InsightFilters     []AgentInsightFilter    `json:"insight_filters"`
	Limit              *int                    `json:"limit"`
	Measures           []AgentMeasureSpec      `json:"measures"`
	Offset             int                     `json:"offset"`
	Query              any                     `json:"query"`
	SignalFilters      *AgentSignalFilters     `json:"signal_filters"`
	SortBy             []SortBy                `json:"sort_by"`
	StartedAfter       *string                 `json:"started_after"`
	StartedBefore      *string                 `json:"started_before"`
}

// SpanMessage is one input or output message of a span.
type SpanMessage struct {
	Role         string `json:"role"`
	Content      string `json:"content"`
	FinishReason string `json:"finish_reason"`
}

// AgentSpan is one span of /agents/spans/query.
type AgentSpan struct {
	AgentDescription         *string            `json:"agent_description"`
	AgentID                  *string            `json:"agent_id"`
	AgentName                *string            `json:"agent_name"`
	AgentVersion             *string            `json:"agent_version"`
	ArtifactRefs             []string           `json:"artifact_refs"`
	CacheCreationInputTokens *int64             `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int64             `json:"cache_read_input_tokens"`
	CacheCreationCostUSD     *float64           `json:"cache_creation_cost_usd"` // part of input_cost_usd
	CacheReadCostUSD         *float64           `json:"cache_read_cost_usd"`     // part of input_cost_usd
	ContentRefs              []string           `json:"content_refs"`
	ConversationID           *string            `json:"conversation_id"`
	ConversationName         *string            `json:"conversation_name"`
	CustomAttrsBool          map[string]bool    `json:"custom_attrs_bool"`
	CustomAttrsFloat         map[string]float64 `json:"custom_attrs_float"`
	CustomAttrsInt           map[string]int64   `json:"custom_attrs_int"`
	CustomAttrsString        map[string]string  `json:"custom_attrs_string"`
	EndedAt                  *string            `json:"ended_at"`
	ErrorType                *string            `json:"error_type"`
	FinishReasons            []string           `json:"finish_reasons"`
	InputCostUSD             *float64           `json:"input_cost_usd"`
	InputMessages            []SpanMessage      `json:"input_messages"`
	InputTokens              *int64             `json:"input_tokens"`
	ObjectRefs               []string           `json:"object_refs"`
	OperationName            *string            `json:"operation_name"`
	OutputCostUSD            *float64           `json:"output_cost_usd"`
	OutputMessages           []SpanMessage      `json:"output_messages"`
	OutputTokens             *int64             `json:"output_tokens"`
	ParentCallID             *string            `json:"parent_call_id"`
	ParentCallTraceID        *string            `json:"parent_call_trace_id"`
	ParentSpanID             *string            `json:"parent_span_id"`
	ProjectID                string             `json:"project_id"`
	ProviderName             *string            `json:"provider_name"`
	ReasoningContent         *string            `json:"reasoning_content"`
	ReasoningTokens          *int64             `json:"reasoning_tokens"`
	RequestModel             *string            `json:"request_model"`
	RequestTemperature       *float64           `json:"request_temperature"`
	RequestMaxTokens         *int64             `json:"request_max_tokens"`
	RequestStopSequences     []string           `json:"request_stop_sequences"`
	ResponseModel            *string            `json:"response_model"`
	SpanID                   string             `json:"span_id"`
	SpanKind                 *string            `json:"span_kind"`
	SpanName                 *string            `json:"span_name"`
	StartedAt                *string            `json:"started_at"`
	StatusCode               string             `json:"status_code"`
	StatusMessage            *string            `json:"status_message"`
	SystemInstructions       []string           `json:"system_instructions"`
	ToolCallArguments        *string            `json:"tool_call_arguments"`
	ToolCallID               *string            `json:"tool_call_id"`
	ToolCallResult           *string            `json:"tool_call_result"`
	ToolDefinitions          *string            `json:"tool_definitions"`
	ToolDescription          *string            `json:"tool_description"`
	ToolName                 *string            `json:"tool_name"`
	ToolType                 *string            `json:"tool_type"`
	TotalCostUSD             *float64           `json:"total_cost_usd"`
	TraceID                  string             `json:"trace_id"`
	RunID                    *string            `json:"run_id"`
	UserID                   *string            `json:"user_id"`
	// Weft extensions.
	DurationMS  *float64 `json:"duration_ms"`
	IsTurn      bool     `json:"is_turn"`
	DisplayName string   `json:"display_name"`
}

// AgentMessagePreview is a first/last message snippet of a group.
type AgentMessagePreview struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// AgentSpanGroup is one grouped row of /agents/spans/query.
type AgentSpanGroup struct {
	AgentNames                    []string                      `json:"agent_names"`
	AgentVersions                 []string                      `json:"agent_versions"`
	ConversationCount             int                           `json:"conversation_count"`
	ConversationNames             []string                      `json:"conversation_names"`
	Distributions                 map[string]*AgentDistribution `json:"distributions"`
	ErrorCount                    int64                         `json:"error_count"`
	FirstMessage                  *AgentMessagePreview          `json:"first_message"`
	FirstSeen                     *string                       `json:"first_seen"`
	GroupKeys                     map[string]any                `json:"group_keys"`
	InvocationCount               int64                         `json:"invocation_count"`
	LastMessage                   *AgentMessagePreview          `json:"last_message"`
	LastSeen                      *string                       `json:"last_seen"`
	Metrics                       map[string]any                `json:"metrics"`
	ProviderNames                 []string                      `json:"provider_names"`
	RequestModels                 []string                      `json:"request_models"`
	SpanCount                     int64                         `json:"span_count"`
	TotalCacheCreationInputTokens int64                         `json:"total_cache_creation_input_tokens"`
	TotalCacheReadInputTokens     int64                         `json:"total_cache_read_input_tokens"`
	TotalCostUSD                  *float64                      `json:"total_cost_usd"`
	TotalDurationMS               int64                         `json:"total_duration_ms"`
	TotalInputCostUSD             *float64                      `json:"total_input_cost_usd"`
	TotalInputTokens              int64                         `json:"total_input_tokens"`
	TotalOutputCostUSD            *float64                      `json:"total_output_cost_usd"`
	TotalOutputTokens             int64                         `json:"total_output_tokens"`
	TotalReasoningTokens          int64                         `json:"total_reasoning_tokens"`
	// Weft extensions.
	LatencyMSP50 *float64 `json:"latency_ms_p50"`
	LatencyMSP95 *float64 `json:"latency_ms_p95"`
	TraceCount   int      `json:"trace_count"`
	// TurnCount counts turn spans, including SDK turns that have no agent
	// invocation span.
	TurnCount int64 `json:"turn_count"`

	agg *spanAgg
}

// AgentDistribution is one group of a span distribution.
type AgentDistribution struct {
	Alias        string           `json:"alias"`
	Key          string           `json:"key"`
	Source       string           `json:"source"`
	ValueType    string           `json:"value_type"`
	TotalCount   int              `json:"total_count"`
	PresentCount int              `json:"present_count"`
	MissingCount int              `json:"missing_count"`
	OtherCount   int              `json:"other_count"`
	Values       []map[string]any `json:"values"`
	Bins         []map[string]any `json:"bins"`
}

// AgentSpansQueryRes is the response of /agents/spans/query.
type AgentSpansQueryRes struct {
	Groups     []*AgentSpanGroup `json:"groups"`
	Spans      []*AgentSpan      `json:"spans"`
	TotalCount int               `json:"total_count"`
}

func (s *Store) filteredSpans(ctx context.Context, pid string, query any, after, before *string, needAttrs bool, extra agentSpanFilter) ([]*agentSpan, error) {
	a, b, err := timeRange(after, before)
	if err != nil {
		return nil, err
	}
	extra.After, extra.Before = a, b
	extra.WithAttrs = needAttrs || queryNeedsAttrs(query)
	spans, err := s.loadAgentSpans(ctx, pid, extra)
	if err != nil {
		return nil, err
	}
	if extra.Eval.active() {
		if spans, err = s.filterByEval(ctx, pid, spans, extra.Eval); err != nil {
			return nil, err
		}
	}
	if exprOf(query) == nil {
		return spans, nil
	}
	out := spans[:0:0]
	for _, sp := range spans {
		ok, err := matchAgentQuery(query, sp)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, sp)
		}
	}
	return out, nil
}

func measureValue(m AgentMeasureSpec, spans []*agentSpan) (any, error) {
	var vals []any
	for _, sp := range spans {
		if m.Filter != nil {
			ok, err := matchAgentQuery(m.Filter, sp)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
		}
		if m.Value == nil {
			vals = append(vals, true)
		} else {
			vals = append(vals, sp.ref(*m.Value))
		}
	}
	return aggregate(m.Aggregation, vals)
}

func aggregate(agg string, vals []any) (any, error) {
	switch agg {
	case "count":
		n := 0
		for _, v := range vals {
			if v != nil {
				n++
			}
		}
		return int64(n), nil
	case "count_distinct":
		seen := map[string]bool{}
		for _, v := range vals {
			if v != nil {
				seen[fmt.Sprint(v)] = true
			}
		}
		return int64(len(seen)), nil
	case "count_true", "count_false":
		n := 0
		for _, v := range vals {
			if v != nil && truthy(v) == (agg == "count_true") {
				n++
			}
		}
		return int64(n), nil
	case "sum", "avg", "min", "max":
		var sum float64
		var n int
		var mn, mx any
		for _, v := range vals {
			if v == nil {
				continue
			}
			if mn == nil || compareValues(v, mn) < 0 {
				mn = v
			}
			if mx == nil || compareValues(v, mx) > 0 {
				mx = v
			}
			if f, ok := toFloat(v); ok {
				sum += f
				n++
			}
		}
		switch agg {
		case "sum":
			return sum, nil
		case "avg":
			if n == 0 {
				return nil, nil
			}
			return sum / float64(n), nil
		case "min":
			return mn, nil
		}
		return mx, nil
	}
	return nil, Invalidf("unknown aggregation %q", agg)
}

func validateMeasure(m AgentMeasureSpec) error {
	if m.Alias == "" {
		return Invalidf("measure alias is required")
	}
	if m.Value != nil {
		if err := m.Value.validate(); err != nil {
			return err
		}
	} else if m.Aggregation != "count" {
		return Invalidf("measure %q needs a value", m.Alias)
	}
	_, err := aggregate(m.Aggregation, nil)
	return err
}

// QueryAgentSpans returns raw spans, or grouped aggregates when group_by is set.
func (s *Store) QueryAgentSpans(ctx context.Context, req AgentSpansQueryReq) (*AgentSpansQueryRes, error) {
	pid, err := refs.NormalizeProject(req.ProjectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	needAttrs := len(req.CustomAttrColumns) > 0 || len(req.GroupDistributions) > 0
	for _, g := range req.GroupBy {
		if err := g.validate(); err != nil {
			return nil, err
		}
		needAttrs = needAttrs || strings.HasPrefix(g.Source, "custom_attrs")
	}
	for _, m := range req.Measures {
		if err := validateMeasure(m); err != nil {
			return nil, err
		}
		needAttrs = needAttrs || (m.Value != nil && strings.HasPrefix(m.Value.Source, "custom_attrs")) || queryNeedsAttrs(m.Filter)
	}
	for _, gf := range req.GroupFilters {
		if err := validateMeasure(gf.Measure); err != nil {
			return nil, err
		}
	}
	spans, err := s.filteredSpans(ctx, pid, req.Query, req.StartedAfter, req.StartedBefore, needAttrs, agentSpanFilter{Eval: &req.AgentEvalFilter})
	if err != nil {
		return nil, err
	}
	if spans, err = s.applySignalFilters(ctx, pid, spans, req.SignalFilters, req.InsightFilters); err != nil {
		return nil, err
	}
	res := &AgentSpansQueryRes{Groups: []*AgentSpanGroup{}, Spans: []*AgentSpan{}}
	if len(req.GroupBy) == 0 {
		sorts := req.SortBy
		if len(sorts) == 0 {
			sorts = []SortBy{{Field: "started_at", Direction: "desc"}}
		}
		for _, so := range sorts {
			if !validAgentField(so.Field) {
				return nil, Invalidf("cannot sort spans by %q", so.Field)
			}
		}
		sort.SliceStable(spans, func(i, j int) bool {
			for _, so := range sorts {
				less, eq := sortLess(spans[i].field(so.Field), spans[j].field(so.Field), so.Direction == "desc")
				if !eq {
					return less
				}
			}
			return false
		})
		res.TotalCount = len(spans)
		pg := page(spans, req.Limit, req.Offset, 100)
		var calls map[string]*Call
		if req.IncludeDetails {
			if calls, err = s.callsByID(ctx, pid, spanIDs(pg)); err != nil {
				return nil, err
			}
		}
		for _, sp := range pg {
			res.Spans = append(res.Spans, sp.toAPI(pid, calls[sp.ID], req.IncludeCosts, req.CustomAttrColumns))
		}
		return res, nil
	}

	groups, err := groupSpans(spans, req.GroupBy, req.Measures, req.IncludeCosts)
	if err != nil {
		return nil, err
	}
	if len(req.GroupFilters) > 0 {
		kept := groups[:0]
		for _, g := range groups {
			ok, err := passesGroupFilters(g, req.GroupFilters)
			if err != nil {
				return nil, err
			}
			if ok {
				kept = append(kept, g)
			}
		}
		groups = kept
	}
	sorts := req.SortBy
	if len(sorts) == 0 {
		sorts = []SortBy{{Field: "last_seen", Direction: "desc"}}
	}
	sort.SliceStable(groups, func(i, j int) bool {
		for _, so := range sorts {
			less, eq := sortLess(groups[i].sortValue(so.Field), groups[j].sortValue(so.Field), so.Direction == "desc")
			if !eq {
				return less
			}
		}
		return false
	})
	res.TotalCount = len(groups)
	res.Groups = page(groups, req.Limit, req.Offset, 100)
	if err := s.fillGroupPreviews(ctx, pid, res.Groups); err != nil {
		return nil, err
	}
	for _, g := range res.Groups {
		for _, d := range req.GroupDistributions {
			g.Distributions[d.Alias] = distribution(d, g.agg.members)
		}
	}
	return res, nil
}

func spanIDs(spans []*agentSpan) []string {
	out := make([]string, len(spans))
	for i, sp := range spans {
		out[i] = sp.ID
	}
	return out
}

func groupSpans(spans []*agentSpan, by []AgentValueRef, measures []AgentMeasureSpec, includeCosts bool) ([]*AgentSpanGroup, error) {
	idx := map[string]*AgentSpanGroup{}
	var order []*AgentSpanGroup
	for _, sp := range spans {
		keys := map[string]any{}
		var kparts []string
		for _, ref := range by {
			v := sp.ref(ref)
			keys[ref.name()] = v
			kparts = append(kparts, jsonString(v))
		}
		k := strings.Join(kparts, "\x00")
		g := idx[k]
		if g == nil {
			g = &AgentSpanGroup{GroupKeys: keys, agg: newSpanAgg(), Distributions: map[string]*AgentDistribution{}, Metrics: map[string]any{}}
			idx[k] = g
			order = append(order, g)
		}
		g.agg.add(sp)
	}
	for _, g := range order {
		a := g.agg
		a.finish()
		traces := map[string]bool{}
		for _, sp := range a.members {
			traces[sp.TraceID] = true
		}
		g.AgentNames, g.AgentVersions = sortedKeys(a.agents), sortedKeys(a.versions)
		g.ConversationCount, g.ConversationNames = len(a.convs), sortedKeys(a.convNames)
		g.ErrorCount, g.InvocationCount, g.SpanCount = a.errors, a.invocations, a.spans
		g.TurnCount = a.turns
		g.FirstSeen, g.LastSeen = microsPtr(a.first), microsPtr(a.last)
		g.ProviderNames, g.RequestModels = sortedKeys(a.providers), sortedKeys(a.models)
		g.TotalCacheCreationInputTokens, g.TotalCacheReadInputTokens = int64(a.cacheW), int64(a.cacheR)
		g.TotalInputTokens, g.TotalOutputTokens, g.TotalReasoningTokens = int64(a.inTok), int64(a.outTok), int64(a.reason)
		g.TotalDurationMS = int64(math.Round(a.topDur))
		g.LatencyMSP50, g.LatencyMSP95 = pctPtr(a.topDurs, 0.5), pctPtr(a.topDurs, 0.95)
		g.TraceCount = len(traces)
		if includeCosts && a.hasCost {
			in, out := a.inCost, a.outCost
			g.TotalInputCostUSD, g.TotalOutputCostUSD, g.TotalCostUSD = &in, &out, a.cost()
		}
		for _, m := range measures {
			v, err := measureValue(m, a.members)
			if err != nil {
				return nil, err
			}
			g.Metrics[m.Alias] = v
		}
	}
	return order, nil
}

func passesGroupFilters(g *AgentSpanGroup, filters []AgentGroupFilter) (bool, error) {
	for _, f := range filters {
		v, err := measureValue(f.Measure, g.agg.members)
		if err != nil {
			return false, err
		}
		if v == nil {
			return false, nil
		}
		if f.Min != nil && compareValues(v, f.Min) < 0 {
			return false, nil
		}
		if f.Max != nil && compareValues(v, f.Max) > 0 {
			return false, nil
		}
	}
	return true, nil
}

func (g *AgentSpanGroup) sortValue(field string) any {
	if v, ok := g.GroupKeys[field]; ok {
		return v
	}
	if v, ok := g.Metrics[field]; ok {
		return v
	}
	switch field {
	case "span_count":
		return g.SpanCount
	case "invocation_count":
		return g.InvocationCount
	case "conversation_count":
		return int64(g.ConversationCount)
	case "error_count":
		return g.ErrorCount
	case "first_seen":
		return derefStr(g.FirstSeen)
	case "last_seen":
		return derefStr(g.LastSeen)
	case "total_input_tokens":
		return g.TotalInputTokens
	case "total_output_tokens":
		return g.TotalOutputTokens
	case "total_reasoning_tokens":
		return g.TotalReasoningTokens
	case "total_duration_ms":
		return g.TotalDurationMS
	case "total_cost_usd":
		return derefF(g.TotalCostUSD)
	case "latency_ms_p50":
		return derefF(g.LatencyMSP50)
	case "latency_ms_p95":
		return derefF(g.LatencyMSP95)
	}
	return nil
}

func distribution(d AgentDistributionSpec, spans []*agentSpan) *AgentDistribution {
	out := &AgentDistribution{Alias: d.Alias, Key: d.Value.Key, Source: d.Value.Source, Values: []map[string]any{}, Bins: []map[string]any{}}
	if out.Source == "" || out.Source == "field" {
		out.Source = "custom_attrs_string"
	}
	out.ValueType = strings.TrimPrefix(out.Source, "custom_attrs_")
	var nums []float64
	counts := map[string]int{}
	for _, sp := range spans {
		out.TotalCount++
		v := sp.ref(d.Value)
		if v == nil {
			out.MissingCount++
			continue
		}
		out.PresentCount++
		if f, ok := toFloat(v); ok && (out.ValueType == "int" || out.ValueType == "float") {
			nums = append(nums, f)
		} else {
			counts[fmt.Sprint(v)]++
		}
	}
	if len(nums) > 0 {
		bins := d.Bins
		if bins <= 0 {
			bins = 12
		}
		sort.Float64s(nums)
		mn, mx := nums[0], nums[len(nums)-1]
		width := (mx - mn) / float64(bins)
		if width == 0 {
			bins, width = 1, 1
		}
		cnt := make([]int, bins)
		for _, x := range nums {
			i := int((x - mn) / width)
			if i >= bins {
				i = bins - 1
			}
			cnt[i]++
		}
		for i, c := range cnt {
			out.Bins = append(out.Bins, map[string]any{"index": i, "min": mn + float64(i)*width, "max": mn + float64(i+1)*width, "count": c})
		}
	}
	type kv struct {
		v string
		n int
	}
	var kvs []kv
	for v, n := range counts {
		kvs = append(kvs, kv{v, n})
	}
	sort.Slice(kvs, func(i, j int) bool { return kvs[i].n > kvs[j].n || kvs[i].n == kvs[j].n && kvs[i].v < kvs[j].v })
	top := d.TopN
	if top <= 0 {
		top = 5
	}
	for i, x := range kvs {
		if i < top {
			out.Values = append(out.Values, map[string]any{"value": x.v, "count": x.n})
		} else {
			out.OtherCount += x.n
		}
	}
	return out
}

// callsByID loads full calls (inputs, output, attributes) for a set of ids.
func (s *Store) callsByID(ctx context.Context, pid string, ids []string) (map[string]*Call, error) {
	out := map[string]*Call{}
	for start := 0; start < len(ids); start += 500 {
		end := min(start+500, len(ids))
		lim := end - start
		calls, err := s.ListCalls(ctx, CallsQuery{ProjectID: pid, Filter: &CallsFilter{CallIDs: ids[start:end]}, Limit: &lim})
		if err != nil {
			return nil, err
		}
		for _, c := range calls {
			out[c.ID] = c
		}
	}
	return out, nil
}

func ptrIf(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func intPtr(f float64) *int64 {
	v := int64(math.Round(f))
	return &v
}

func (sp *agentSpan) toAPI(pid string, call *Call, includeCosts bool, cols []AgentValueRef) *AgentSpan {
	op := ptrIf(sp.Op)
	out := &AgentSpan{
		AgentDescription: ptrIf(sp.AgentDesc), AgentID: ptrIf(sp.AgentID), AgentName: ptrIf(sp.AgentName), AgentVersion: ptrIf(sp.AgentVersion),
		ArtifactRefs: []string{}, ContentRefs: []string{}, ObjectRefs: []string{}, FinishReasons: []string{},
		ConversationID: ptrIf(sp.ConvID), ConversationName: ptrIf(sp.ConvName), ErrorType: ptrIf(sp.errorType()),
		InputMessages: []SpanMessage{}, OutputMessages: []SpanMessage{}, RequestStopSequences: []string{}, SystemInstructions: []string{},
		OperationName: op, ParentSpanID: ptrIf(sp.ParentID), ParentCallID: ptrIf(sp.ParentCallID), ParentCallTraceID: ptrIf(sp.ParentCallTraceID), ProjectID: pid, ProviderName: ptrIf(sp.Provider),
		RequestModel: ptrIf(sp.RequestModel), ResponseModel: ptrIf(sp.ResponseModel), SpanID: sp.ID, SpanName: ptrIf(sp.SpanName),
		StartedAt: microsPtr(sp.StartedUS), StatusCode: sp.statusCode(), StatusMessage: sp.Exception,
		ToolCallID: ptrIf(sp.ToolCallID), ToolName: ptrIf(sp.ToolName), TraceID: sp.TraceID,
		RunID: ptrIf(sp.RunID), UserID: ptrIf(sp.UserID), IsTurn: sp.IsTurn, DisplayName: sp.displayName(),
	}
	if sp.Ended {
		out.EndedAt = microsPtr(sp.EndedUS)
		d, _ := sp.durationMS()
		out.DurationMS = &d
	}
	if sp.InTok > 0 || sp.OutTok > 0 || sp.Op == OpChat {
		out.InputTokens, out.OutputTokens = intPtr(sp.InTok), intPtr(sp.OutTok)
	}
	if sp.ReasonTok > 0 {
		out.ReasoningTokens = intPtr(sp.ReasonTok)
	}
	if sp.CacheRead > 0 {
		out.CacheReadInputTokens = intPtr(sp.CacheRead)
	}
	if sp.CacheW > 0 {
		out.CacheCreationInputTokens = intPtr(sp.CacheW)
	}
	if includeCosts && sp.HasCost {
		in, o, t := sp.InCost, sp.OutCost, sp.InCost+sp.OutCost
		out.InputCostUSD, out.OutputCostUSD, out.TotalCostUSD = &in, &o, &t
		cr, cw := sp.CacheReadCost, sp.CacheWCost
		out.CacheReadCostUSD, out.CacheCreationCostUSD = &cr, &cw
	}
	if sp.attrsJSON != "" || call != nil {
		if sp.attrsJSON == "" && call != nil {
			b, _ := json.Marshal(call.Attributes)
			sp.attrsJSON = string(b)
		}
		s, i, f, b := sp.customByType()
		if len(cols) > 0 {
			keep := map[string]bool{}
			for _, c := range cols {
				keep[c.Key] = true
			}
			for k := range s {
				if !keep[k] {
					delete(s, k)
				}
			}
			for k := range i {
				if !keep[k] {
					delete(i, k)
				}
			}
			for k := range f {
				if !keep[k] {
					delete(f, k)
				}
			}
			for k := range b {
				if !keep[k] {
					delete(b, k)
				}
			}
		}
		out.CustomAttrsString, out.CustomAttrsInt, out.CustomAttrsFloat, out.CustomAttrsBool = s, i, f, b
	} else {
		out.CustomAttrsString, out.CustomAttrsInt, out.CustomAttrsFloat, out.CustomAttrsBool =
			map[string]string{}, map[string]int64{}, map[string]float64{}, map[string]bool{}
	}
	if call != nil {
		sp.fillDetails(out, call)
	}
	return out
}

// fillDetails adds message bodies and tool payloads from the call.
func (sp *agentSpan) fillDetails(out *AgentSpan, call *Call) {
	a := newSpanAttrs(call.Attributes)
	if k := asMap(call.Attributes["otel"]); k != nil {
		if kind, _ := k["kind"].(string); kind != "" {
			out.SpanKind = &kind
		}
	}
	ins := InputMessages(call.Inputs)
	for _, m := range ins {
		if m.Role == "system" {
			out.SystemInstructions = append(out.SystemInstructions, m.Text)
			continue
		}
		out.InputMessages = append(out.InputMessages, SpanMessage{Role: m.Role, Content: messageContent(m)})
	}
	if sys := a.str("gen_ai.system_instructions"); sys != "" && len(out.SystemInstructions) == 0 {
		out.SystemInstructions = append(out.SystemInstructions, partsTextOf(sys))
	}
	outs := OutputMessages(call.Output)
	var reasoning []string
	for _, m := range outs {
		out.OutputMessages = append(out.OutputMessages, SpanMessage{Role: m.Role, Content: messageContent(m), FinishReason: m.FinishReason})
		if m.FinishReason != "" {
			out.FinishReasons = append(out.FinishReasons, m.FinishReason)
		}
		if m.Reasoning != "" {
			reasoning = append(reasoning, m.Reasoning)
		}
	}
	if len(reasoning) > 0 {
		r := strings.Join(reasoning, "\n")
		out.ReasoningContent = &r
	}
	if sp.Op == OpExecuteTool {
		args := a.str("gen_ai.tool.call.arguments", "ai.toolCall.args", "tool_arguments", "gcp.vertex.agent.tool_call_args", "input.value")
		if args == "" && len(call.Inputs) > 0 {
			args = jsonString(call.Inputs)
		}
		res := a.str("gen_ai.tool.call.result", "ai.toolCall.result", "tool_response", "gcp.vertex.agent.tool_response", "output.value")
		if res == "" && call.Output != nil {
			res = jsonString(call.Output)
		}
		out.ToolCallArguments, out.ToolCallResult = ptrIf(args), ptrIf(res)
		out.ToolType = ptrIf(a.str("gen_ai.tool.type"))
		out.ToolDescription = ptrIf(a.str("gen_ai.tool.description", "tool.description"))
	}
	out.ToolDefinitions = ptrIf(a.str("gen_ai.tool.definitions"))
	if f, ok := toFloat(a.get("gen_ai.request.temperature")); ok {
		out.RequestTemperature = &f
	}
	if f, ok := toFloat(a.get("gen_ai.request.max_tokens")); ok {
		out.RequestMaxTokens = intPtr(f)
	}
}

// partsTextOf flattens GenAI system_instructions parts into text.
func partsTextOf(s string) string {
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return s
	}
	l, ok := v.([]any)
	if !ok {
		return s
	}
	var ts []string
	for _, p := range l {
		if m := asMap(p); m != nil {
			if t, ok := m["content"].(string); ok {
				ts = append(ts, t)
			}
		} else if t, ok := p.(string); ok {
			ts = append(ts, t)
		}
	}
	return strings.Join(ts, "\n")
}

// ---------------------------------------------------------------- custom attrs schema

// AgentCustomAttrsSchemaReq is the body of /agents/spans/custom-attrs/schema.
type AgentCustomAttrsSchemaReq struct {
	ProjectID string `json:"project_id"`
	AgentEvalFilter
	Limit         *int    `json:"limit"`
	Offset        int     `json:"offset"`
	Query         any     `json:"query"`
	StartedAfter  *string `json:"started_after"`
	StartedBefore *string `json:"started_before"`
}

// AgentCustomAttr is one key/type observed on matching spans.
type AgentCustomAttr struct {
	Key       string `json:"key"`
	Source    string `json:"source"`
	SpanCount int    `json:"span_count"`
	ValueType string `json:"value_type"`
}

// CustomAttrsSchema lists custom attribute keys by how many spans carry them.
func (s *Store) CustomAttrsSchema(ctx context.Context, req AgentCustomAttrsSchemaReq) ([]AgentCustomAttr, bool, int, error) {
	pid, err := refs.NormalizeProject(req.ProjectID)
	if err != nil {
		return nil, false, 0, &InvalidError{Msg: err.Error()}
	}
	spans, err := s.filteredSpans(ctx, pid, req.Query, req.StartedAfter, req.StartedBefore, true, agentSpanFilter{Eval: &req.AgentEvalFilter})
	if err != nil {
		return nil, false, 0, err
	}
	counts := map[[2]string]int{}
	for _, sp := range spans {
		for k, v := range sp.customAttrs() {
			counts[[2]string{k, attrType(v)}]++
		}
	}
	out := make([]AgentCustomAttr, 0, len(counts))
	for k, n := range counts {
		out = append(out, AgentCustomAttr{Key: k[0], ValueType: k[1], Source: "custom_attrs_" + k[1], SpanCount: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SpanCount != out[j].SpanCount {
			return out[i].SpanCount > out[j].SpanCount
		}
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].ValueType < out[j].ValueType
	})
	limit := 200
	if req.Limit != nil && *req.Limit > 0 {
		limit = *req.Limit
	}
	pg := page(out, &limit, req.Offset, limit)
	return pg, req.Offset+len(pg) < len(out), limit, nil
}

// ---------------------------------------------------------------- /agents/spans/stats

// AgentStatsMetric is one metric of /agents/spans/stats.
type AgentStatsMetric struct {
	Alias        string        `json:"alias"`
	Value        AgentValueRef `json:"value"`
	ValueType    string        `json:"value_type"`
	Aggregations []string      `json:"aggregations"`
	Percentiles  []float64     `json:"percentiles"`
}

// AgentBucketBy picks time or numeric bucketing.
type AgentBucketBy struct {
	Type    string            `json:"type"`
	Alias   string            `json:"alias"`
	Bins    int               `json:"bins"`
	Min     *float64          `json:"min"`
	Max     *float64          `json:"max"`
	Value   *AgentValueRef    `json:"value"`
	GroupBy []AgentValueRef   `json:"group_by"`
	Measure *AgentMeasureSpec `json:"measure"`
}

// AgentSpanStatsReq is the body of /agents/spans/stats.
type AgentSpanStatsReq struct {
	ProjectID string `json:"project_id"`
	AgentEvalFilter
	Start          string               `json:"start"`
	End            *string              `json:"end"`
	BucketBy       *AgentBucketBy       `json:"bucket_by"`
	Granularity    *int64               `json:"granularity"`
	GroupBy        []AgentValueRef      `json:"group_by"`
	GroupFilters   []AgentGroupFilter   `json:"group_filters"`
	GroupLimit     int                  `json:"group_limit"`
	InsightFilters []AgentInsightFilter `json:"insight_filters"`
	Metrics        []AgentStatsMetric   `json:"metrics"`
	Query          any                  `json:"query"`
	SignalFilters  *AgentSignalFilters  `json:"signal_filters"`
	Timezone       string               `json:"timezone"`
}

// AgentStatsColumn describes one column of the stats rows.
type AgentStatsColumn struct {
	Aggregation *string `json:"aggregation"`
	Metric      *string `json:"metric"`
	Name        string  `json:"name"`
	Role        string  `json:"role"`
	ValueType   string  `json:"value_type"`
}

// AgentSpanStatsRes is the response of /agents/spans/stats.
type AgentSpanStatsRes struct {
	BucketType  string             `json:"bucket_type"`
	Columns     []AgentStatsColumn `json:"columns"`
	End         string             `json:"end"`
	Granularity *int64             `json:"granularity"`
	Rows        []map[string]any   `json:"rows"`
	Start       string             `json:"start"`
	Timezone    string             `json:"timezone"`
}

func pctName(p float64) string {
	return "p" + strconv.FormatFloat(p, 'f', -1, 64)
}

func metricValues(m AgentStatsMetric, spans []*agentSpan) map[string]any {
	vals := make([]any, 0, len(spans))
	var nums []float64
	for _, sp := range spans {
		v := sp.ref(m.Value)
		vals = append(vals, v)
		if f, ok := toFloat(v); ok && v != nil {
			if _, isBool := v.(bool); !isBool {
				nums = append(nums, f)
			}
		}
	}
	out := map[string]any{}
	for _, a := range m.Aggregations {
		v, _ := aggregate(a, vals)
		out[m.Alias+"_"+a] = v
	}
	sort.Float64s(nums)
	for _, p := range m.Percentiles {
		if v := percentile(nums, p/100); v != nil {
			out[m.Alias+"_"+pctName(p)] = *v
		} else {
			out[m.Alias+"_"+pctName(p)] = nil
		}
	}
	return out
}

// SpanStats computes chart rows: one per time (or numeric) bucket and group.
// Every row also carries "count", the number of matching spans.
func (s *Store) SpanStats(ctx context.Context, req AgentSpanStatsReq) (*AgentSpanStatsRes, error) {
	pid, err := refs.NormalizeProject(req.ProjectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	if req.Start == "" {
		return nil, Invalidf("start is required")
	}
	startUS, err := ParseTime(req.Start)
	if err != nil {
		return nil, err
	}
	endUS := time.Now().UnixMicro()
	if req.End != nil && *req.End != "" {
		if endUS, err = ParseTime(*req.End); err != nil {
			return nil, err
		}
	}
	if endUS <= startUS {
		return nil, Invalidf("end must be after start")
	}
	tz := req.Timezone
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, Invalidf("unknown timezone %q", tz)
	}
	needAttrs := false
	for _, g := range req.GroupBy {
		if err := g.validate(); err != nil {
			return nil, err
		}
		needAttrs = needAttrs || strings.HasPrefix(g.Source, "custom_attrs")
	}
	for _, m := range req.Metrics {
		if m.Alias == "" {
			return nil, Invalidf("metric alias is required")
		}
		if err := m.Value.validate(); err != nil {
			return nil, err
		}
		for _, a := range m.Aggregations {
			if _, err := aggregate(a, nil); err != nil {
				return nil, err
			}
		}
		for _, p := range m.Percentiles {
			if p < 0 || p > 100 {
				return nil, Invalidf("percentiles must be within 0..100")
			}
		}
		needAttrs = needAttrs || strings.HasPrefix(m.Value.Source, "custom_attrs")
	}
	startStr, endStr := FormatMicros(startUS), FormatMicros(endUS)
	spans, err := s.filteredSpans(ctx, pid, req.Query, &startStr, &endStr, needAttrs, agentSpanFilter{Eval: &req.AgentEvalFilter})
	if err != nil {
		return nil, err
	}
	if spans, err = s.applySignalFilters(ctx, pid, spans, req.SignalFilters, req.InsightFilters); err != nil {
		return nil, err
	}

	// Keep the top groups by span count, then apply group filters.
	groupKey := func(sp *agentSpan) (string, map[string]any) {
		vals := map[string]any{}
		var parts []string
		for _, g := range req.GroupBy {
			v := sp.ref(g)
			vals[g.name()] = v
			parts = append(parts, jsonString(v))
		}
		return strings.Join(parts, "\x00"), vals
	}
	if len(req.GroupBy) > 0 {
		members := map[string][]*agentSpan{}
		for _, sp := range spans {
			k, _ := groupKey(sp)
			members[k] = append(members[k], sp)
		}
		var keys []string
		for k, ms := range members {
			ok := true
			for _, f := range req.GroupFilters {
				v, err := measureValue(f.Measure, ms)
				if err != nil {
					return nil, err
				}
				if v == nil || (f.Min != nil && compareValues(v, f.Min) < 0) || (f.Max != nil && compareValues(v, f.Max) > 0) {
					ok = false
				}
			}
			if ok {
				keys = append(keys, k)
			}
		}
		sort.Slice(keys, func(i, j int) bool {
			if len(members[keys[i]]) != len(members[keys[j]]) {
				return len(members[keys[i]]) > len(members[keys[j]])
			}
			return keys[i] < keys[j]
		})
		limit := req.GroupLimit
		if limit <= 0 {
			limit = 50
		}
		if len(keys) > limit {
			keys = keys[:limit]
		}
		keep := map[string]bool{}
		for _, k := range keys {
			keep[k] = true
		}
		kept := spans[:0:0]
		for _, sp := range spans {
			if k, _ := groupKey(sp); keep[k] {
				kept = append(kept, sp)
			}
		}
		spans = kept
	}

	res := &AgentSpanStatsRes{BucketType: "time", Start: startStr, End: endStr, Timezone: tz, Rows: []map[string]any{}}
	str := func(s string) *string { return &s }
	metricCols := func() []AgentStatsColumn {
		cols := []AgentStatsColumn{{Name: "count", Role: "metric", ValueType: "number", Aggregation: str("count")}}
		for _, m := range req.Metrics {
			for _, a := range m.Aggregations {
				vt := "number"
				if (a == "min" || a == "max") && m.ValueType != "" && m.ValueType != "boolean" {
					vt = m.ValueType
				}
				cols = append(cols, AgentStatsColumn{Name: m.Alias + "_" + a, Role: "metric", ValueType: vt, Metric: str(m.Alias), Aggregation: str(a)})
			}
			for _, p := range m.Percentiles {
				cols = append(cols, AgentStatsColumn{Name: m.Alias + "_" + pctName(p), Role: "metric", ValueType: "number", Metric: str(m.Alias), Aggregation: str(pctName(p))})
			}
		}
		return cols
	}
	groupCols := func() []AgentStatsColumn {
		var cols []AgentStatsColumn
		for _, g := range req.GroupBy {
			cols = append(cols, AgentStatsColumn{Name: g.name(), Role: "group", ValueType: "string"})
		}
		return cols
	}
	emit := func(base map[string]any, members []*agentSpan) {
		byGroup := map[string][]*agentSpan{}
		gvals := map[string]map[string]any{}
		var order []string
		for _, sp := range members {
			k, v := groupKey(sp)
			if _, ok := byGroup[k]; !ok {
				order = append(order, k)
				gvals[k] = v
			}
			byGroup[k] = append(byGroup[k], sp)
		}
		sort.Strings(order)
		for _, k := range order {
			row := map[string]any{}
			for kk, vv := range base {
				row[kk] = vv
			}
			for kk, vv := range gvals[k] {
				row[kk] = vv
			}
			row["count"] = int64(len(byGroup[k]))
			for _, m := range req.Metrics {
				for kk, vv := range metricValues(m, byGroup[k]) {
					row[kk] = vv
				}
			}
			res.Rows = append(res.Rows, row)
		}
	}

	if req.BucketBy != nil && req.BucketBy.Type == "number" {
		return s.numericSpanStats(req, res, spans, groupCols(), metricCols(), emit)
	}

	gran := int64(0)
	if req.Granularity != nil && *req.Granularity > 0 {
		gran = *req.Granularity
	} else {
		span := (endUS - startUS) / 1_000_000
		gran = niceBuckets[len(niceBuckets)-1]
		for _, b := range niceBuckets {
			if span/b <= 60 {
				gran = b
				break
			}
		}
	}
	if (endUS-startUS)/1_000_000/gran > 10000 {
		gran = (endUS-startUS)/1_000_000/10000 + 1
	}
	res.Granularity = &gran
	res.Columns = append([]AgentStatsColumn{{Name: "timestamp", Role: "time", ValueType: "datetime"}}, groupCols()...)
	res.Columns = append(res.Columns, metricCols()...)
	// Align buckets to the granularity in the requested time zone.
	_, off := time.UnixMicro(startUS).In(loc).Zone()
	gUS := gran * 1_000_000
	first := ((startUS+int64(off)*1_000_000)/gUS)*gUS - int64(off)*1_000_000
	buckets := map[int64][]*agentSpan{}
	for _, sp := range spans {
		b := first + ((sp.StartedUS-first)/gUS)*gUS
		buckets[b] = append(buckets[b], sp)
	}
	for b := first; b < endUS; b += gUS {
		ts := time.UnixMicro(b).In(loc).Format(time.RFC3339)
		members := buckets[b]
		if len(members) == 0 {
			if len(req.GroupBy) == 0 { // keep empty buckets so charts have a continuous axis
				row := map[string]any{"timestamp": ts, "count": int64(0)}
				for _, m := range req.Metrics {
					for k, v := range metricValues(m, nil) {
						row[k] = v
					}
				}
				res.Rows = append(res.Rows, row)
			}
			continue
		}
		emit(map[string]any{"timestamp": ts}, members)
	}
	return res, nil
}

func (s *Store) numericSpanStats(req AgentSpanStatsReq, res *AgentSpanStatsRes, spans []*agentSpan, groupCols, metricCols []AgentStatsColumn,
	emit func(map[string]any, []*agentSpan)) (*AgentSpanStatsRes, error) {
	bb := req.BucketBy
	alias := bb.Alias
	if alias == "" {
		alias = "value"
	}
	bins := bb.Bins
	if bins <= 0 {
		bins = 24
	}
	if bins > 200 {
		return nil, Invalidf("bins must be at most 200")
	}
	res.BucketType = "number"
	str := func(s string) *string { return &s }
	// Values to bucket: per span, or per group of bucket_by.group_by with a measure.
	type item struct {
		v       float64
		members []*agentSpan
	}
	var items []item
	if bb.Measure != nil && len(bb.GroupBy) > 0 {
		if err := validateMeasure(*bb.Measure); err != nil {
			return nil, err
		}
		groups := map[string][]*agentSpan{}
		for _, sp := range spans {
			var parts []string
			for _, g := range bb.GroupBy {
				parts = append(parts, jsonString(sp.ref(g)))
			}
			k := strings.Join(parts, "\x00")
			groups[k] = append(groups[k], sp)
		}
		for _, ms := range groups {
			v, err := measureValue(*bb.Measure, ms)
			if err != nil {
				return nil, err
			}
			if f, ok := toFloat(v); ok && v != nil {
				items = append(items, item{f, ms})
			}
		}
	} else {
		if bb.Value == nil {
			return nil, Invalidf("numeric bucket_by needs a value or a group measure")
		}
		if err := bb.Value.validate(); err != nil {
			return nil, err
		}
		for _, sp := range spans {
			if f, ok := toFloat(sp.ref(*bb.Value)); ok && sp.ref(*bb.Value) != nil {
				items = append(items, item{f, []*agentSpan{sp}})
			}
		}
	}
	res.Columns = append([]AgentStatsColumn{
		{Name: alias + "_min", Role: "bucket", ValueType: "number", Aggregation: str("min")},
		{Name: alias + "_max", Role: "bucket", ValueType: "number", Aggregation: str("max")},
		{Name: "bucket_index", Role: "bucket", ValueType: "number"},
	}, groupCols...)
	res.Columns = append(res.Columns, metricCols...)
	if len(items) == 0 {
		return res, nil
	}
	mn, mx := items[0].v, items[0].v
	for _, it := range items {
		mn, mx = math.Min(mn, it.v), math.Max(mx, it.v)
	}
	if bb.Min != nil {
		mn = *bb.Min
	}
	if bb.Max != nil {
		mx = *bb.Max
	}
	width := (mx - mn) / float64(bins)
	if width <= 0 {
		bins, width = 1, 1
	}
	byBin := make([][]*agentSpan, bins)
	for _, it := range items {
		if it.v < mn || it.v > mx {
			continue
		}
		i := int((it.v - mn) / width)
		if i >= bins {
			i = bins - 1
		}
		byBin[i] = append(byBin[i], it.members...)
	}
	for i := 0; i < bins; i++ {
		base := map[string]any{alias + "_min": mn + float64(i)*width, alias + "_max": mn + float64(i+1)*width, "bucket_index": i}
		if len(byBin[i]) == 0 {
			if len(req.GroupBy) == 0 {
				base["count"] = int64(0)
				res.Rows = append(res.Rows, base)
			}
			continue
		}
		emit(base, byBin[i])
	}
	return res, nil
}
