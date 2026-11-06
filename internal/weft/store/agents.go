package store

// Agents: a span index over calls that powers the /agents/* API.
//
// Every call gets one agent_spans row, written by an ingest hook inside the
// same transaction as the call. The row holds the semantic fields derived
// from the call (operation, agent, conversation, tool, model, provider), so
// agent and conversation queries never re-parse call attributes. Agent and
// conversation identity is inherited from the nearest ancestor that sets it,
// and re-propagated to descendants when a parent arrives after its children
// (OTLP exporters send children first).
//
// Usage, cost, status and latency are read from the calls table at query
// time, because a call's own usage depends on its roll-up, which changes as
// descendants arrive.

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

func init() {
	registerSchema(`
CREATE TABLE IF NOT EXISTS agent_spans (
	project_id TEXT NOT NULL,
	id TEXT NOT NULL,
	trace_id TEXT NOT NULL DEFAULT '',
	parent_id TEXT,
	operation_name TEXT NOT NULL DEFAULT '',
	span_name TEXT NOT NULL DEFAULT '',
	own_agent INTEGER NOT NULL DEFAULT 0,
	agent_name TEXT NOT NULL DEFAULT '',
	agent_id TEXT NOT NULL DEFAULT '',
	agent_version TEXT NOT NULL DEFAULT '',
	agent_description TEXT NOT NULL DEFAULT '',
	own_conversation INTEGER NOT NULL DEFAULT 0,
	conversation_id TEXT NOT NULL DEFAULT '',
	conversation_name TEXT NOT NULL DEFAULT '',
	is_turn INTEGER NOT NULL DEFAULT 0,
	tool_name TEXT NOT NULL DEFAULT '',
	tool_call_id TEXT NOT NULL DEFAULT '',
	request_model TEXT NOT NULL DEFAULT '',
	response_model TEXT NOT NULL DEFAULT '',
	provider_name TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (project_id, id)
);
CREATE INDEX IF NOT EXISTS agent_spans_parent ON agent_spans(project_id, parent_id);
CREATE INDEX IF NOT EXISTS agent_spans_agent ON agent_spans(project_id, agent_name);
CREATE INDEX IF NOT EXISTS agent_spans_conv ON agent_spans(project_id, conversation_id);
CREATE INDEX IF NOT EXISTS agent_spans_trace ON agent_spans(project_id, trace_id);
`)
	registerMigration(backfillAgentSpans)
	registerCallHook(indexAgentSpan)
}

// Normalised operation names (gen_ai.operation.name values where one exists).
const (
	OpInvokeAgent = "invoke_agent"
	OpChat        = "chat"
	OpExecuteTool = "execute_tool"
	OpEmbeddings  = "embeddings"
	OpRetrieval   = "retrieval"
	OpChain       = "chain"
)

// SpanInfo is what the index derives from one call.
type SpanInfo struct {
	Operation        string
	SpanName         string
	OwnAgent         bool
	AgentName        string
	AgentID          string
	AgentVersion     string
	AgentDescription string
	ConversationID   string
	ConversationName string
	IsTurn           bool
	ToolName         string
	ToolCallID       string
	RequestModel     string
	ResponseModel    string
	ProviderName     string
}

// spanAttrs looks attributes up in OTel span attributes first, then in the
// call's own attributes (flat dotted keys or nested maps).
type spanAttrs struct {
	otel map[string]any
	call map[string]any
}

func newSpanAttrs(attributes map[string]any) spanAttrs {
	a := spanAttrs{call: attributes}
	if o, ok := attributes["otel"].(map[string]any); ok {
		a.otel, _ = o["attributes"].(map[string]any)
	}
	return a
}

func (a spanAttrs) get(key string) any {
	if v, ok := a.otel[key]; ok && v != nil {
		return v
	}
	if v, ok := a.call[key]; ok && v != nil {
		return v
	}
	if strings.Contains(key, ".") {
		if v, ok := getPath(a.call, strings.Split(key, ".")); ok {
			return v
		}
	}
	return nil
}

func (a spanAttrs) str(keys ...string) string {
	for _, k := range keys {
		switch v := a.get(k).(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		case json.Number:
			return v.String()
		}
	}
	return ""
}

func (a spanAttrs) truthy(keys ...string) bool {
	for _, k := range keys {
		switch v := a.get(k).(type) {
		case bool:
			if v {
				return true
			}
		case string:
			if strings.EqualFold(v, "true") || v == "1" {
				return true
			}
		case json.Number:
			if v.String() == "1" {
				return true
			}
		}
	}
	return false
}

// kindAliases maps span-kind vocabularies (weft.span_kind, OpenInference,
// Traceloop, MLflow) onto normalised operation names.
var kindAliases = map[string]string{
	"agent": OpInvokeAgent, "invoke_agent": OpInvokeAgent, "create_agent": OpInvokeAgent, "subagent": OpInvokeAgent, "sub_agent": OpInvokeAgent,
	"turn": OpInvokeAgent,
	"llm":  OpChat, "chat": OpChat, "chat_model": OpChat, "text_completion": OpChat, "generate_content": OpChat, "completion": OpChat,
	"tool": OpExecuteTool, "execute_tool": OpExecuteTool, "function": OpExecuteTool,
	"embedding": OpEmbeddings, "embeddings": OpEmbeddings,
	"retriever": OpRetrieval, "retrieval": OpRetrieval, "retrieve": OpRetrieval,
	"chain": OpChain, "workflow": OpChain, "task": OpChain, "parser": OpChain, "search": OpRetrieval,
	"reranker": "rerank", "rerank": "rerank", "guardrail": "guardrail", "evaluator": "evaluator", "prompt": "prompt",
}

func normKind(s string) string {
	s = strings.ToLower(strings.Trim(strings.TrimSpace(s), `"`)) // MLflow JSON-encodes attribute values
	if s == "" || s == "unknown" || s == "unspecified" {
		return ""
	}
	if k, ok := kindAliases[s]; ok {
		return k
	}
	return s
}

// spanNamePrefixes classify spans by name when no attribute says what they are.
var spanNamePrefixes = []struct{ prefix, op string }{
	{"invoke_agent", OpInvokeAgent}, {"create_agent", OpInvokeAgent}, {"agent run", OpInvokeAgent},
	{"execute_tool", OpExecuteTool}, {"running tool", OpExecuteTool}, {"running tools", OpChain},
	{"chat ", OpChat}, {"call_llm", OpChat}, {"text_completion", OpChat}, {"generate_content", OpChat},
	{"embeddings", OpEmbeddings},
}

// llmOpNames are SDK op base names (or prefixes) that are LLM calls.
var llmOpNames = []string{"openai.chat.completions.create", "openai.responses.create", "openai.completions.create",
	"anthropic.messages.create", "anthropic.Messages.create", "anthropic.messages.stream", "anthropic.Messages.stream",
	"weft.completions.create", "google.genai", "gemini.", "litellm.completion", "mistral.chat", "groq.chat", "bedrock.converse"}

// DeriveSpan classifies a call for the agents index. attributes are the
// call's attributes (OTLP spans keep raw span attributes under otel), summary
// its stored summary (used only to recognise LLM calls by their usage).
func DeriveSpan(opName, displayName string, threadID, turnID *string, id string, attributes, inputs, summary map[string]any) SpanInfo {
	a := newSpanAttrs(attributes)
	base := OpBaseName(opName)
	spanName := base
	if o, ok := attributes["otel"].(map[string]any); ok {
		if n, ok := o["span_name"].(string); ok && n != "" {
			spanName = n
		}
	}
	var d SpanInfo
	d.SpanName = spanName

	// Operation.
	op := normKind(a.str("gen_ai.operation.name"))
	if op == "" {
		op = normKind(a.str("weft.span_kind", "openinference.span.kind", "traceloop.span.kind", "mlflow.spanType"))
	}
	if op == "" {
		vid := a.str("ai.operationId", "operation.name")
		if vid == "" && strings.HasPrefix(spanName, "ai.") {
			vid = spanName
		}
		if f := strings.Fields(vid); len(f) > 0 { // operation.name is "<operationId> <functionId>"
			op = vercelOperation(f[0])
		}
	}
	if op == "" && a.str("gcp.vertex.agent.llm_request") != "" {
		op = OpChat
	}
	if op == "" && a.str("gcp.vertex.agent.tool_call_args") != "" {
		op = OpExecuteTool
	}
	if op == "" && a.str("llm.request.type") != "" {
		op = normKind(a.str("llm.request.type"))
	}
	lname := strings.ToLower(spanName)
	if op == "" {
		for _, p := range spanNamePrefixes {
			if strings.HasPrefix(lname, p.prefix) {
				op = p.op
				break
			}
		}
	}
	if op == "" {
		for _, p := range llmOpNames {
			if strings.HasPrefix(base, p) || strings.HasPrefix(strings.ToLower(base), strings.ToLower(p)) {
				op = OpChat
				break
			}
		}
	}
	if op == "" && a.otel == nil && hasLLMUsage(summary) && hasMessages(inputs) {
		op = OpChat
	}
	d.Operation = op

	// Agent identity.
	d.AgentName = a.str("gen_ai.agent.name", "weft.agent.name", "weft.agent_name", "agent.name", "agent_name",
		"gcp.vertex.agent.agent_name")
	if d.AgentName == "" && normKind(a.str("traceloop.span.kind")) == OpInvokeAgent {
		d.AgentName = a.str("traceloop.entity.name")
	}
	if d.AgentName == "" && op == OpInvokeAgent {
		d.AgentName = nameAfterPrefix(spanName, "invoke_agent", "create_agent")
		if d.AgentName == "" {
			if displayName != "" {
				d.AgentName = displayName
			} else if !strings.HasPrefix(lname, "agent run") && !strings.HasPrefix(lname, "invoke_agent") {
				d.AgentName = base
			}
		}
	}
	d.AgentID = a.str("gen_ai.agent.id", "weft.agent.id")
	d.AgentVersion = a.str("gen_ai.agent.version", "weft.agent.version", "weft.agent_version")
	d.AgentDescription = a.str("gen_ai.agent.description", "weft.agent.description")
	d.OwnAgent = d.AgentName != ""
	if !d.OwnAgent {
		d.AgentID, d.AgentVersion, d.AgentDescription = "", "", ""
	}

	// Conversation and turn.
	if threadID != nil {
		// Trimmed as AgentShield detections trim it (detAttrStr), so a padded
		// session id (from an old engine, or forged) names one session.
		d.ConversationID = strings.TrimSpace(*threadID)
	}
	if d.ConversationID == "" {
		d.ConversationID = a.str(append([]string{"gen_ai.conversation.id", "gcp.vertex.agent.session_id", "session.id"}, AttrKeys(AttrSessionID)...)...)
	}
	d.ConversationName = a.str("gen_ai.conversation.name", "weft.conversation.name")
	d.IsTurn = (turnID != nil && *turnID == id) || a.truthy("weft.is_turn")
	if isReplaySpan(a) && isVerdictSpan(attributes) {
		// A replayed evaluation belongs to no session and no agent
		// (detections_otel.go); it stays visible in the raw span views.
		d.ConversationID, d.ConversationName, d.IsTurn = "", "", false
		d.AgentName, d.AgentID, d.AgentVersion, d.AgentDescription, d.OwnAgent = "", "", "", "", false
	}

	// Tool.
	d.ToolName = a.str("gen_ai.tool.name", "tool.name", "ai.toolCall.name", "gcp.vertex.agent.tool_name")
	if d.ToolName == "" && op == OpExecuteTool {
		if normKind(a.str("traceloop.span.kind")) == OpExecuteTool {
			d.ToolName = a.str("traceloop.entity.name")
		}
		if d.ToolName == "" {
			d.ToolName = nameAfterPrefix(spanName, "execute_tool", "running tool")
		}
		if d.ToolName == "" {
			if displayName != "" {
				d.ToolName = displayName
			} else {
				d.ToolName = base
			}
		}
	}
	d.ToolCallID = a.str("gen_ai.tool.call.id", "ai.toolCall.id", "tool_call.id", "tool_call_id")

	// Model and provider.
	d.RequestModel = a.str("gen_ai.request.model", "ai.model.id", "llm.model_name", "gen_ai.response.model", "model_name")
	if d.RequestModel == "" && op == OpChat {
		if m, ok := inputs["model"].(string); ok {
			d.RequestModel = m
		}
	}
	d.ResponseModel = a.str("gen_ai.response.model", "ai.response.model")
	d.ProviderName = a.str("gen_ai.provider.name", "gen_ai.system", "ai.model.provider", "llm.provider", "llm.system")
	if d.ProviderName == "" && op == OpChat {
		if i := strings.IndexByte(base, '.'); i > 0 {
			d.ProviderName = strings.ToLower(base[:i])
		}
	}
	if i := strings.IndexByte(d.ProviderName, '.'); i > 0 { // Vercel: "openai.chat"
		d.ProviderName = d.ProviderName[:i]
	}
	return d
}

// vercelOperation classifies Vercel AI SDK spans by ai.operationId.
func vercelOperation(id string) string {
	switch {
	case id == "":
		return ""
	case strings.HasSuffix(id, ".doGenerate"), strings.HasSuffix(id, ".doStream"):
		if strings.HasPrefix(id, "ai.embed") {
			return OpEmbeddings
		}
		return OpChat
	case strings.HasSuffix(id, ".doEmbed"), id == "ai.embed", id == "ai.embedMany":
		return OpEmbeddings
	case id == "ai.toolCall":
		return OpExecuteTool
	case strings.HasPrefix(id, "ai.generate"), strings.HasPrefix(id, "ai.stream"):
		return OpChain
	}
	return ""
}

func nameAfterPrefix(name string, prefixes ...string) string {
	for _, p := range prefixes {
		if len(name) > len(p) && strings.EqualFold(name[:len(p)], p) && name[len(p)] == ' ' {
			return strings.TrimSpace(name[len(p)+1:])
		}
	}
	return ""
}

func hasLLMUsage(summary map[string]any) bool {
	u, ok := summary["usage"].(map[string]any)
	return ok && len(u) > 0
}

func hasMessages(inputs map[string]any) bool {
	for _, k := range []string{"messages", "input", "contents"} {
		if l, ok := inputs[k].([]any); ok && len(l) > 0 {
			if m, ok := l[0].(map[string]any); ok && m["role"] != nil {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------- indexing

type dbtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type inherited struct {
	agentName, agentID, agentVersion, agentDescription string
	conversationID, conversationName                   string
}

func indexAgentSpanTx(ctx context.Context, tx dbtx, projectID, id string) error {
	var opName, traceID, attrs, inputs, summary string
	var display, parent, thread, turn sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT op_name, display_name, trace_id, parent_id, thread_id, turn_id, attributes, inputs, summary
		FROM calls WHERE project_id = ? AND id = ?`, projectID, id).Scan(&opName, &display, &traceID, &parent, &thread, &turn, &attrs, &inputs, &summary)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if opName == "" { // end arrived before start; the start will index it
		return nil
	}
	attrMap := decodeMap(attrs)
	d := DeriveSpan(opName, display.String, strPtr(thread), strPtr(turn), id, attrMap, decodeMap(inputs), decodeMap(summary))
	if err := indexEvalLinksTx(ctx, tx, projectID, id, opName, strPtr(parent), attrMap); err != nil {
		return err
	}

	eff := inherited{d.AgentName, d.AgentID, d.AgentVersion, d.AgentDescription, d.ConversationID, d.ConversationName}
	if parent.Valid && parent.String != "" && (!d.OwnAgent || d.ConversationID == "") {
		var p inherited
		err := tx.QueryRowContext(ctx, `SELECT agent_name, agent_id, agent_version, agent_description, conversation_id, conversation_name
			FROM agent_spans WHERE project_id = ? AND id = ?`, projectID, parent.String).
			Scan(&p.agentName, &p.agentID, &p.agentVersion, &p.agentDescription, &p.conversationID, &p.conversationName)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if !d.OwnAgent {
			eff.agentName, eff.agentID, eff.agentVersion, eff.agentDescription = p.agentName, p.agentID, p.agentVersion, p.agentDescription
		}
		if d.ConversationID == "" {
			eff.conversationID = p.conversationID
			if eff.conversationName == "" {
				eff.conversationName = p.conversationName
			}
		}
	}
	// A session titled by its event log (conversation_titles) shows that
	// title unless the span names its conversation itself.
	if eff.conversationID != "" && eff.conversationName == "" {
		if eff.conversationName, err = conversationTitle(ctx, tx, projectID, eff.conversationID); err != nil {
			return err
		}
	}
	var prev inherited
	hadPrev := true
	err = tx.QueryRowContext(ctx, `SELECT agent_name, agent_id, agent_version, agent_description, conversation_id, conversation_name
		FROM agent_spans WHERE project_id = ? AND id = ?`, projectID, id).
		Scan(&prev.agentName, &prev.agentID, &prev.agentVersion, &prev.agentDescription, &prev.conversationID, &prev.conversationName)
	if err == sql.ErrNoRows {
		hadPrev = false
	} else if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_spans(project_id, id, trace_id, parent_id, operation_name, span_name, own_agent,
			agent_name, agent_id, agent_version, agent_description, own_conversation, conversation_id, conversation_name, is_turn,
			tool_name, tool_call_id, request_model, response_model, provider_name)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(project_id, id) DO UPDATE SET trace_id = excluded.trace_id, parent_id = excluded.parent_id,
			operation_name = excluded.operation_name, span_name = excluded.span_name, own_agent = excluded.own_agent,
			agent_name = excluded.agent_name, agent_id = excluded.agent_id, agent_version = excluded.agent_version,
			agent_description = excluded.agent_description, own_conversation = excluded.own_conversation,
			conversation_id = excluded.conversation_id, conversation_name = excluded.conversation_name, is_turn = excluded.is_turn,
			tool_name = excluded.tool_name, tool_call_id = excluded.tool_call_id, request_model = excluded.request_model,
			response_model = excluded.response_model, provider_name = excluded.provider_name`,
		projectID, id, traceID, nullStr(strPtr(parent)), d.Operation, d.SpanName, d.OwnAgent,
		eff.agentName, eff.agentID, eff.agentVersion, eff.agentDescription, d.ConversationID != "", eff.conversationID, eff.conversationName, d.IsTurn,
		d.ToolName, d.ToolCallID, d.RequestModel, d.ResponseModel, d.ProviderName)
	if err != nil {
		return err
	}
	// Push changed identity down to descendants that inherit it.
	agentChanged := eff.agentName != prev.agentName || eff.agentID != prev.agentID || eff.agentVersion != prev.agentVersion || eff.agentDescription != prev.agentDescription
	if agentChanged && (hadPrev || eff.agentName != "") {
		if _, err := tx.ExecContext(ctx, `WITH RECURSIVE d(id) AS (
				SELECT id FROM agent_spans WHERE project_id = ? AND parent_id = ? AND own_agent = 0
				UNION SELECT s.id FROM agent_spans s JOIN d ON s.parent_id = d.id WHERE s.project_id = ? AND s.own_agent = 0)
			UPDATE agent_spans SET agent_name = ?, agent_id = ?, agent_version = ?, agent_description = ?
			WHERE project_id = ? AND id IN (SELECT id FROM d)`,
			projectID, id, projectID, eff.agentName, eff.agentID, eff.agentVersion, eff.agentDescription, projectID); err != nil {
			return err
		}
	}
	convChanged := eff.conversationID != prev.conversationID || eff.conversationName != prev.conversationName
	if convChanged && (hadPrev || eff.conversationID != "") {
		if _, err := tx.ExecContext(ctx, `WITH RECURSIVE d(id) AS (
				SELECT id FROM agent_spans WHERE project_id = ? AND parent_id = ? AND own_conversation = 0
				UNION SELECT s.id FROM agent_spans s JOIN d ON s.parent_id = d.id WHERE s.project_id = ? AND s.own_conversation = 0)
			UPDATE agent_spans SET conversation_id = ?, conversation_name = ?
			WHERE project_id = ? AND id IN (SELECT id FROM d)`,
			projectID, id, projectID, eff.conversationID, eff.conversationName, projectID); err != nil {
			return err
		}
	}
	return nil
}

func indexAgentSpan(ctx context.Context, tx *sql.Tx, projectID, id string) error {
	return indexAgentSpanTx(ctx, tx, projectID, id)
}

// backfillAgentSpans indexes calls written before the agents index existed.
func backfillAgentSpans(db *sql.DB) error {
	var n int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_spans`).Scan(&n); err != nil || n > 0 {
		return err
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM calls`).Scan(&n); err != nil || n == 0 {
		return err
	}
	rows, err := db.Query(`SELECT project_id, id FROM calls ORDER BY project_id, started_at`)
	if err != nil {
		return err
	}
	var keys [][2]string
	for rows.Next() {
		var p, id string
		if err := rows.Scan(&p, &id); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, [2]string{p, id})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if err := indexAgentSpan(ctx, tx, k[0], k[1]); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// ReindexAgentSpans rebuilds the agents index for a project (tests, repair).
func (s *Store) ReindexAgentSpans(ctx context.Context, projectID string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM agent_spans WHERE project_id = ?`, projectID); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id FROM calls WHERE project_id = ? ORDER BY started_at`, projectID)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		for _, id := range ids {
			if err := indexAgentSpan(ctx, tx, projectID, id); err != nil {
				return err
			}
		}
		return nil
	})
}
