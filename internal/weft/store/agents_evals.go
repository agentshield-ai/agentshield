package store

// Agent evals: which agent spans and conversations belong to an evaluation
// run, so the Agents views can be filtered to a run and a run's agent
// activity listed.
//
// A span belongs to evaluation run R (prediction P) when
//   - it carries weft.eval.run_id = R (and weft.eval.predict_and_score_call_id
//     = P), as spans recorded inside EvaluationLogger.log_prediction() do, or
//     OTel spans an agent service tags itself;
//   - an Evaluation.predict_and_score call P under R links to it through
//     attributes.weft.linked_calls / attributes.weft.genai_span_ref (a span id
//     or call ref), or links its conversation through
//     attributes.weft.linked_conversation_ids;
//   - or it descends from such a span (OTel attributes do not propagate to
//     children, and LLM integrations inside a turn do not tag their calls).
//
// Links are written to agent_eval_links by the agents index hook, from the
// already decoded call attributes; membership (descendants, conversations)
// is resolved at query time.

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

const opPredictAndScoreName = "Evaluation.predict_and_score"

func init() {
	registerSchema(`
CREATE TABLE IF NOT EXISTS agent_eval_links (
	project_id TEXT NOT NULL,
	source_id TEXT NOT NULL,
	kind TEXT NOT NULL,
	target TEXT NOT NULL,
	run_id TEXT NOT NULL DEFAULT '',
	pas_id TEXT NOT NULL DEFAULT '',
	trace_id TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (project_id, source_id, kind, target)
);
CREATE INDEX IF NOT EXISTS agent_eval_links_run ON agent_eval_links(project_id, run_id);
CREATE INDEX IF NOT EXISTS agent_eval_links_pas ON agent_eval_links(project_id, pas_id);
CREATE INDEX IF NOT EXISTS agent_eval_links_target ON agent_eval_links(project_id, target);
`)
	registerMigration(backfillEvalLinks)
}

// Link kinds: a span tagged with weft.eval.*, a call linked from a
// prediction, and a conversation linked from a prediction.
const (
	evalLinkAttr         = "attr"
	evalLinkSpan         = "span"
	evalLinkConversation = "conversation"
)

type evalLink struct {
	kind, target, runID, pasID, traceID string
}

// linkedCallID reads a call id from an id, a weft:/// span ref,
// or {call_id|span_id|id, trace_id?}.
func linkedCallID(v any) (id, trace string) {
	switch t := v.(type) {
	case string:
		s := strings.TrimSpace(t)
		if strings.HasPrefix(s, "weft:///") {
			if r, err := refs.Parse(s); err == nil && r.Kind == refs.KindSpan {
				return r.Name, ""
			}
		}
		return s, ""
	case map[string]any:
		for _, k := range []string{"call_id", "span_id", "id"} {
			if s, ok := t[k].(string); ok && strings.TrimSpace(s) != "" {
				tr, _ := t["trace_id"].(string)
				return strings.TrimSpace(s), tr
			}
		}
	}
	return "", ""
}

func listOf(v any) []any {
	switch t := v.(type) {
	case nil:
		return nil
	case []any:
		return t
	}
	return []any{v}
}

// evalLinksOf derives the links one call records. pasParent resolves the
// run of a span that names only its prediction.
func evalLinksOf(opName, id string, parentID *string, attributes map[string]any, pasParent func(string) string) []evalLink {
	a := newSpanAttrs(attributes)
	var out []evalLink
	run, pas := a.str("weft.eval.run_id"), a.str("weft.eval.predict_and_score_call_id")
	if run == "" && pas != "" && pasParent != nil {
		run = pasParent(pas)
	}
	if run != "" {
		out = append(out, evalLink{kind: evalLinkAttr, target: id, runID: run, pasID: pas})
	}
	// Predictions: an Evaluation.predict_and_score call, the parent its run.
	if OpBaseName(opName) != opPredictAndScoreName {
		return out
	}
	weft, _ := attributes["weft"].(map[string]any)
	if weft == nil {
		return out
	}
	run = ""
	if s, ok := weft["evaluation_run_id"].(string); ok {
		run = s
	}
	if run == "" && parentID != nil {
		run = *parentID
	}
	if run == "" {
		return out
	}
	seen := map[string]bool{}
	for _, key := range []string{"linked_calls", "genai_span_ref"} {
		for _, v := range listOf(weft[key]) {
			cid, tr := linkedCallID(v)
			if cid == "" || seen[cid] {
				continue
			}
			seen[cid] = true
			out = append(out, evalLink{kind: evalLinkSpan, target: cid, runID: run, pasID: id, traceID: tr})
		}
	}
	for _, v := range listOf(weft["linked_conversation_ids"]) {
		if s, ok := v.(string); ok && s != "" && !seen["conv:"+s] {
			seen["conv:"+s] = true
			out = append(out, evalLink{kind: evalLinkConversation, target: s, runID: run, pasID: id})
		}
	}
	return out
}

// indexEvalLinksTx rewrites the links a call records.
func indexEvalLinksTx(ctx context.Context, tx dbtx, projectID, id, opName string, parentID *string, attributes map[string]any) error {
	links := evalLinksOf(opName, id, parentID, attributes, func(pas string) string {
		var p sql.NullString
		_ = tx.QueryRowContext(ctx, `SELECT parent_id FROM calls WHERE project_id = ? AND id = ?`, projectID, pas).Scan(&p)
		return p.String
	})
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_eval_links WHERE project_id = ? AND source_id = ?`, projectID, id); err != nil {
		return err
	}
	for _, l := range links {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO agent_eval_links(project_id, source_id, kind, target, run_id, pas_id, trace_id)
			VALUES(?,?,?,?,?,?,?)`, projectID, id, l.kind, l.target, l.runID, l.pasID, l.traceID); err != nil {
			return err
		}
	}
	return nil
}

// backfillEvalLinks indexes links of calls written before agent_eval_links
// existed, once (a marker row records that it ran).
func backfillEvalLinks(db *sql.DB) error {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_eval_links WHERE project_id = '' AND kind = '_backfilled'`).Scan(&n); err != nil || n > 0 {
		return err
	}
	rows, err := db.Query(`SELECT project_id, id, op_name, parent_id, attributes FROM calls
		WHERE attributes LIKE '%eval%' OR attributes LIKE '%linked_c%' OR attributes LIKE '%genai_span_ref%'`)
	if err != nil {
		return err
	}
	type rec struct {
		pid, id, op, attrs string
		parent             sql.NullString
	}
	var recs []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.pid, &r.id, &r.op, &r.parent, &r.attrs); err != nil {
			rows.Close()
			return err
		}
		recs = append(recs, r)
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
	for _, r := range recs {
		if err := indexEvalLinksTx(ctx, tx, r.pid, r.id, r.op, strPtr(r.parent), decodeMap(r.attrs)); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO agent_eval_links(project_id, source_id, kind, target) VALUES('', '', '_backfilled', '')`); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ---------------------------------------------------------------- membership

// AgentEvalFilter restricts agent queries to the spans of an evaluation run
// (Weft extension). PredictAndScoreCallID narrows to one prediction.
type AgentEvalFilter struct {
	EvaluationRunID       string `json:"evaluation_run_id,omitempty"`
	PredictAndScoreCallID string `json:"predict_and_score_call_id,omitempty"`
}

func (f *AgentEvalFilter) active() bool {
	return f != nil && (f.EvaluationRunID != "" || f.PredictAndScoreCallID != "")
}

// evalRef is one (run, prediction) a span belongs to.
type evalRef struct{ Run, Pas string }

// evalMembership maps span ids to the runs and predictions they belong to.
// runs / pases restrict the seeds (nil: every link in the project).
func (s *Store) evalMembership(ctx context.Context, pid string, runs, pases []string) (map[string][]evalRef, error) {
	cond := ""
	args := []any{pid}
	if runs != nil || pases != nil {
		var parts []string
		if len(runs) > 0 {
			c, a := inList("l.run_id", runs)
			parts = append(parts, c)
			args = append(args, a...)
		}
		if len(pases) > 0 {
			c, a := inList("l.pas_id", pases)
			parts = append(parts, c)
			args = append(args, a...)
		}
		if len(parts) == 0 {
			return map[string][]evalRef{}, nil
		}
		cond = " AND (" + strings.Join(parts, " AND ") + ")"
	}
	// Links recorded by deleted calls, or pointing at deleted runs, drop out.
	seed := `FROM agent_eval_links l JOIN calls src ON src.project_id = l.project_id AND src.id = l.source_id AND src.deleted_at IS NULL
		LEFT JOIN calls r ON r.project_id = l.project_id AND r.id = l.run_id
		WHERE l.project_id = ? AND l.kind != '_backfilled' AND (r.id IS NULL OR r.deleted_at IS NULL)` + cond
	q := `WITH RECURSIVE d(id, run_id, pas_id) AS (
			SELECT l.target, l.run_id, l.pas_id ` + seed + ` AND l.kind IN ('attr', 'span')
			UNION
			SELECT s.id, d.run_id, d.pas_id FROM agent_spans s JOIN d ON s.parent_id = d.id WHERE s.project_id = ?)
		SELECT id, run_id, pas_id FROM d
		UNION
		SELECT s.id, l.run_id, l.pas_id ` + strings.Replace(seed, "FROM agent_eval_links l", "FROM agent_eval_links l JOIN agent_spans s ON s.project_id = l.project_id AND s.conversation_id = l.target", 1) +
		` AND l.kind = 'conversation'`
	all := append(append(append([]any{}, args...), pid), args...)
	rows, err := s.r.QueryContext(ctx, q, all...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]evalRef{}
	for rows.Next() {
		var id string
		var r evalRef
		if err := rows.Scan(&id, &r.Run, &r.Pas); err != nil {
			return nil, err
		}
		dup := false
		for _, x := range out[id] {
			if x == r {
				dup = true
				break
			}
		}
		if !dup {
			out[id] = append(out[id], r)
		}
	}
	return out, rows.Err()
}

// filterByEval keeps spans of the filter's run (and prediction).
func (s *Store) filterByEval(ctx context.Context, pid string, spans []*agentSpan, f *AgentEvalFilter) ([]*agentSpan, error) {
	var runs, pases []string
	if f.EvaluationRunID != "" {
		runs = []string{f.EvaluationRunID}
	}
	if f.PredictAndScoreCallID != "" {
		pases = []string{f.PredictAndScoreCallID}
	}
	m, err := s.evalMembership(ctx, pid, runs, pases)
	if err != nil {
		return nil, err
	}
	out := spans[:0:0]
	for _, sp := range spans {
		if len(m[sp.ID]) > 0 {
			out = append(out, sp)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- /agents/evals/query

// AgentEvalsQueryReq is the body of /agents/evals/query (Weft).
type AgentEvalsQueryReq struct {
	ProjectID          string   `json:"project_id"`
	AgentName          string   `json:"agent_name"`
	ConversationID     string   `json:"conversation_id"`
	TraceIDs           []string `json:"trace_ids"`
	EvaluationRunIDs   []string `json:"evaluation_run_ids"`
	IncludeCosts       bool     `json:"include_costs"`
	IncludePredictions bool     `json:"include_predictions"`
	Limit              *int     `json:"limit"`
	Offset             int      `json:"offset"`
}

// AgentEvalPrediction is one prediction (predict_and_score call) of a run
// with agent spans.
type AgentEvalPrediction struct {
	PredictAndScoreCallID string   `json:"predict_and_score_call_id"`
	Status                string   `json:"status"`
	Example               any      `json:"example"`
	Output                any      `json:"output"`
	Scores                any      `json:"scores"`
	ModelLatency          any      `json:"model_latency"`
	Kind                  string   `json:"kind,omitempty"`
	RowDigest             string   `json:"row_digest,omitempty"`
	ExampleID             string   `json:"example_id,omitempty"`
	TrialIndex            any      `json:"trial_index,omitempty"`
	ConversationIDs       []string `json:"conversation_ids"`
	TraceIDs              []string `json:"trace_ids"`
	SpanCount             int      `json:"span_count"`
}

// AgentEvalRun is an evaluation run with the agent activity linked to it.
type AgentEvalRun struct {
	CallID            string                 `json:"call_id"`
	DisplayName       *string                `json:"display_name"`
	EvaluationRef     *string                `json:"evaluation_ref"`
	EvaluationName    string                 `json:"evaluation_name"`
	ModelRef          *string                `json:"model_ref"`
	ModelName         string                 `json:"model_name"`
	StartedAt         *string                `json:"started_at"`
	EndedAt           *string                `json:"ended_at"`
	Status            string                 `json:"status"`
	Summary           any                    `json:"summary"`
	PredictionCount   int                    `json:"prediction_count"`
	SpanCount         int64                  `json:"span_count"`
	ErrorCount        int64                  `json:"error_count"`
	TurnCount         int64                  `json:"turn_count"`
	TraceCount        int                    `json:"trace_count"`
	ConversationCount int                    `json:"conversation_count"`
	ConversationIDs   []string               `json:"conversation_ids"`
	AgentNames        []string               `json:"agent_names"`
	FirstSeen         *string                `json:"first_seen"`
	LastSeen          *string                `json:"last_seen"`
	TotalInputTokens  int64                  `json:"total_input_tokens"`
	TotalOutputTokens int64                  `json:"total_output_tokens"`
	TotalCostUSD      *float64               `json:"total_cost_usd"`
	Predictions       []*AgentEvalPrediction `json:"predictions,omitempty"`
}

func refNameOf(v any) (*string, string) {
	switch t := v.(type) {
	case string:
		if refs.IsRef(t) {
			if r, err := refs.Parse(t); err == nil {
				return &t, r.Name
			}
		}
		return nil, t
	case map[string]any:
		if n, ok := t["name"].(string); ok {
			return nil, n
		}
	}
	return nil, ""
}

func callStatusOf(c *Call) string {
	switch {
	case c == nil:
		return "unknown"
	case c.Exception != nil:
		return "error"
	case c.EndedAt == nil:
		return "running"
	}
	return "success"
}

func capStrings(m map[string]bool, n int) []string {
	out := sortedKeys(m)
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func predictionOf(pas string, c *Call) *AgentEvalPrediction {
	p := &AgentEvalPrediction{PredictAndScoreCallID: pas, Status: callStatusOf(c), ConversationIDs: []string{}, TraceIDs: []string{}}
	if c == nil {
		return p
	}
	p.Example = c.Inputs["example"]
	if o, ok := c.Output.(map[string]any); ok {
		p.Output, p.Scores, p.ModelLatency = o["output"], o["scores"], o["model_latency"]
	} else {
		p.Output = c.Output
	}
	if w, ok := c.Attributes["weft"].(map[string]any); ok {
		if ev, ok := w["eval"].(map[string]any); ok {
			p.Kind, _ = ev["kind"].(string)
			p.RowDigest, _ = ev["row_digest"].(string)
			p.ExampleID, _ = ev["example_id"].(string)
			p.TrialIndex = ev["trial_index"]
		}
	}
	return p
}

// QueryAgentEvals lists evaluation runs that have agent spans, newest first,
// with the activity of those spans. Filters keep runs with a matching span
// (agent, conversation, traces); the totals then cover matching spans only.
func (s *Store) QueryAgentEvals(ctx context.Context, req AgentEvalsQueryReq) ([]*AgentEvalRun, int, error) {
	pid, err := refs.NormalizeProject(req.ProjectID)
	if err != nil {
		return nil, 0, &InvalidError{Msg: err.Error()}
	}
	var runFilter []string
	if req.EvaluationRunIDs != nil {
		runFilter = req.EvaluationRunIDs
		if len(runFilter) == 0 {
			return []*AgentEvalRun{}, 0, nil
		}
	}
	m, err := s.evalMembership(ctx, pid, runFilter, nil)
	if err != nil {
		return nil, 0, err
	}
	out := []*AgentEvalRun{}
	if len(m) == 0 {
		return out, 0, nil
	}
	lf := agentSpanFilter{AgentName: req.AgentName}
	if req.ConversationID != "" {
		lf.ConversationIDs = []string{req.ConversationID}
	}
	if req.TraceIDs != nil {
		lf.TraceIDs = req.TraceIDs
	}
	spans, err := s.loadAgentSpans(ctx, pid, lf)
	if err != nil {
		return nil, 0, err
	}
	type runAcc struct {
		agg    *spanAgg
		pases  map[string]bool
		traces map[string]bool
	}
	type predAcc struct {
		convs, traces map[string]bool
		spans         int
	}
	runs := map[string]*runAcc{}
	preds := map[string]*predAcc{}
	for _, sp := range spans {
		seenRun := map[string]bool{}
		for _, r := range m[sp.ID] {
			ra := runs[r.Run]
			if ra == nil {
				ra = &runAcc{agg: newSpanAgg(), pases: map[string]bool{}, traces: map[string]bool{}}
				runs[r.Run] = ra
			}
			if !seenRun[r.Run] {
				seenRun[r.Run] = true
				ra.agg.add(sp)
				ra.traces[sp.TraceID] = true
			}
			if r.Pas == "" {
				continue
			}
			ra.pases[r.Pas] = true
			pa := preds[r.Pas]
			if pa == nil {
				pa = &predAcc{convs: map[string]bool{}, traces: map[string]bool{}}
				preds[r.Pas] = pa
			}
			pa.spans++
			pa.traces[sp.TraceID] = true
			if sp.ConvID != "" {
				pa.convs[sp.ConvID] = true
			}
		}
	}
	ids := make([]string, 0, len(runs))
	for id := range runs {
		ids = append(ids, id)
	}
	calls, err := s.callsByID(ctx, pid, ids)
	if err != nil {
		return nil, 0, err
	}
	for _, id := range ids {
		ra := runs[id]
		ra.agg.finish()
		a := ra.agg
		c := calls[id]
		r := &AgentEvalRun{CallID: id, Status: callStatusOf(c), PredictionCount: len(ra.pases), SpanCount: a.spans, ErrorCount: a.errors,
			TurnCount: a.turns, TraceCount: len(ra.traces), ConversationCount: len(a.convs), ConversationIDs: capStrings(a.convs, 50),
			AgentNames: sortedKeys(a.agents), FirstSeen: microsPtr(a.first), LastSeen: microsPtr(a.last),
			TotalInputTokens: int64(a.inTok), TotalOutputTokens: int64(a.outTok)}
		if req.IncludeCosts {
			r.TotalCostUSD = a.cost()
		}
		if c != nil {
			r.DisplayName, r.StartedAt, r.EndedAt, r.Summary = c.DisplayName, &c.StartedAt, c.EndedAt, c.Output
			r.EvaluationRef, r.EvaluationName = refNameOf(c.Inputs["self"])
			r.ModelRef, r.ModelName = refNameOf(c.Inputs["model"])
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := "", ""
		if out[i].StartedAt != nil {
			a = *out[i].StartedAt
		} else if out[i].FirstSeen != nil {
			a = *out[i].FirstSeen
		}
		if out[j].StartedAt != nil {
			b = *out[j].StartedAt
		} else if out[j].FirstSeen != nil {
			b = *out[j].FirstSeen
		}
		if a != b {
			return a > b
		}
		return out[i].CallID < out[j].CallID
	})
	total := len(out)
	out = page(out, req.Limit, req.Offset, 50)
	if req.IncludePredictions || req.ConversationID != "" || req.TraceIDs != nil {
		var pasIDs []string
		for _, r := range out {
			pasIDs = append(pasIDs, sortedKeys(runs[r.CallID].pases)...)
		}
		pcalls, err := s.callsByID(ctx, pid, pasIDs)
		if err != nil {
			return nil, 0, err
		}
		for _, r := range out {
			r.Predictions = []*AgentEvalPrediction{}
			for _, pas := range sortedKeys(runs[r.CallID].pases) {
				p := predictionOf(pas, pcalls[pas])
				pa := preds[pas]
				p.ConversationIDs, p.TraceIDs, p.SpanCount = sortedKeys(pa.convs), sortedKeys(pa.traces), pa.spans
				r.Predictions = append(r.Predictions, p)
			}
			sort.SliceStable(r.Predictions, func(i, j int) bool {
				ci, cj := pcalls[r.Predictions[i].PredictAndScoreCallID], pcalls[r.Predictions[j].PredictAndScoreCallID]
				if ci != nil && cj != nil && ci.StartedAt != cj.StartedAt {
					return ci.StartedAt < cj.StartedAt
				}
				return r.Predictions[i].PredictAndScoreCallID < r.Predictions[j].PredictAndScoreCallID
			})
		}
	}
	return out, total, nil
}

// ---------------------------------------------------------------- /agents/evals/conversations

// AgentEvalConversationsReq is the body of /agents/evals/conversations (Weft).
type AgentEvalConversationsReq struct {
	ProjectID string `json:"project_id"`
	AgentEvalFilter
	AgentName    string   `json:"agent_name"`
	IncludeCosts bool     `json:"include_costs"`
	SortBy       []SortBy `json:"sort_by"`
	Limit        *int     `json:"limit"`
	Offset       int      `json:"offset"`
}

// AgentEvalConversation is a conversation group plus the predictions it
// belongs to.
type AgentEvalConversation struct {
	ConversationID string `json:"conversation_id"`
	*AgentSpanGroup
	Predictions []*AgentEvalPrediction `json:"predictions"`
}

// AgentEvalConversationsRes is the response of /agents/evals/conversations.
type AgentEvalConversationsRes struct {
	Conversations []*AgentEvalConversation `json:"conversations"`
	TotalCount    int                      `json:"total_count"`
	// Spans of the run outside any conversation.
	OtherSpanCount int `json:"other_span_count"`
	SpanCount      int `json:"span_count"`
}

// EvalConversations lists the conversations of an evaluation run (or one
// prediction), most recent first, with the run's predictions in each.
func (s *Store) EvalConversations(ctx context.Context, req AgentEvalConversationsReq) (*AgentEvalConversationsRes, error) {
	pid, err := refs.NormalizeProject(req.ProjectID)
	if err != nil {
		return nil, &InvalidError{Msg: err.Error()}
	}
	if !req.AgentEvalFilter.active() {
		return nil, Invalidf("evaluation_run_id or predict_and_score_call_id is required")
	}
	var runs, pases []string
	if req.EvaluationRunID != "" {
		runs = []string{req.EvaluationRunID}
	}
	if req.PredictAndScoreCallID != "" {
		pases = []string{req.PredictAndScoreCallID}
	}
	m, err := s.evalMembership(ctx, pid, runs, pases)
	if err != nil {
		return nil, err
	}
	res := &AgentEvalConversationsRes{Conversations: []*AgentEvalConversation{}}
	if len(m) == 0 {
		return res, nil
	}
	spans, err := s.loadAgentSpans(ctx, pid, agentSpanFilter{AgentName: req.AgentName})
	if err != nil {
		return nil, err
	}
	var conv []*agentSpan
	pasByConv := map[string]map[string]bool{}
	for _, sp := range spans {
		refs := m[sp.ID]
		if len(refs) == 0 {
			continue
		}
		res.SpanCount++
		if sp.ConvID == "" {
			res.OtherSpanCount++
			continue
		}
		conv = append(conv, sp)
		for _, r := range refs {
			if r.Pas == "" {
				continue
			}
			if pasByConv[sp.ConvID] == nil {
				pasByConv[sp.ConvID] = map[string]bool{}
			}
			pasByConv[sp.ConvID][r.Pas] = true
		}
	}
	groups, err := groupSpans(conv, []AgentValueRef{{Key: "conversation_id"}}, nil, req.IncludeCosts)
	if err != nil {
		return nil, err
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
	pg := page(groups, req.Limit, req.Offset, 50)
	if err := s.fillGroupPreviews(ctx, pid, pg); err != nil {
		return nil, err
	}
	var pasIDs []string
	seen := map[string]bool{}
	for _, g := range pg {
		id, _ := g.GroupKeys["conversation_id"].(string)
		for p := range pasByConv[id] {
			if !seen[p] {
				seen[p] = true
				pasIDs = append(pasIDs, p)
			}
		}
	}
	pcalls, err := s.callsByID(ctx, pid, pasIDs)
	if err != nil {
		return nil, err
	}
	for _, g := range pg {
		id, _ := g.GroupKeys["conversation_id"].(string)
		c := &AgentEvalConversation{ConversationID: id, AgentSpanGroup: g, Predictions: []*AgentEvalPrediction{}}
		for _, p := range sortedKeys(pasByConv[id]) {
			pr := predictionOf(p, pcalls[p])
			pr.ConversationIDs = []string{id}
			c.Predictions = append(c.Predictions, pr)
		}
		res.Conversations = append(res.Conversations, c)
	}
	return res, nil
}

// MarshalJSON flattens the group fields next to conversation_id and predictions.
func (c *AgentEvalConversation) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(c.AgentSpanGroup)
	if err != nil {
		return nil, err
	}
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	m["conversation_id"], _ = json.Marshal(c.ConversationID)
	m["predictions"], _ = json.Marshal(c.Predictions)
	return json.Marshal(m)
}

// evalLinkCount is used by tests: links recorded for a project.
func (s *Store) evalLinkCount(ctx context.Context, pid string) (int, error) {
	var n int
	err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_eval_links WHERE project_id = ?`, pid).Scan(&n)
	return n, err
}
