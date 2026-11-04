package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// AgentChatFeedback is feedback as projected into the agent chat views.
type AgentChatFeedback struct {
	ID                      string             `json:"id"`
	AnnotationRef           *string            `json:"annotation_ref"`
	CallRef                 *string            `json:"call_ref"`
	CreatedAt               *string            `json:"created_at"`
	Creator                 *string            `json:"creator"`
	FeedbackType            string             `json:"feedback_type"`
	Payload                 map[string]any     `json:"payload"`
	RunnableRef             *string            `json:"runnable_ref"`
	ScorerRatingConfidences map[string]float64 `json:"scorer_rating_confidences"`
	ScorerRatingReasons     map[string]string  `json:"scorer_rating_reasons"`
	ScorerRatings           map[string]float64 `json:"scorer_ratings"`
	ScorerTagConfidences    map[string]float64 `json:"scorer_tag_confidences"`
	ScorerTagReasons        map[string]string  `json:"scorer_tag_reasons"`
	ScorerTags              []string           `json:"scorer_tags"`
	TriggerRef              *string            `json:"trigger_ref"`
	UserID                  *string            `json:"user_id"`
	Ref                     string             `json:"ref"`
}

func chatFeedback(fs []Feedback) []AgentChatFeedback {
	out := make([]AgentChatFeedback, 0, len(fs))
	for _, f := range fs {
		created := f.CreatedAt
		cf := AgentChatFeedback{ID: f.ID, AnnotationRef: f.AnnotationRef, CallRef: f.CallRef, CreatedAt: &created, Creator: f.Creator,
			FeedbackType: f.FeedbackType, Payload: asMap(f.Payload), RunnableRef: f.RunnableRef, TriggerRef: f.TriggerRef, UserID: f.UserID,
			Ref: f.Ref, ScorerRatingConfidences: map[string]float64{}, ScorerRatingReasons: map[string]string{},
			ScorerRatings: map[string]float64{}, ScorerTagConfidences: map[string]float64{}, ScorerTagReasons: map[string]string{}, ScorerTags: []string{}}
		if cf.Payload == nil {
			cf.Payload = map[string]any{"value": f.Payload}
		}
		for _, r := range feedbackRatings(f) {
			cf.ScorerRatings[r.Name] = r.Value
		}
		cf.ScorerTags = append(cf.ScorerTags, feedbackTags(f)...)
		// Signal feedback keeps its scorer_* fields in its payload.
		if sc, ok := signalScoresOf(f.Payload); ok {
			for k, v := range sc.ScorerRatingConfidences {
				cf.ScorerRatingConfidences[k] = v
			}
			for k, v := range sc.ScorerRatingReasons {
				cf.ScorerRatingReasons[k] = v
			}
			for k, v := range sc.ScorerTagConfidences {
				cf.ScorerTagConfidences[k] = v
			}
			for k, v := range sc.ScorerTagReasons {
				cf.ScorerTagReasons[k] = v
			}
		}
		out = append(out, cf)
	}
	return out
}

// AgentChatAgentStart marks an agent (or sub-agent) invocation.
type AgentChatAgentStart struct {
	Model              *string `json:"model"`
	Status             *string `json:"status"`
	SystemInstructions *string `json:"system_instructions"`
	ToolDefinitions    *string `json:"tool_definitions"`
}

// AgentChatAssistant is assistant text from an LLM or agent span.
type AgentChatAssistant struct {
	ContentRefs      []string `json:"content_refs"`
	DurationMS       *int64   `json:"duration_ms"`
	InputCostUSD     *float64 `json:"input_cost_usd"`
	InputTokens      *int64   `json:"input_tokens"`
	Model            *string  `json:"model"`
	OutputCostUSD    *float64 `json:"output_cost_usd"`
	OutputTokens     *int64   `json:"output_tokens"`
	ReasoningContent *string  `json:"reasoning_content"`
	ReasoningTokens  *int64   `json:"reasoning_tokens"`
	Status           *string  `json:"status"`
	Text             string   `json:"text"`
	TotalCostUSD     *float64 `json:"total_cost_usd"`
	// Weft extension: tools the model asked for in this response. LLM calls
	// that only request tools appear as assistant messages with empty text.
	RequestedTools []string `json:"requested_tools,omitempty"`
}

// AgentChatToolCall is a tool execution (or, with include_model_tool_calls,
// a tool call requested by a model with no recorded execution).
type AgentChatToolCall struct {
	ContentRefs   []string `json:"content_refs"`
	DurationMS    *int64   `json:"duration_ms"`
	Status        *string  `json:"status"`
	ToolArguments *string  `json:"tool_arguments"`
	ToolName      *string  `json:"tool_name"`
	ToolResult    *string  `json:"tool_result"`
	// Weft extension.
	ToolCallID *string `json:"tool_call_id,omitempty"`
}

// AgentChatUser is a user prompt.
type AgentChatUser struct {
	ContentRefs []string `json:"content_refs"`
	Text        string   `json:"text"`
}

// AgentChatMessage is one element of the chat timeline.
type AgentChatMessage struct {
	AgentHandoff     any                  `json:"agent_handoff"`
	AgentName        *string              `json:"agent_name"`
	AgentStart       *AgentChatAgentStart `json:"agent_start"`
	AgentVersion     *string              `json:"agent_version"`
	AssistantMessage *AgentChatAssistant  `json:"assistant_message"`
	ContextCompacted any                  `json:"context_compacted"`
	ErrorType        *string              `json:"error_type"`
	Feedback         []AgentChatFeedback  `json:"feedback"`
	SpanID           *string              `json:"span_id"`
	StartedAt        *string              `json:"started_at"`
	StatusCode       *string              `json:"status_code"`
	StatusMessage    *string              `json:"status_message"`
	ToolCall         *AgentChatToolCall   `json:"tool_call"`
	Type             string               `json:"type"`
	UserMessage      *AgentChatUser       `json:"user_message"`
	// Weft extensions: the span's normalised operation and display name, and
	// media parts (images, audio) attached to a user or assistant message.
	OperationName *string `json:"operation_name,omitempty"`
	SpanName      *string `json:"span_name,omitempty"`
	Media         []any   `json:"media,omitempty"`
}

// AgentTraceChat is the response of /agents/traces/chat.
type AgentTraceChat struct {
	AgentName                     *string             `json:"agent_name"`
	AgentVersion                  *string             `json:"agent_version"`
	EndedAt                       *string             `json:"ended_at"`
	Feedback                      []AgentChatFeedback `json:"feedback"`
	Messages                      []AgentChatMessage  `json:"messages"`
	Provider                      *string             `json:"provider"`
	RootSpanName                  *string             `json:"root_span_name"`
	StartedAt                     *string             `json:"started_at"`
	StatusCode                    *string             `json:"status_code"`
	TotalCacheCreationInputTokens int64               `json:"total_cache_creation_input_tokens"`
	TotalCacheReadInputTokens     int64               `json:"total_cache_read_input_tokens"`
	TotalCostUSD                  *float64            `json:"total_cost_usd"`
	TotalDurationMS               *int64              `json:"total_duration_ms"`
	TotalInputTokens              int64               `json:"total_input_tokens"`
	TotalOutputTokens             int64               `json:"total_output_tokens"`
	TotalReasoningTokens          int64               `json:"total_reasoning_tokens"`
	TraceID                       string              `json:"trace_id"`
	UserID                        *string             `json:"user_id"`
	// Weft extensions.
	RootSpanID string `json:"root_span_id"`
	SpanCount  int    `json:"span_count"`
	LLMCount   int    `json:"llm_call_count"`
	ToolCount  int    `json:"tool_call_count"`
}

type chatBuilder struct {
	spans          map[string]*agentSpan
	calls          map[string]*Call
	children       map[string][]string
	feedback       bool
	modelToolCalls bool
	executed       map[string]bool
	msgs           []AgentChatMessage
	lastAssistant  string
}

func newChatBuilder(spans []*agentSpan, calls map[string]*Call, feedback, modelToolCalls bool) *chatBuilder {
	b := &chatBuilder{spans: map[string]*agentSpan{}, calls: calls, children: map[string][]string{}, feedback: feedback,
		modelToolCalls: modelToolCalls, executed: map[string]bool{}}
	for _, sp := range spans {
		b.spans[sp.ID] = sp
		if sp.ToolCallID != "" {
			b.executed[sp.ToolCallID] = true
		}
	}
	for _, sp := range spans { // spans are ordered by start time
		if _, ok := b.spans[sp.ParentID]; ok {
			b.children[sp.ParentID] = append(b.children[sp.ParentID], sp.ID)
		}
	}
	return b
}

func (b *chatBuilder) base(sp *agentSpan, typ string) AgentChatMessage {
	id := sp.ID
	m := AgentChatMessage{Type: typ, SpanID: &id, StartedAt: microsPtr(sp.StartedUS), AgentName: ptrIf(sp.AgentName),
		AgentVersion: ptrIf(sp.AgentVersion), ErrorType: ptrIf(sp.errorType()), StatusMessage: sp.Exception,
		OperationName: ptrIf(sp.Op), SpanName: ptrIf(sp.displayName())}
	st := sp.statusCode()
	m.StatusCode = &st
	if b.feedback {
		if c := b.calls[sp.ID]; c != nil && len(c.Feedback) > 0 {
			m.Feedback = chatFeedback(c.Feedback)
		}
	}
	return m
}

func durPtr(sp *agentSpan) *int64 {
	if d, ok := sp.durationMS(); ok {
		v := int64(d + 0.5)
		return &v
	}
	return nil
}

// userText finds the user prompt of a turn: the last user message of the
// root's inputs, else of the first LLM call below it, else the root's
// readable inputs. When the root's message carries no media, media attached
// to the first LLM call's user message (attach_media) is shown with it.
func (b *chatBuilder) userText(root string) (string, []any) {
	llmText, llmMedia, llmFound := b.firstLLMUser(root)
	if c := b.calls[root]; c != nil {
		if t, m := lastUserText(InputMessages(c.Inputs)); m != nil {
			if len(m.Media) == 0 {
				return t, llmMedia
			}
			return t, m.Media
		}
	}
	if llmFound {
		return llmText, llmMedia
	}
	if c := b.calls[root]; c != nil {
		return InputText(c.Inputs), nil
	}
	return "", nil
}

// firstLLMUser is the last user message of the first LLM call below root.
func (b *chatBuilder) firstLLMUser(root string) (string, []any, bool) {
	var found string
	var media []any
	var walk func(id string) bool
	walk = func(id string) bool {
		for _, ch := range b.children[id] {
			if sp := b.spans[ch]; sp != nil && sp.Op == OpChat {
				if c := b.calls[ch]; c != nil {
					if t, m := lastUserText(InputMessages(c.Inputs)); m != nil {
						found, media = t, m.Media
						return true
					}
				}
			}
			if walk(ch) {
				return true
			}
		}
		return false
	}
	ok := walk(root)
	return found, media, ok
}

func (b *chatBuilder) emitAssistant(sp *agentSpan, text, reasoning string, media []any, tools []string, withUsage bool) {
	if text == "" && reasoning == "" && len(media) == 0 && len(tools) == 0 {
		return
	}
	m := b.base(sp, "assistant_message")
	a := &AgentChatAssistant{ContentRefs: []string{}, Text: text, Model: ptrIf(sp.RequestModel), Status: m.StatusCode, DurationMS: durPtr(sp),
		RequestedTools: tools}
	if reasoning != "" {
		a.ReasoningContent = &reasoning
	}
	if withUsage {
		if sp.InTok > 0 || sp.OutTok > 0 {
			a.InputTokens, a.OutputTokens = intPtr(sp.InTok), intPtr(sp.OutTok)
		}
		if sp.ReasonTok > 0 {
			a.ReasoningTokens = intPtr(sp.ReasonTok)
		}
		if sp.HasCost {
			in, out, t := sp.InCost, sp.OutCost, sp.InCost+sp.OutCost
			a.InputCostUSD, a.OutputCostUSD, a.TotalCostUSD = &in, &out, &t
		}
	}
	m.AssistantMessage = a
	m.Media = media
	b.msgs = append(b.msgs, m)
	if text != "" {
		b.lastAssistant = text
	}
}

func (b *chatBuilder) walk(id string, isRoot bool) {
	sp := b.spans[id]
	if sp == nil {
		return
	}
	c := b.calls[id]
	if isRoot {
		if t, media := b.userText(id); t != "" || len(media) > 0 {
			m := b.base(sp, "user_message")
			m.StatusCode, m.StatusMessage, m.ErrorType = nil, nil, nil
			m.Feedback = nil
			m.UserMessage = &AgentChatUser{ContentRefs: []string{}, Text: t}
			m.Media = media
			b.msgs = append(b.msgs, m)
		}
	}
	switch sp.Op {
	case OpInvokeAgent:
		if !isRoot {
			m := b.base(sp, "agent_start")
			as := &AgentChatAgentStart{Model: ptrIf(sp.RequestModel), Status: m.StatusCode}
			if c != nil {
				a := newSpanAttrs(c.Attributes)
				if sys := a.str("gen_ai.system_instructions"); sys != "" {
					t := partsTextOf(sys)
					as.SystemInstructions = &t
				} else if msgs := InputMessages(c.Inputs); len(msgs) > 0 && msgs[0].Role == "system" {
					as.SystemInstructions = &msgs[0].Text
				}
				as.ToolDefinitions = ptrIf(a.str("gen_ai.tool.definitions"))
			}
			m.AgentStart = as
			b.msgs = append(b.msgs, m)
		}
	case OpChat:
		if c != nil {
			outs := OutputMessages(c.Output)
			text, reasoning := assistantText(outs)
			var media []any
			var tools []string
			for _, o := range outs {
				media = append(media, o.Media...)
				for _, tc := range o.ToolCalls {
					tools = append(tools, tc.Name)
				}
			}
			b.emitAssistant(sp, text, reasoning, media, tools, true)
			if b.modelToolCalls {
				for _, o := range outs {
					for _, tc := range o.ToolCalls {
						if tc.ID != "" && b.executed[tc.ID] {
							continue
						}
						m := b.base(sp, "tool_call")
						m.StatusCode, m.StatusMessage, m.ErrorType = nil, nil, nil
						m.ToolCall = &AgentChatToolCall{ContentRefs: []string{}, ToolName: ptrIf(tc.Name), ToolArguments: ptrIf(tc.Arguments), ToolCallID: ptrIf(tc.ID)}
						b.msgs = append(b.msgs, m)
					}
				}
			}
		}
	case OpExecuteTool:
		m := b.base(sp, "tool_call")
		tc := &AgentChatToolCall{ContentRefs: []string{}, ToolName: ptrIf(sp.ToolName), Status: m.StatusCode, DurationMS: durPtr(sp), ToolCallID: ptrIf(sp.ToolCallID)}
		if c != nil {
			out := &AgentSpan{}
			sp.fillDetails(out, c)
			tc.ToolArguments, tc.ToolResult = out.ToolCallArguments, out.ToolCallResult
		}
		m.ToolCall = tc
		b.msgs = append(b.msgs, m)
	}
	for _, ch := range b.children[id] {
		b.walk(ch, false)
	}
	// The final answer of a turn or (sub-)agent, when no LLM span already said it.
	if (isRoot || sp.Op == OpInvokeAgent) && sp.Op != OpChat && c != nil {
		text, reasoning := assistantText(OutputMessages(c.Output))
		if text == "" && isRoot && c.Output != nil && OutputMessages(c.Output) == nil {
			text = OutputText(c.Output)
		}
		if text != "" && strings.TrimSpace(text) != strings.TrimSpace(b.lastAssistant) {
			b.emitAssistant(sp, text, reasoning, nil, nil, false)
		}
	}
}

// subtree returns root and its descendants among spans.
func subtree(root string, spans []*agentSpan) []*agentSpan {
	kids := map[string][]*agentSpan{}
	for _, sp := range spans {
		kids[sp.ParentID] = append(kids[sp.ParentID], sp)
	}
	var out []*agentSpan
	var byID = map[string]*agentSpan{}
	for _, sp := range spans {
		byID[sp.ID] = sp
	}
	var rec func(id string)
	rec = func(id string) {
		if sp := byID[id]; sp != nil {
			out = append(out, sp)
		}
		for _, k := range kids[id] {
			rec(k.ID)
		}
	}
	rec(root)
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedUS < out[j].StartedUS })
	return out
}

func buildTraceChat(traceID string, roots []*agentSpan, spans []*agentSpan, calls map[string]*Call, feedback, modelToolCalls bool) *AgentTraceChat {
	res := &AgentTraceChat{TraceID: traceID, Messages: []AgentChatMessage{}}
	b := newChatBuilder(spans, calls, feedback, modelToolCalls)
	var cost float64
	hasCost := false
	for _, r := range roots {
		b.walk(r.ID, true)
	}
	members := map[string]bool{}
	for _, r := range roots {
		for _, sp := range subtree(r.ID, spans) {
			members[sp.ID] = true
		}
	}
	for _, sp := range spans {
		if !members[sp.ID] {
			continue
		}
		res.SpanCount++
		switch sp.Op {
		case OpChat:
			res.LLMCount++
		case OpExecuteTool:
			res.ToolCount++
		}
		res.TotalInputTokens += int64(sp.InTok)
		res.TotalOutputTokens += int64(sp.OutTok)
		res.TotalReasoningTokens += int64(sp.ReasonTok)
		res.TotalCacheReadInputTokens += int64(sp.CacheRead)
		res.TotalCacheCreationInputTokens += int64(sp.CacheW)
		if sp.HasCost {
			hasCost = true
			cost += sp.InCost + sp.OutCost
		}
		if res.Provider == nil && sp.Provider != "" {
			res.Provider = ptrIf(sp.Provider)
		}
		if res.AgentName == nil && sp.AgentName != "" {
			res.AgentName, res.AgentVersion = ptrIf(sp.AgentName), ptrIf(sp.AgentVersion)
		}
	}
	if hasCost {
		res.TotalCostUSD = &cost
	}
	res.Messages = b.msgs
	if len(roots) > 0 {
		r := roots[0]
		res.RootSpanID = r.ID
		res.RootSpanName = ptrIf(r.displayName())
		res.StartedAt = microsPtr(r.StartedUS)
		st := r.statusCode()
		var endUS int64
		for _, x := range roots {
			if x.Exception != nil {
				st = "ERROR"
			}
			if x.Ended && x.EndedUS > endUS {
				endUS = x.EndedUS
			}
		}
		res.StatusCode = &st
		if endUS > 0 {
			res.EndedAt = microsPtr(endUS)
			d := (endUS - r.StartedUS + 500) / 1000
			res.TotalDurationMS = &d
		}
		res.UserID = ptrIf(r.UserID)
		if feedback {
			res.Feedback = []AgentChatFeedback{}
			if c := calls[r.ID]; c != nil {
				res.Feedback = chatFeedback(c.Feedback)
			}
		}
	}
	return res
}

func (s *Store) traceBodies(ctx context.Context, pid string, traceIDs []string, feedback bool) ([]*agentSpan, map[string]*Call, error) {
	spans, err := s.loadAgentSpans(ctx, pid, agentSpanFilter{TraceIDs: traceIDs, AllCalls: true})
	if err != nil {
		return nil, nil, err
	}
	lim := 20000
	calls, err := s.ListCalls(ctx, CallsQuery{ProjectID: pid, Filter: &CallsFilter{TraceIDs: traceIDs}, Limit: &lim, IncludeFeedback: feedback,
		SortBy: []SortBy{{Field: "started_at", Direction: "asc"}}})
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[string]*Call, len(calls))
	for _, c := range calls {
		byID[c.ID] = c
	}
	return spans, byID, nil
}

// AgentTraceChatReq is the body of /agents/traces/chat.
type AgentTraceChatReq struct {
	ProjectID             string `json:"project_id"`
	TraceID               string `json:"trace_id"`
	IncludeFeedback       bool   `json:"include_feedback"`
	IncludeModelToolCalls bool   `json:"include_model_tool_calls"`
}

// TraceChat renders one trace as a chat timeline.
func (s *Store) TraceChat(ctx context.Context, req AgentTraceChatReq) (*AgentTraceChat, error) {
	pid, err := refs.NormalizeProject(req.ProjectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	if req.TraceID == "" {
		return nil, Invalidf("trace_id is required")
	}
	spans, calls, err := s.traceBodies(ctx, pid, []string{req.TraceID}, req.IncludeFeedback)
	if err != nil {
		return nil, err
	}
	if len(spans) == 0 {
		return nil, ErrNotFound
	}
	ids := map[string]bool{}
	for _, sp := range spans {
		ids[sp.ID] = true
	}
	var roots []*agentSpan
	for _, sp := range spans {
		if !ids[sp.ParentID] {
			roots = append(roots, sp)
		}
	}
	return buildTraceChat(req.TraceID, roots, spans, calls, req.IncludeFeedback, req.IncludeModelToolCalls), nil
}

// conversationTurns picks a conversation's turns: spans flagged as turns
// (turn_id == id, weft.is_turn), else the spans whose parent is outside
// the conversation (each usually the root of its own trace).
func conversationTurns(spans []*agentSpan) []*agentSpan {
	var flagged, roots []*agentSpan
	ids := map[string]bool{}
	for _, sp := range spans {
		ids[sp.ID] = true
	}
	for _, sp := range spans {
		if sp.IsTurn {
			flagged = append(flagged, sp)
		}
		if !ids[sp.ParentID] {
			roots = append(roots, sp)
		}
	}
	out := roots
	if len(flagged) > 0 {
		out = flagged
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedUS < out[j].StartedUS })
	return out
}

// AgentConversationChatReq is the body of /agents/conversations/chat.
type AgentConversationChatReq struct {
	ProjectID             string `json:"project_id"`
	ConversationID        string `json:"conversation_id"`
	IncludeFeedback       bool   `json:"include_feedback"`
	IncludeModelToolCalls bool   `json:"include_model_tool_calls"`
	Limit                 *int   `json:"limit"`
	Offset                int    `json:"offset"`
}

// AgentConversationChat is the response of /agents/conversations/chat.
type AgentConversationChat struct {
	ConversationID string              `json:"conversation_id"`
	Feedback       []AgentChatFeedback `json:"feedback"`
	HasMore        bool                `json:"has_more"`
	Limit          int                 `json:"limit"`
	Offset         int                 `json:"offset"`
	TotalCostUSD   *float64            `json:"total_cost_usd"`
	TotalTurns     int                 `json:"total_turns"`
	Turns          []*AgentTraceChat   `json:"turns"`
	// Weft extensions.
	ConversationName *string  `json:"conversation_name"`
	AgentNames       []string `json:"agent_names"`
	StartedAt        *string  `json:"started_at"`
	LastActivity     *string  `json:"last_activity"`
	TotalInputTokens int64    `json:"total_input_tokens"`
	TotalOutTokens   int64    `json:"total_output_tokens"`
	SpanCount        int      `json:"span_count"`
	ErrorCount       int      `json:"error_count"`
}

// ConversationChat renders a page of a conversation's turns, newest page
// first (offset counts most-recent turns to skip), each in chronological order.
func (s *Store) ConversationChat(ctx context.Context, req AgentConversationChatReq) (*AgentConversationChat, error) {
	pid, err := refs.NormalizeProject(req.ProjectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	if req.ConversationID == "" {
		return nil, Invalidf("conversation_id is required")
	}
	limit := 50
	if req.Limit != nil && *req.Limit > 0 {
		limit = *req.Limit
	}
	offset := max(req.Offset, 0)
	conv, err := s.loadAgentSpans(ctx, pid, agentSpanFilter{ConversationIDs: []string{req.ConversationID}})
	if err != nil {
		return nil, err
	}
	res := &AgentConversationChat{ConversationID: req.ConversationID, Limit: limit, Offset: offset, Turns: []*AgentTraceChat{}, AgentNames: []string{}}
	if len(conv) == 0 {
		return res, nil
	}
	agg := newSpanAgg()
	for _, sp := range conv {
		agg.add(sp)
	}
	res.TotalCostUSD = agg.cost()
	res.ConversationName = ptrIf(strings.Join(sortedKeys(agg.convNames), ", "))
	res.AgentNames = sortedKeys(agg.agents)
	res.StartedAt, res.LastActivity = microsPtr(agg.first), microsPtr(agg.last)
	res.TotalInputTokens, res.TotalOutTokens = int64(agg.inTok), int64(agg.outTok)
	res.SpanCount, res.ErrorCount = int(agg.spans), int(agg.errors)

	turns := conversationTurns(conv)
	res.TotalTurns = len(turns)
	end := len(turns) - offset
	if end < 0 {
		end = 0
	}
	start := max(end-limit, 0)
	res.HasMore = start > 0
	pg := turns[start:end]
	if req.IncludeFeedback {
		res.Feedback = []AgentChatFeedback{}
	}
	if len(pg) == 0 {
		return res, nil
	}
	traceSet := map[string]bool{}
	var traces []string
	for _, t := range pg {
		if !traceSet[t.TraceID] {
			traceSet[t.TraceID] = true
			traces = append(traces, t.TraceID)
		}
	}
	spans, calls, err := s.traceBodies(ctx, pid, traces, req.IncludeFeedback)
	if err != nil {
		return nil, err
	}
	for _, t := range pg {
		tree := subtree(t.ID, spans)
		var root *agentSpan
		for _, sp := range tree {
			if sp.ID == t.ID {
				root = sp
			}
		}
		if root == nil {
			continue
		}
		res.Turns = append(res.Turns, buildTraceChat(t.TraceID, []*agentSpan{root}, tree, calls, req.IncludeFeedback, req.IncludeModelToolCalls))
	}
	return res, nil
}

// ---------------------------------------------------------------- /agents/conversations/spans

// AgentConversationSpansReq is the body of /agents/conversations/spans.
type AgentConversationSpansReq struct {
	ProjectID       string   `json:"project_id"`
	ConversationIDs []string `json:"conversation_ids"`
	StartedAfter    *string  `json:"started_after"`
	StartedBefore   *string  `json:"started_before"`
}

// ConversationSpan is one scalar span in a conversation minimap.
type ConversationSpan struct {
	DurationMS    int64  `json:"duration_ms"`
	OperationName string `json:"operation_name"`
	SpanID        string `json:"span_id"`
	Status        string `json:"status"`
	TraceID       string `json:"trace_id"`
	// Weft extensions.
	SpanName  string `json:"span_name"`
	StartedAt string `json:"started_at"`
}

// ConversationRating is one numeric rating of a turn or conversation.
type ConversationRating struct {
	Confidence *float64 `json:"confidence"`
	Name       string   `json:"name"`
	Reason     *string  `json:"reason"`
	Value      float64  `json:"value"`
}

// ConversationSpansFeedback groups the tags and ratings of one turn.
type ConversationSpansFeedback struct {
	FeedbackType string               `json:"feedback_type"`
	Ratings      []ConversationRating `json:"ratings"`
	Tags         []string             `json:"tags"`
	TraceID      *string              `json:"trace_id"`
}

// ConversationSpans is one conversation's span sequence.
type ConversationSpans struct {
	ConversationID string                      `json:"conversation_id"`
	Spans          []ConversationSpan          `json:"spans"`
	SpansFeedback  []ConversationSpansFeedback `json:"spans_feedback"`
}

// feedbackRatings maps scorer and annotation feedback onto numeric ratings.
func feedbackRatings(f Feedback) []ConversationRating {
	if sc, ok := signalScoresOf(f.Payload); ok {
		return signalRatings(sc)
	}
	p := asMap(f.Payload)
	var out []ConversationRating
	add := func(name string, v any) {
		if _, isBool := v.(bool); isBool {
			x := 0.0
			if v.(bool) {
				x = 1
			}
			out = append(out, ConversationRating{Name: name, Value: x})
			return
		}
		if x, ok := toFloat(v); ok {
			out = append(out, ConversationRating{Name: name, Value: x})
		}
	}
	switch {
	case strings.HasPrefix(f.FeedbackType, "weft.scorer."):
		name := strings.TrimPrefix(f.FeedbackType, "weft.scorer.")
		o := p["output"]
		if om := asMap(o); om != nil {
			keys := make([]string, 0, len(om))
			for k := range om {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				add(name+"."+k, om[k])
			}
		} else {
			add(name, o)
		}
	case strings.HasPrefix(f.FeedbackType, "weft.annotation."):
		add(strings.TrimPrefix(f.FeedbackType, "weft.annotation."), p["value"])
	}
	return out
}

func feedbackTags(f Feedback) []string {
	if sc, ok := signalScoresOf(f.Payload); ok {
		return sc.ScorerTags
	}
	p := asMap(f.Payload)
	switch {
	case f.FeedbackType == "weft.reaction":
		if e, ok := p["emoji"].(string); ok {
			return []string{e}
		}
	case strings.HasPrefix(f.FeedbackType, "weft.annotation."):
		if s, ok := p["value"].(string); ok && s != "" {
			return []string{strings.TrimPrefix(f.FeedbackType, "weft.annotation.") + ": " + s}
		}
	case f.FeedbackType == "weft.user_feedback" || f.FeedbackType == "weft.signal":
		if l, ok := p["tags"].([]any); ok {
			var out []string
			for _, x := range l {
				if s, ok := x.(string); ok {
					out = append(out, s)
				}
			}
			return out
		}
	}
	return nil
}

// ConversationSpanSequences returns span sequences for conversations.
func (s *Store) ConversationSpanSequences(ctx context.Context, req AgentConversationSpansReq) ([]ConversationSpans, error) {
	pid, err := refs.NormalizeProject(req.ProjectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	if len(req.ConversationIDs) > 1000 {
		return nil, Invalidf("too many conversation ids (max 1000)")
	}
	out := make([]ConversationSpans, 0, len(req.ConversationIDs))
	if len(req.ConversationIDs) == 0 {
		return out, nil
	}
	a, b, err := timeRange(req.StartedAfter, req.StartedBefore)
	if err != nil {
		return nil, err
	}
	spans, err := s.loadAgentSpans(ctx, pid, agentSpanFilter{ConversationIDs: req.ConversationIDs, After: a, Before: b})
	if err != nil {
		return nil, err
	}
	byConv := map[string][]*agentSpan{}
	for _, sp := range spans {
		byConv[sp.ConvID] = append(byConv[sp.ConvID], sp)
	}
	// Feedback on each conversation's turns.
	turnRef := map[string][2]string{} // call ref -> (conversation, trace)
	var refList []string
	for conv, ss := range byConv {
		for _, t := range conversationTurns(ss) {
			r := refs.SpanRef(pid, t.ID)
			turnRef[r] = [2]string{conv, t.TraceID}
			refList = append(refList, r)
		}
	}
	var fbs []Feedback
	for start := 0; start < len(refList); start += 500 {
		part, err := s.feedbackForRefs(ctx, pid, refList[start:min(start+500, len(refList))])
		if err != nil {
			return nil, err
		}
		fbs = append(fbs, part...)
	}
	type fkey struct{ conv, trace, typ string }
	grouped := map[fkey]*ConversationSpansFeedback{}
	var order []fkey
	for _, f := range fbs {
		ct := turnRef[f.Ref]
		typ := "weft.user_feedback"
		if strings.HasPrefix(f.FeedbackType, "weft.scorer.") || f.FeedbackType == "weft.signal" {
			typ = "weft.signal"
		}
		ratings, tags := feedbackRatings(f), feedbackTags(f)
		if len(ratings) == 0 && len(tags) == 0 {
			continue
		}
		k := fkey{ct[0], ct[1], typ}
		g := grouped[k]
		if g == nil {
			trace := ct[1]
			g = &ConversationSpansFeedback{FeedbackType: typ, Ratings: []ConversationRating{}, Tags: []string{}, TraceID: &trace}
			grouped[k] = g
			order = append(order, k)
		}
		g.Ratings = append(g.Ratings, ratings...)
		g.Tags = append(g.Tags, tags...)
	}
	for _, conv := range req.ConversationIDs {
		cs := ConversationSpans{ConversationID: conv, Spans: []ConversationSpan{}, SpansFeedback: []ConversationSpansFeedback{}}
		for _, sp := range byConv[conv] {
			d, _ := sp.durationMS()
			cs.Spans = append(cs.Spans, ConversationSpan{DurationMS: int64(d + 0.5), OperationName: sp.Op, SpanID: sp.ID,
				Status: sp.statusCode(), TraceID: sp.TraceID, SpanName: sp.displayName(), StartedAt: FormatMicros(sp.StartedUS)})
		}
		for _, k := range order {
			if k.conv == conv {
				cs.SpansFeedback = append(cs.SpansFeedback, *grouped[k])
			}
		}
		out = append(out, cs)
	}
	return out, nil
}

// ---------------------------------------------------------------- group previews

func (s *Store) fillGroupPreviews(ctx context.Context, pid string, groups []*AgentSpanGroup) error {
	const n = 4
	need := map[string]bool{}
	type cand struct{ first, last []*agentSpan }
	cands := make([]cand, len(groups))
	for i, g := range groups {
		ms := g.agg.members // ordered by start
		var c cand
		for _, sp := range ms {
			if len(c.first) >= n {
				break
			}
			c.first = append(c.first, sp)
		}
		byEnd := append([]*agentSpan(nil), ms...)
		sort.SliceStable(byEnd, func(a, b int) bool { return byEnd[a].EndedUS > byEnd[b].EndedUS })
		for _, sp := range byEnd {
			if len(c.last) >= n {
				break
			}
			c.last = append(c.last, sp)
		}
		for _, sp := range append(c.first, c.last...) {
			need[sp.ID] = true
		}
		cands[i] = c
	}
	ids := make([]string, 0, len(need))
	for id := range need {
		ids = append(ids, id)
	}
	calls, err := s.callsByID(ctx, pid, ids)
	if err != nil {
		return err
	}
	for i, g := range groups {
		for _, sp := range cands[i].first {
			if c := calls[sp.ID]; c != nil {
				t, m := lastUserText(InputMessages(c.Inputs))
				if m == nil && sp.ParentID == "" {
					t = InputText(c.Inputs)
				}
				if t != "" {
					g.FirstMessage = &AgentMessagePreview{Role: "user_message", Text: truncateText(t, 200)}
					break
				}
			}
		}
		for _, sp := range cands[i].last {
			if c := calls[sp.ID]; c != nil {
				t, _ := assistantText(OutputMessages(c.Output))
				if t != "" {
					g.LastMessage = &AgentMessagePreview{Role: "assistant_message", Text: truncateText(t, 200)}
					break
				}
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------- /agents/search

// AgentSearchReq is the body of /agents/search.
type AgentSearchReq struct {
	ProjectID       string   `json:"project_id"`
	AgentName       *string  `json:"agent_name"`
	ConversationID  *string  `json:"conversation_id"`
	Limit           *int     `json:"limit"`
	Offset          int      `json:"offset"`
	ProviderName    *string  `json:"provider_name"`
	Query           string   `json:"query"`
	RequestModel    *string  `json:"request_model"`
	Roles           []string `json:"roles"`
	StartedAfter    *string  `json:"started_after"`
	StartedBefore   *string  `json:"started_before"`
	TraceID         *string  `json:"trace_id"`
	TruncateContent *bool    `json:"truncate_content"`
}

// AgentSearchMessage is one matching message.
type AgentSearchMessage struct {
	ContentDigest  string `json:"content_digest"`
	ContentPreview string `json:"content_preview"`
	Role           string `json:"role"`
	SpanID         string `json:"span_id"`
	StartedAt      string `json:"started_at"`
	TraceID        string `json:"trace_id"`
}

// AgentSearchResult is one conversation with matching messages.
type AgentSearchResult struct {
	AgentName        string               `json:"agent_name"`
	ConversationID   string               `json:"conversation_id"`
	ConversationName string               `json:"conversation_name"`
	LastActivity     string               `json:"last_activity"`
	MatchedMessages  []AgentSearchMessage `json:"matched_messages"`
}

// searchRole maps a message onto the search roles: tool responses are
// tool_result and assistant messages that only request tools are tool_call.
func searchRole(m ChatMsg) string {
	switch {
	case m.Role == "tool":
		return "tool_result"
	case m.Role == "assistant" && m.Text == "" && len(m.ToolCalls) > 0:
		return "tool_call"
	}
	return m.Role
}

// compactArgs re-encodes JSON arguments compactly so equal calls compare equal.
func compactArgs(s string) string {
	if v := decodeJSON(s); v != nil {
		return jsonString(v)
	}
	return s
}

func snippet(text, q string, n int) string {
	text = strings.Join(strings.Fields(text), " ")
	r := []rune(text)
	if len(r) <= n {
		return text
	}
	i := 0
	if q != "" {
		if idx := strings.Index(strings.ToLower(text), strings.ToLower(q)); idx >= 0 {
			i = len([]rune(text[:idx]))
		}
	}
	start := max(i-n/3, 0)
	end := min(start+n, len(r))
	out := string(r[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(r) {
		out += "…"
	}
	return out
}

// SearchAgentMessages searches message content across conversations.
func (s *Store) SearchAgentMessages(ctx context.Context, req AgentSearchReq) ([]AgentSearchResult, int, error) {
	pid, err := refs.NormalizeProject(req.ProjectID)
	if err != nil {
		return nil, 0, &InvalidError{Msg: err.Error()}
	}
	after, before, err := timeRange(req.StartedAfter, req.StartedBefore)
	if err != nil {
		return nil, 0, err
	}
	where := []string{"s.project_id = ?", "c.deleted_at IS NULL", "s.conversation_id != ''"}
	args := []any{pid}
	for _, f := range []struct {
		col string
		v   *string
	}{{"s.agent_name", req.AgentName}, {"s.conversation_id", req.ConversationID}, {"s.provider_name", req.ProviderName},
		{"s.request_model", req.RequestModel}, {"s.trace_id", req.TraceID}} {
		if f.v != nil && *f.v != "" {
			where = append(where, f.col+" = ?")
			args = append(args, *f.v)
		}
	}
	if after > 0 {
		where = append(where, "c.started_at >= ?")
		args = append(args, after)
	}
	if before > 0 {
		where = append(where, "c.started_at < ?")
		args = append(args, before)
	}
	q := strings.TrimSpace(req.Query)
	if q != "" {
		where = append(where, "(instr(lower(c.inputs), lower(?)) > 0 OR instr(lower(COALESCE(c.output, '')), lower(?)) > 0)")
		args = append(args, q, q)
	}
	rows, err := s.r.QueryContext(ctx, `SELECT s.id, s.trace_id, s.conversation_id, s.conversation_name, s.agent_name, s.operation_name, s.tool_name,
			c.started_at, c.ended_at, c.inputs, c.output
		FROM agent_spans s JOIN calls c ON c.project_id = s.project_id AND c.id = s.id
		WHERE `+strings.Join(where, " AND ")+` ORDER BY c.started_at LIMIT 50000`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	roles := map[string]bool{}
	for _, r := range req.Roles {
		roles[r] = true
	}
	truncate := req.TruncateContent == nil || *req.TruncateContent
	lq := strings.ToLower(q)
	type convAcc struct {
		res  *AgentSearchResult
		seen map[string]bool
		last int64
	}
	convs := map[string]*convAcc{}
	for rows.Next() {
		var id, trace, conv, convName, agent, op, toolName, inputs string
		var started int64
		var ended *int64
		var output *string
		if err := rows.Scan(&id, &trace, &conv, &convName, &agent, &op, &toolName, &started, &ended, &inputs, &output); err != nil {
			return nil, 0, err
		}
		type msg struct{ role, content string }
		var msgs []msg
		in := decodeMap(inputs)
		var out any
		if output != nil {
			out = decodeJSON(*output)
		}
		if op == OpExecuteTool {
			msgs = append(msgs, msg{"tool_call", toolName + "(" + jsonString(in) + ")"})
			if out != nil {
				msgs = append(msgs, msg{"tool_result", OutputText(out)})
			}
		} else {
			for _, m := range append(InputMessages(in), OutputMessages(out)...) {
				role := searchRole(m)
				if role == "tool_call" {
					for _, tc := range m.ToolCalls {
						msgs = append(msgs, msg{role, tc.Name + "(" + compactArgs(tc.Arguments) + ")"})
					}
					continue
				}
				msgs = append(msgs, msg{role, messageContent(m)})
			}
		}
		acc := convs[conv]
		for _, m := range msgs {
			if m.content == "" || (len(roles) > 0 && !roles[m.role]) {
				continue
			}
			if lq != "" && !strings.Contains(strings.ToLower(m.content), lq) {
				continue
			}
			sum := sha256.Sum256([]byte(m.content))
			digest := hex.EncodeToString(sum[:])
			if acc == nil {
				acc = &convAcc{res: &AgentSearchResult{ConversationID: conv, MatchedMessages: []AgentSearchMessage{}}, seen: map[string]bool{}}
				convs[conv] = acc
			}
			if acc.seen[digest] || len(acc.res.MatchedMessages) >= 50 {
				continue
			}
			acc.seen[digest] = true
			preview := m.content
			if truncate {
				preview = snippet(m.content, q, 200)
			}
			acc.res.MatchedMessages = append(acc.res.MatchedMessages, AgentSearchMessage{ContentDigest: digest, ContentPreview: preview,
				Role: m.role, SpanID: id, StartedAt: FormatMicros(started), TraceID: trace})
		}
		if acc != nil {
			if acc.res.AgentName == "" {
				acc.res.AgentName = agent
			}
			if acc.res.ConversationName == "" {
				acc.res.ConversationName = convName
			}
			last := started
			if ended != nil && *ended > last {
				last = *ended
			}
			if last > acc.last {
				acc.last = last
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	out := make([]AgentSearchResult, 0, len(convs))
	for _, a := range convs {
		a.res.LastActivity = FormatMicros(a.last)
		out = append(out, *a.res)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastActivity != out[j].LastActivity {
			return out[i].LastActivity > out[j].LastActivity
		}
		return out[i].ConversationID < out[j].ConversationID
	})
	return page(out, req.Limit, req.Offset, 20), len(out), nil
}
