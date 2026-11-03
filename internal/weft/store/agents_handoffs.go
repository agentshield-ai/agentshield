package store

// Agent graph index: teams, hand-offs and reasoning steps
// (docs/weft/DESIGN.md §2, §2.1, §2.2).
//
// The agent-graph call hook runs after the agents index hook (agents.go;
// init order follows file names) and does two things:
//
//  1. Per span, it stores what can be read from the span alone: team and
//     hand-off hints (agent_span_meta), OTel span links (agent_span_links) and
//     reasoning steps (reasoning_steps).
//  2. It marks the span's trace for derivation. Deriving a trace rebuilds
//     its agent_handoffs rows from the spans indexed so far and refreshes the
//     reasoning steps' agent, session and next_span_id. Batch ingest
//     (UpsertBatch, UpsertComplete) derives each touched trace once, at the
//     end of the transaction; single writes derive immediately.
//
// Because every derivation starts from the stored spans, the order in which
// spans arrive does not matter: a child that arrives before its parent gets
// its edge when the parent's arrival re-derives the trace. Traces whose
// spans link to spans of a re-derived trace are re-derived too.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

func init() {
	registerSchema(`
CREATE TABLE IF NOT EXISTS agent_span_meta (
	project_id TEXT NOT NULL,
	id TEXT NOT NULL,
	trace_id TEXT NOT NULL DEFAULT '',
	team_id TEXT NOT NULL DEFAULT '',
	team_name TEXT NOT NULL DEFAULT '',
	handoff_to TEXT NOT NULL DEFAULT '',
	handoff_from TEXT NOT NULL DEFAULT '',
	handoff_kind TEXT NOT NULL DEFAULT '',
	handoff_source TEXT NOT NULL DEFAULT '',
	graph_node TEXT NOT NULL DEFAULT '',
	graph_parent TEXT NOT NULL DEFAULT '',
	graph_step INTEGER NOT NULL DEFAULT 0,
	out_tool_call_ids TEXT NOT NULL DEFAULT '',
	verdict INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (project_id, id)
);
CREATE INDEX IF NOT EXISTS agent_span_meta_trace ON agent_span_meta(project_id, trace_id);
CREATE INDEX IF NOT EXISTS agent_span_meta_team ON agent_span_meta(project_id, team_id);
CREATE TABLE IF NOT EXISTS agent_span_links (
	project_id TEXT NOT NULL,
	span_id TEXT NOT NULL,
	trace_id TEXT NOT NULL DEFAULT '',
	linked_trace_id TEXT NOT NULL DEFAULT '',
	linked_span_id TEXT NOT NULL,
	PRIMARY KEY (project_id, span_id, linked_span_id)
);
CREATE INDEX IF NOT EXISTS agent_span_links_trace ON agent_span_links(project_id, trace_id);
CREATE INDEX IF NOT EXISTS agent_span_links_target ON agent_span_links(project_id, linked_trace_id);
CREATE TABLE IF NOT EXISTS agent_handoffs (
	project_id TEXT NOT NULL,
	id TEXT NOT NULL,
	session_id TEXT NOT NULL DEFAULT '',
	trace_id TEXT NOT NULL DEFAULT '',
	source_span_id TEXT NOT NULL DEFAULT '',
	from_span_id TEXT NOT NULL DEFAULT '',
	to_span_id TEXT NOT NULL DEFAULT '',
	from_agent TEXT NOT NULL DEFAULT '',
	to_agent TEXT NOT NULL DEFAULT '',
	kind TEXT NOT NULL DEFAULT '',
	source TEXT NOT NULL DEFAULT '',
	started_at INTEGER,
	ended_at INTEGER,
	status TEXT NOT NULL DEFAULT '',
	input_preview TEXT NOT NULL DEFAULT '',
	output_preview TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (project_id, id)
);
CREATE INDEX IF NOT EXISTS agent_handoffs_trace ON agent_handoffs(project_id, trace_id);
CREATE INDEX IF NOT EXISTS agent_handoffs_session ON agent_handoffs(project_id, session_id);
CREATE INDEX IF NOT EXISTS agent_handoffs_from ON agent_handoffs(project_id, from_agent);
CREATE INDEX IF NOT EXISTS agent_handoffs_to ON agent_handoffs(project_id, to_agent);
CREATE TABLE IF NOT EXISTS reasoning_steps (
	project_id TEXT NOT NULL,
	id TEXT NOT NULL,
	span_id TEXT NOT NULL,
	trace_id TEXT NOT NULL DEFAULT '',
	session_id TEXT NOT NULL DEFAULT '',
	agent_name TEXT NOT NULL DEFAULT '',
	idx INTEGER NOT NULL DEFAULT 0,
	kind TEXT NOT NULL DEFAULT 'thinking',
	text TEXT NOT NULL DEFAULT '',
	text_hash TEXT NOT NULL DEFAULT '',
	tokens INTEGER,
	source TEXT NOT NULL DEFAULT '',
	started_at INTEGER NOT NULL DEFAULT 0,
	next_span_id TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (project_id, id)
);
CREATE INDEX IF NOT EXISTS reasoning_steps_span ON reasoning_steps(project_id, span_id);
CREATE INDEX IF NOT EXISTS reasoning_steps_trace ON reasoning_steps(project_id, trace_id);
CREATE INDEX IF NOT EXISTS reasoning_steps_session ON reasoning_steps(project_id, session_id);
CREATE TABLE IF NOT EXISTS weft_backfills (
	name TEXT PRIMARY KEY,
	done_at INTEGER NOT NULL
);
`)
	registerMigration(func(db *sql.DB) error { // databases from before the verdict flag
		if ok, err := HasColumn(db, "agent_span_meta", "verdict"); err != nil || ok {
			return err
		}
		_, err := db.Exec(`ALTER TABLE agent_span_meta ADD COLUMN verdict INTEGER NOT NULL DEFAULT 0`)
		return err
	})
	registerMigration(backfillAgentGraph)
	registerCallHook(indexAgentGraphSpan)
}

// Hand-off kinds.
const (
	HandoffDelegate = "delegate"
	HandoffHandoff  = "handoff"
	HandoffReturn   = "return"
)

// Hand-off sources: the rule of DESIGN.md §2.1 that recorded an edge.
const (
	HandoffSrcNested    = "nested"        // nested invoke_agent of another agent
	HandoffSrcAttribute = "attribute"     // weft.handoff.* attributes
	HandoffSrcOpenAI    = "openai_agents" // OpenAI Agents SDK handoff spans / graph.node.parent_id
	HandoffSrcLangGraph = "langgraph"     // transitions between agent nodes
	HandoffSrcCrewAI    = "crewai"        // delegation tools
	HandoffSrcADK       = "adk"           // transfer_to_agent
	HandoffSrcLink      = "link"          // OTel span link to another agent's span
	HandoffSrcReturn    = "return"        // control going back after a delegation
)

// ---------------------------------------------------------------- per-span hints

type spanHints struct {
	TeamID, TeamName                                   string
	HandoffTo, HandoffFrom, HandoffKind, HandoffSource string
	GraphNode, GraphParent                             string
	GraphStep                                          int64
	OutToolCallIDs                                     []string
	// Verdict marks an AgentShield verdict span (DESIGN.md §5.1): stored as
	// a span and a detection, but not agent activity.
	Verdict bool
}

func (h spanHints) empty() bool {
	return h.TeamID == "" && h.HandoffTo == "" && h.GraphNode == "" && h.GraphParent == "" && len(h.OutToolCallIDs) == 0 && !h.Verdict
}

// isVerdictSpan reports an AgentShield verdict span: instrumentation scope
// agentshield, or an agentshield.verdict.action attribute.
func isVerdictSpan(attributes map[string]any) bool {
	o := asMap(attributes["otel"])
	if sc := asMap(o["scope"]); sc != nil && sc["name"] == "agentshield" {
		return true
	}
	return newSpanAttrs(attributes).str("agentshield.verdict.action") != ""
}

func normHandoffKind(s, def string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "delegate", "delegation":
		return HandoffDelegate
	case "handoff", "hand-off", "hand_off", "transfer":
		return HandoffHandoff
	case "return":
		return HandoffReturn
	}
	return def
}

// handoffNameArrows splits "Handoff: A → B" / "A -> B" span names.
var handoffNameArrows = []string{" → ", " -> ", "->", "→"}

func handoffFromName(name string) (from, to string) {
	l := strings.ToLower(name)
	if strings.HasPrefix(l, "handoff to ") {
		return "", strings.TrimSpace(name[len("handoff to "):])
	}
	if !strings.HasPrefix(l, "handoff") {
		return "", ""
	}
	rest := strings.TrimSpace(strings.TrimLeft(name[len("handoff"):], ":"))
	for _, a := range handoffNameArrows {
		if i := strings.Index(rest, a); i > 0 {
			return strings.TrimSpace(rest[:i]), strings.TrimSpace(rest[i+len(a):])
		}
	}
	return "", ""
}

var crewAIDelegationTools = map[string]bool{"delegate work to coworker": true, "ask question to coworker": true,
	"delegate_work_to_coworker": true, "ask_question_to_coworker": true}

// jsonMapAttr decodes a map-valued attribute that may be JSON-encoded.
func jsonMapAttr(v any) map[string]any {
	switch t := v.(type) {
	case map[string]any:
		return t
	case string:
		if s := strings.TrimSpace(t); strings.HasPrefix(s, "{") {
			var m map[string]any
			if json.Unmarshal([]byte(s), &m) == nil {
				return m
			}
		}
	}
	return nil
}

func toolArg(inputs map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := inputs[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	for _, w := range []string{"args", "arguments", "input", "kwargs"} {
		m := jsonMapAttr(inputs[w])
		for _, k := range keys {
			if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

// deriveHints reads the team, hand-off and graph hints of one span.
func deriveHints(op, spanName, toolName string, attributes, inputs map[string]any, output any) spanHints {
	a := newSpanAttrs(attributes)
	var h spanHints
	h.Verdict = isVerdictSpan(attributes)
	h.TeamID = a.str("weft.team.id", "weft.team_id")
	h.TeamName = a.str("weft.team.name", "weft.team_name")
	if h.TeamID == "" {
		h.TeamID = h.TeamName
	}

	// Hand-offs, most specific first.
	switch {
	case a.str("weft.handoff.to_agent") != "":
		h.HandoffTo, h.HandoffFrom = a.str("weft.handoff.to_agent"), a.str("weft.handoff.from_agent")
		h.HandoffKind, h.HandoffSource = normHandoffKind(a.str("weft.handoff.kind"), HandoffHandoff), HandoffSrcAttribute
	case a.str("to_agent", "handoff.to_agent", "gen_ai.handoff.to_agent", "openai.agents.handoff.to_agent") != "":
		h.HandoffTo = a.str("to_agent", "handoff.to_agent", "gen_ai.handoff.to_agent", "openai.agents.handoff.to_agent")
		h.HandoffFrom = a.str("from_agent", "handoff.from_agent", "gen_ai.handoff.from_agent", "openai.agents.handoff.from_agent")
		h.HandoffKind, h.HandoffSource = HandoffHandoff, HandoffSrcOpenAI
	default:
		if from, to := handoffFromName(spanName); to != "" {
			h.HandoffTo, h.HandoffFrom, h.HandoffKind, h.HandoffSource = to, from, HandoffHandoff, HandoffSrcOpenAI
		}
	}
	if h.HandoffTo == "" && op == OpExecuteTool {
		tn := strings.ToLower(strings.TrimSpace(toolName))
		switch {
		case crewAIDelegationTools[tn]:
			if to := toolArg(inputs, "coworker", "agent", "agent_name"); to != "" {
				h.HandoffTo, h.HandoffKind, h.HandoffSource = to, HandoffDelegate, HandoffSrcCrewAI
			}
		case tn == "transfer_to_agent":
			if to := toolArg(inputs, "agent_name", "agent"); to != "" {
				h.HandoffTo, h.HandoffKind, h.HandoffSource = to, HandoffHandoff, HandoffSrcADK
			}
		}
	}

	// LangGraph node metadata (OpenInference keeps it in a JSON "metadata"
	// attribute; other exporters flatten it).
	meta := jsonMapAttr(a.get("metadata"))
	h.GraphNode = a.str("langgraph_node", "langgraph.node", "metadata.langgraph_node")
	if h.GraphNode == "" {
		h.GraphNode, _ = meta["langgraph_node"].(string)
	}
	step := a.get("langgraph_step")
	if step == nil {
		step = a.get("langgraph.step")
	}
	if step == nil {
		step = meta["langgraph_step"]
	}
	if f, ok := toFloat(step); ok {
		h.GraphStep = int64(f)
	}
	// OpenInference marks an agent that was handed control with the agent
	// that handed it over.
	if op == OpInvokeAgent {
		if p := a.str("graph.node.parent_id"); p != "" && p != a.str("graph.node.id") {
			h.GraphParent = p
		}
	}

	if op == OpChat {
		seen := map[string]bool{}
		for _, m := range OutputMessages(output) {
			for _, tc := range m.ToolCalls {
				if tc.ID != "" && !seen[tc.ID] {
					seen[tc.ID] = true
					h.OutToolCallIDs = append(h.OutToolCallIDs, tc.ID)
				}
			}
		}
	}
	return h
}

type spanLink struct{ traceID, spanID string }

func spanLinks(attributes map[string]any) []spanLink {
	o := asMap(attributes["otel"])
	l, _ := o["links"].([]any)
	var out []spanLink
	for _, x := range l {
		m := asMap(x)
		sid, _ := m["span_id"].(string)
		tid, _ := m["trace_id"].(string)
		if sid != "" {
			out = append(out, spanLink{tid, sid})
		}
	}
	return out
}

// ---------------------------------------------------------------- hook

// traceBatch collects the traces a batch write touched, so each is derived
// once at the end of the transaction.
type traceBatch struct {
	seen  map[[2]string]bool
	order [][2]string
}

type traceBatchKey struct{}

// withTraceBatch defers trace derivation until flushTraceBatch.
func withTraceBatch(ctx context.Context) context.Context {
	if _, ok := ctx.Value(traceBatchKey{}).(*traceBatch); ok {
		return ctx
	}
	return context.WithValue(ctx, traceBatchKey{}, &traceBatch{seen: map[[2]string]bool{}})
}

// flushTraceBatch derives the traces collected since withTraceBatch.
func flushTraceBatch(ctx context.Context, tx *sql.Tx) error {
	b, ok := ctx.Value(traceBatchKey{}).(*traceBatch)
	if !ok || len(b.order) == 0 {
		return nil
	}
	keys := b.order
	b.order, b.seen = nil, map[[2]string]bool{}
	return deriveTraces(ctx, tx, keys)
}

func markTraceDirty(ctx context.Context, tx *sql.Tx, projectID, traceID string) error {
	k := [2]string{projectID, traceID}
	if b, ok := ctx.Value(traceBatchKey{}).(*traceBatch); ok {
		if !b.seen[k] {
			b.seen[k] = true
			b.order = append(b.order, k)
		}
		return nil
	}
	return deriveTraces(ctx, tx, [][2]string{k})
}

func indexAgentGraphSpan(ctx context.Context, tx *sql.Tx, projectID, id string) error {
	var opName, traceID, attrs, inputs, summary string
	var output sql.NullString
	var started sql.NullInt64
	var op, spanName, toolName, agent, conv sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT c.op_name, c.trace_id, c.attributes, c.inputs, c.output, c.summary, c.started_at,
			s.operation_name, s.span_name, s.tool_name, s.agent_name, s.conversation_id
		FROM calls c LEFT JOIN agent_spans s ON s.project_id = c.project_id AND s.id = c.id
		WHERE c.project_id = ? AND c.id = ?`, projectID, id).
		Scan(&opName, &traceID, &attrs, &inputs, &output, &summary, &started, &op, &spanName, &toolName, &agent, &conv)
	if err == sql.ErrNoRows || (err == nil && (opName == "" || !op.Valid)) {
		return nil // end before start: the start indexes it
	}
	if err != nil {
		return err
	}
	attrMap := decodeMap(attrs)
	if w := asMap(attrMap["weft"]); w != nil && isTruthy(w["internal"]) {
		return nil // judge, monitor and signal calls Weft makes itself
	}
	isModel := op.String == OpChat
	var inMap map[string]any
	var out any
	if isModel || op.String == OpExecuteTool {
		inMap = decodeMap(inputs)
		if isModel && output.Valid {
			out = decodeJSON(output.String)
		}
	}

	h := deriveHints(op.String, spanName.String, toolName.String, attrMap, inMap, out)
	if h.empty() {
		if _, err := tx.ExecContext(ctx, `DELETE FROM agent_span_meta WHERE project_id = ? AND id = ?`, projectID, id); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx, `INSERT INTO agent_span_meta(project_id, id, trace_id, team_id, team_name, handoff_to,
			handoff_from, handoff_kind, handoff_source, graph_node, graph_parent, graph_step, out_tool_call_ids, verdict)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(project_id, id) DO UPDATE SET trace_id = excluded.trace_id, team_id = excluded.team_id, team_name = excluded.team_name,
			handoff_to = excluded.handoff_to, handoff_from = excluded.handoff_from, handoff_kind = excluded.handoff_kind,
			handoff_source = excluded.handoff_source, graph_node = excluded.graph_node, graph_parent = excluded.graph_parent,
			graph_step = excluded.graph_step, out_tool_call_ids = excluded.out_tool_call_ids, verdict = excluded.verdict`,
		projectID, id, traceID, h.TeamID, h.TeamName, h.HandoffTo, h.HandoffFrom, h.HandoffKind, h.HandoffSource,
		h.GraphNode, h.GraphParent, h.GraphStep, strings.Join(h.OutToolCallIDs, "\n"), h.Verdict); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_span_links WHERE project_id = ? AND span_id = ?`, projectID, id); err != nil {
		return err
	}
	for _, l := range spanLinks(attrMap) {
		if l.spanID == id {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO agent_span_links(project_id, span_id, trace_id, linked_trace_id, linked_span_id)
			VALUES(?,?,?,?,?)`, projectID, id, traceID, l.traceID, l.spanID); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM reasoning_steps WHERE project_id = ? AND span_id = ?`, projectID, id); err != nil {
		return err
	}
	steps := extractReasoning(isModel, attrMap, inMap, out, decodeMap(summary))
	for i, st := range steps {
		at := st.AtUS
		if at == 0 {
			at = started.Int64
		}
		var tok any
		if st.Tokens > 0 {
			tok = st.Tokens
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO reasoning_steps(project_id, id, span_id, trace_id, session_id, agent_name, idx, kind,
				text, text_hash, tokens, source, started_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(project_id, id) DO UPDATE SET span_id = excluded.span_id, trace_id = excluded.trace_id,
				session_id = excluded.session_id, agent_name = excluded.agent_name, idx = excluded.idx, kind = excluded.kind,
				text = excluded.text, text_hash = excluded.text_hash, tokens = excluded.tokens, source = excluded.source,
				started_at = excluded.started_at`,
			projectID, reasoningStepID(id, i), id, traceID, conv.String, agent.String, i, st.Kind, st.Text, st.Hash, tok, st.Source, at); err != nil {
			return err
		}
	}
	return markTraceDirty(ctx, tx, projectID, traceID)
}

func reasoningStepID(spanID string, i int) string {
	return spanID + "#" + strconv.Itoa(i)
}

// ---------------------------------------------------------------- derivation

type gspan struct {
	ID, Parent, Op, SpanName, Display, Agent, Conv, Tool, ToolCallID string
	Own                                                              bool
	Start, End                                                       int64
	Ended, Failed                                                    bool
	HTo, HFrom, HKind, HSource, GNode, GParent                       string
	GStep                                                            int64
	OutTools                                                         []string

	parent  *gspan
	inv     bool
	missing bool // parent_id names a span not indexed yet
}

type gtrace struct {
	spans []*gspan
	byID  map[string]*gspan
	root  map[*gspan]*gspan // invocation root memo (nil value: none)
}

func (t *gtrace) invRoot(sp *gspan) *gspan {
	if r, ok := t.root[sp]; ok {
		return r
	}
	var r *gspan
	for x := sp; x != nil; x = x.parent {
		if x.inv {
			r = x
			break
		}
	}
	t.root[sp] = r
	return r
}

// nearestInvAncestor is the closest strict ancestor that is an agent
// invocation; known is false when the chain is broken by a span that has
// not arrived yet.
func (t *gtrace) nearestInvAncestor(sp *gspan) (p *gspan, known bool) {
	for x := sp; ; {
		if x.Parent == "" {
			return nil, true
		}
		if x.parent == nil {
			return nil, false
		}
		x = x.parent
		if x.inv {
			return x, true
		}
	}
}

func (t *gtrace) isDescendant(sp, anc *gspan) bool {
	for x := sp.parent; x != nil; x = x.parent {
		if x == anc {
			return true
		}
	}
	return false
}

func normAgentName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.NewReplacer(" ", "_", "-", "_", ".", "_").Replace(s)
}

// resolveTarget finds the invocation of agent that a hand-off at src handed
// control to: a descendant of src first, else the first one starting after.
func (t *gtrace) resolveTarget(src *gspan, agent string) *gspan {
	norm := normAgentName(agent)
	var best *gspan
	bestDesc := false
	for _, sp := range t.spans {
		if !sp.inv || sp == src || (sp.Agent != agent && normAgentName(sp.Agent) != norm) || sp.Start < src.Start-1000 {
			continue
		}
		desc := t.isDescendant(sp, src)
		switch {
		case best == nil, desc && !bestDesc, desc == bestDesc && sp.Start < best.Start:
			best, bestDesc = sp, desc
		}
	}
	return best
}

type hedge struct {
	id, kind, source               string
	src, from, to                  *gspan
	caller                         *gspan // the span on the caller's side, for reasoning links
	fromSpanID, fromAgent, toAgent string
	started, ended                 int64
	hasEnd                         bool
	status                         string
}

func handoffID(rule string, parts ...string) string {
	h := sha256.Sum256([]byte(rule + "\x00" + strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:16])
}

func spanStatus(sp *gspan) string {
	switch {
	case sp == nil:
		return ""
	case sp.Failed:
		return "error"
	case !sp.Ended:
		return "running"
	}
	return "ok"
}

func loadTraceSpans(ctx context.Context, tx *sql.Tx, projectID, traceID string) (*gtrace, error) {
	rows, err := tx.QueryContext(ctx, `SELECT s.id, COALESCE(s.parent_id, ''), s.operation_name, s.span_name, COALESCE(c.display_name, ''),
			s.own_agent, s.agent_name, s.conversation_id, s.tool_name, s.tool_call_id, c.started_at, c.ended_at, c.exception IS NOT NULL,
			COALESCE(m.handoff_to, ''), COALESCE(m.handoff_from, ''), COALESCE(m.handoff_kind, ''), COALESCE(m.handoff_source, ''),
			COALESCE(m.graph_node, ''), COALESCE(m.graph_parent, ''), COALESCE(m.graph_step, 0), COALESCE(m.out_tool_call_ids, '')
		FROM agent_spans s JOIN calls c ON c.project_id = s.project_id AND c.id = s.id
		LEFT JOIN agent_span_meta m ON m.project_id = s.project_id AND m.id = s.id
		WHERE s.project_id = ? AND s.trace_id = ? AND c.deleted_at IS NULL AND COALESCE(m.verdict, 0) = 0
		ORDER BY c.started_at, s.id`, projectID, traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	t := &gtrace{byID: map[string]*gspan{}, root: map[*gspan]*gspan{}}
	for rows.Next() {
		sp := &gspan{}
		var started, ended sql.NullInt64
		var tools string
		if err := rows.Scan(&sp.ID, &sp.Parent, &sp.Op, &sp.SpanName, &sp.Display, &sp.Own, &sp.Agent, &sp.Conv, &sp.Tool, &sp.ToolCallID,
			&started, &ended, &sp.Failed, &sp.HTo, &sp.HFrom, &sp.HKind, &sp.HSource, &sp.GNode, &sp.GParent, &sp.GStep, &tools); err != nil {
			return nil, err
		}
		sp.Start, sp.End, sp.Ended = started.Int64, ended.Int64, ended.Valid
		if tools != "" {
			sp.OutTools = strings.Split(tools, "\n")
		}
		t.spans = append(t.spans, sp)
		t.byID[sp.ID] = sp
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, sp := range t.spans {
		if sp.Parent != "" {
			sp.parent = t.byID[sp.Parent]
			sp.missing = sp.parent == nil
		}
	}
	// Agent invocations, as markInvocations (agents_query.go) defines them.
	for _, sp := range t.spans {
		if sp.Agent == "" || !sp.Own {
			continue
		}
		if sp.Op == OpInvokeAgent {
			sp.inv = true
			continue
		}
		p := sp.parent
		sp.inv = (p == nil || p.Agent != sp.Agent) && (sp.Op != OpExecuteTool && sp.Op != OpChat || p == nil)
	}
	return t, nil
}

// graphNodeSpans returns LangGraph node executions: the top-most span of
// each node run, and whether it ran a model (an agent node).
func (t *gtrace) graphNodeSpans() map[*gspan]bool {
	nodes := map[*gspan]bool{}
	for _, sp := range t.spans {
		if sp.GNode == "" || strings.HasPrefix(sp.GNode, "__") {
			continue
		}
		if p := sp.parent; p != nil && p.GNode == sp.GNode {
			continue
		}
		nodes[sp] = sp.Own && sp.Agent != ""
	}
	if len(nodes) == 0 {
		return nodes
	}
	for _, sp := range t.spans {
		if sp.Op != OpChat {
			continue
		}
		for x := sp; x != nil; x = x.parent {
			if _, ok := nodes[x]; ok {
				nodes[x] = true
				break
			}
		}
	}
	return nodes
}

func (sp *gspan) nodeAgent() string {
	if sp.Own && sp.Agent != "" {
		return sp.Agent
	}
	return sp.GNode
}

func (t *gtrace) deriveEdges(links map[string][]linkedSpan) []*hedge {
	var edges []*hedge
	targeted := map[*gspan]bool{}
	add := func(e *hedge) {
		if e.to != nil {
			targeted[e.to] = true
			e.toAgent = e.to.Agent
			if e.toAgent == "" {
				e.toAgent = e.to.nodeAgent()
			}
		}
		if e.from != nil {
			e.fromSpanID = e.from.ID
		}
		edges = append(edges, e)
	}

	// Explicit hand-offs: weft.handoff.*, framework hand-off spans and tools.
	for _, sp := range t.spans {
		if sp.HTo == "" {
			continue
		}
		from := sp.HFrom
		if from == "" {
			from = sp.Agent
			if (from == "" || from == sp.HTo) && sp.parent != nil {
				from = sp.parent.Agent
			}
		}
		if from == "" || from == sp.HTo {
			continue
		}
		e := &hedge{id: handoffID("x", sp.ID), kind: normHandoffKind(sp.HKind, HandoffHandoff), source: sp.HSource,
			src: sp, from: sp, caller: sp, fromAgent: from, toAgent: sp.HTo}
		if e.kind != HandoffReturn {
			e.to = t.resolveTarget(sp, sp.HTo)
		}
		add(e)
		if e.to == nil {
			e.toAgent = sp.HTo
		}
	}

	// LangGraph transitions between agent nodes of the same graph run.
	nodes := t.graphNodeSpans()
	byParent := map[string][]*gspan{}
	for sp, isAgent := range nodes {
		if isAgent {
			byParent[sp.Parent] = append(byParent[sp.Parent], sp)
		}
	}
	for _, list := range byParent {
		sort.Slice(list, func(i, j int) bool {
			if list[i].GStep != list[j].GStep {
				return list[i].GStep < list[j].GStep
			}
			return list[i].Start < list[j].Start
		})
		for i := 1; i < len(list); i++ {
			prev, next := list[i-1], list[i]
			if prev.nodeAgent() == next.nodeAgent() || targeted[next] {
				continue
			}
			add(&hedge{id: handoffID("g", next.ID), kind: HandoffHandoff, source: HandoffSrcLangGraph, src: next, from: prev,
				to: next, caller: prev, fromAgent: prev.nodeAgent()})
		}
	}

	// OpenInference graph.node.parent_id on agent spans.
	for _, sp := range t.spans {
		if !sp.inv || sp.GParent == "" || sp.GParent == sp.Agent || targeted[sp] {
			continue
		}
		var from *gspan
		for _, c := range t.spans {
			if c.inv && c.Agent == sp.GParent && c.Start <= sp.Start && (from == nil || c.Start > from.Start) {
				from = c
			}
		}
		add(&hedge{id: handoffID("p", sp.ID), kind: HandoffHandoff, source: HandoffSrcOpenAI, src: sp, from: from, to: sp,
			caller: from, fromAgent: sp.GParent})
	}

	// Span links from an invocation to another agent's span.
	for _, sp := range t.spans {
		if !sp.inv || targeted[sp] {
			continue
		}
		for _, l := range links[sp.ID] {
			if l.agent == "" || l.agent == sp.Agent {
				continue
			}
			e := &hedge{id: handoffID("l", sp.ID, l.spanID), kind: HandoffHandoff, source: HandoffSrcLink, src: sp, to: sp,
				fromAgent: l.agent}
			add(e)
			e.fromSpanID = l.spanID
			break
		}
	}

	// Nested invocations of another agent.
	for _, sp := range t.spans {
		if !sp.inv || targeted[sp] {
			continue
		}
		p, known := t.nearestInvAncestor(sp)
		if !known || p == nil || p.Agent == sp.Agent {
			continue
		}
		add(&hedge{id: handoffID("d", sp.ID), kind: HandoffDelegate, source: HandoffSrcNested, src: sp, from: p, to: sp,
			caller: sp.parent, fromAgent: p.Agent})
	}

	// Timing and status; control going back after a delegation.
	n := len(edges)
	for i := 0; i < n; i++ {
		e := edges[i]
		w := e.to
		if w == nil {
			w = e.src
		}
		e.started, e.status = w.Start, spanStatus(w)
		if w.Ended {
			e.ended, e.hasEnd = w.End, true
		}
		if e.kind == HandoffDelegate && e.to != nil && e.to.Ended && e.fromSpanID != "" {
			edges = append(edges, &hedge{id: handoffID("r", e.id), kind: HandoffReturn, source: HandoffSrcReturn, src: e.src,
				from: e.to, fromSpanID: e.to.ID, to: e.from, fromAgent: e.toAgent, toAgent: e.fromAgent,
				started: e.to.End, ended: e.to.End, hasEnd: true, status: spanStatus(e.to)})
		}
	}
	return edges
}

type linkedSpan struct{ spanID, agent string }

func loadTraceLinks(ctx context.Context, tx *sql.Tx, projectID, traceID string) (map[string][]linkedSpan, error) {
	rows, err := tx.QueryContext(ctx, `SELECT l.span_id, l.linked_span_id, COALESCE(s.agent_name, '')
		FROM agent_span_links l LEFT JOIN agent_spans s ON s.project_id = l.project_id AND s.id = l.linked_span_id
		WHERE l.project_id = ? AND l.trace_id = ? ORDER BY l.span_id, l.linked_span_id`, projectID, traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]linkedSpan{}
	for rows.Next() {
		var sid string
		var l linkedSpan
		if err := rows.Scan(&sid, &l.spanID, &l.agent); err != nil {
			return nil, err
		}
		out[sid] = append(out[sid], l)
	}
	return out, rows.Err()
}

const handoffPreviewLen = 500

// spanPreviews reads short input and output texts of spans.
func spanPreviews(ctx context.Context, tx *sql.Tx, projectID string, ids []string) (map[string][2]string, error) {
	return spanPreviewsQ(ctx, tx, projectID, ids, handoffPreviewLen)
}

// inputPreview is the request a span received: its last user message, or
// its arguments.
func inputPreview(inputs map[string]any) string {
	if msgs := InputMessages(inputs); msgs != nil {
		if t, _ := lastUserText(msgs); t != "" {
			return t
		}
	}
	return InputText(inputs)
}

// outputPreview is what a span returned: the assistant text of a
// chat-shaped output (empty when the reply was only reasoning or tool
// calls), else the output as text.
func outputPreview(output any) string {
	if msgs := OutputMessages(output); msgs != nil {
		t, _ := assistantText(msgs)
		return t
	}
	return OutputText(output)
}

// deriveTraces derives each trace, then the traces linking into them.
func deriveTraces(ctx context.Context, tx *sql.Tx, keys [][2]string) error {
	done := map[[2]string]bool{}
	queue := append([][2]string(nil), keys...)
	for len(queue) > 0 && len(done) < 10000 {
		k := queue[0]
		queue = queue[1:]
		if done[k] {
			continue
		}
		done[k] = true
		deps, err := deriveTrace(ctx, tx, k[0], k[1])
		if err != nil {
			return err
		}
		for _, d := range deps {
			if dk := [2]string{k[0], d}; !done[dk] {
				queue = append(queue, dk)
			}
		}
	}
	return nil
}

func nullMicros(v int64, ok bool) any {
	if !ok {
		return nil
	}
	return v
}

func deriveTrace(ctx context.Context, tx *sql.Tx, projectID, traceID string) ([]string, error) {
	t, err := loadTraceSpans(ctx, tx, projectID, traceID)
	if err != nil {
		return nil, err
	}
	links, err := loadTraceLinks(ctx, tx, projectID, traceID)
	if err != nil {
		return nil, err
	}
	edges := t.deriveEdges(links)

	var ids []string
	for _, e := range edges {
		if e.kind == HandoffReturn {
			continue
		}
		if e.to != nil {
			ids = append(ids, e.to.ID)
		}
		ids = append(ids, e.src.ID)
	}
	prev, err := spanPreviews(ctx, tx, projectID, ids)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_handoffs WHERE project_id = ? AND trace_id = ?`, projectID, traceID); err != nil {
		return nil, err
	}
	for _, e := range edges {
		var in, out string
		if e.kind == HandoffReturn {
			out = prev[e.from.ID][1]
		} else {
			if e.to != nil {
				in, out = prev[e.to.ID][0], prev[e.to.ID][1]
			}
			if in == "" {
				in = prev[e.src.ID][0]
			}
			if out == "" {
				out = prev[e.src.ID][1]
			}
		}
		session := ""
		for _, sp := range []*gspan{e.to, e.src, e.from} {
			if sp != nil && sp.Conv != "" {
				session = sp.Conv
				break
			}
		}
		toSpan := ""
		if e.to != nil {
			toSpan = e.to.ID
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO agent_handoffs(project_id, id, session_id, trace_id, source_span_id,
				from_span_id, to_span_id, from_agent, to_agent, kind, source, started_at, ended_at, status, input_preview, output_preview)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			projectID, e.id, session, traceID, e.src.ID, e.fromSpanID, toSpan, e.fromAgent, e.toAgent, e.kind, e.source,
			nullMicros(e.started, e.started != 0), nullMicros(e.ended, e.hasEnd), e.status, in, out); err != nil {
			return nil, err
		}
	}
	if err := t.updateReasoning(ctx, tx, projectID, traceID, edges); err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT trace_id FROM agent_span_links WHERE project_id = ? AND linked_trace_id = ? AND trace_id != ?`,
		projectID, traceID, traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var deps []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		deps = append(deps, d)
	}
	return deps, rows.Err()
}

// actionCandidate is a tool call or hand-off a reasoning step can lead to.
type actionCandidate struct {
	spanID string
	start  int64
	root   *gspan
}

func (t *gtrace) actionCandidates(edges []*hedge) []actionCandidate {
	var out []actionCandidate
	for _, sp := range t.spans {
		if sp.Op == OpExecuteTool {
			out = append(out, actionCandidate{sp.ID, sp.Start, t.invRoot(sp)})
		}
	}
	for _, e := range edges {
		if e.kind == HandoffReturn || e.caller == nil {
			continue
		}
		out = append(out, actionCandidate{e.src.ID, e.src.Start, t.invRoot(e.caller)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].start < out[j].start })
	return out
}

// nextAction finds the first tool call or hand-off starting at or after
// `at`, before `until` (0: unbounded), in the same agent run as root.
func nextAction(cands []actionCandidate, root *gspan, at, until int64, exclude string) string {
	for _, c := range cands {
		if c.start < at || c.spanID == exclude || c.root != root {
			continue
		}
		if until > 0 && c.start >= until {
			break
		}
		return c.spanID
	}
	return ""
}

func (t *gtrace) updateReasoning(ctx context.Context, tx *sql.Tx, projectID, traceID string, edges []*hedge) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, span_id, source, started_at, next_span_id, agent_name, session_id
		FROM reasoning_steps WHERE project_id = ? AND trace_id = ?`, projectID, traceID)
	if err != nil {
		return err
	}
	type stepRow struct{ id, span, source, next, agent, session string }
	var steps []stepRow
	var at []int64
	for rows.Next() {
		var r stepRow
		var a int64
		if err := rows.Scan(&r.id, &r.span, &r.source, &a, &r.next, &r.agent, &r.session); err != nil {
			rows.Close()
			return err
		}
		steps, at = append(steps, r), append(at, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(steps) == 0 {
		return err
	}
	cands := t.actionCandidates(edges)
	toolByCallID := map[string]*gspan{}
	for _, sp := range t.spans {
		if sp.Op == OpExecuteTool && sp.ToolCallID != "" {
			if prev, ok := toolByCallID[sp.ToolCallID]; !ok || sp.Start < prev.Start {
				toolByCallID[sp.ToolCallID] = sp
			}
		}
	}
	// model span -> start of the next model span in its agent run (t.spans
	// is ordered by start time)
	nextModel := map[*gspan]int64{}
	lastModel := map[*gspan]*gspan{}
	for _, sp := range t.spans {
		if sp.Op != OpChat {
			continue
		}
		root := t.invRoot(sp)
		if p := lastModel[root]; p != nil && sp.Start > p.Start {
			nextModel[p] = sp.Start
		}
		lastModel[root] = sp
	}
	for i, r := range steps {
		sp := t.byID[r.span]
		if sp == nil {
			continue
		}
		next := ""
		switch r.source {
		case ReasoningFromOutput:
			for _, id := range sp.OutTools {
				if tool := toolByCallID[id]; tool != nil {
					next = tool.ID
					break
				}
			}
			if next == "" {
				next = nextAction(cands, t.invRoot(sp), sp.Start, nextModel[sp], sp.ID)
			}
		case ReasoningFromEvent:
			next = nextAction(cands, t.invRoot(sp), at[i], 0, sp.ID)
		}
		if next != r.next || sp.Agent != r.agent || sp.Conv != r.session {
			if _, err := tx.ExecContext(ctx, `UPDATE reasoning_steps SET next_span_id = ?, agent_name = ?, session_id = ? WHERE project_id = ? AND id = ?`,
				next, sp.Agent, sp.Conv, projectID, r.id); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------- backfill

const agentGraphBackfill = "agent_graph_v1"

// backfillAgentGraph indexes calls written before the agent graph existed,
// once per database.
func backfillAgentGraph(db *sql.DB) error {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM weft_backfills WHERE name = ?`, agentGraphBackfill).Scan(&n); err != nil || n > 0 {
		return err
	}
	rows, err := db.Query(`SELECT project_id, id FROM calls WHERE deleted_at IS NULL AND has_start = 1 ORDER BY project_id, started_at`)
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
	ctx := withTraceBatch(context.Background())
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if err := indexAgentGraphSpan(ctx, tx, k[0], k[1]); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err := flushTraceBatch(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO weft_backfills(name, done_at) VALUES(?, ?)`, agentGraphBackfill, nowMicros()); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ReindexAgentGraph rebuilds the hand-off and reasoning index of a project
// (tests, repair). Run ReindexAgentSpans first when the span index changed.
func (s *Store) ReindexAgentGraph(ctx context.Context, projectID string) error {
	ctx = withTraceBatch(ctx)
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for _, tbl := range []string{"agent_span_meta", "agent_span_links", "agent_handoffs", "reasoning_steps"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+tbl+` WHERE project_id = ?`, projectID); err != nil {
				return err
			}
		}
		rows, err := tx.QueryContext(ctx, `SELECT id FROM calls WHERE project_id = ? AND deleted_at IS NULL AND has_start = 1 ORDER BY started_at`, projectID)
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
			if err := indexAgentGraphSpan(ctx, tx, projectID, id); err != nil {
				return err
			}
		}
		return flushTraceBatch(ctx, tx)
	})
}
