package evals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// Summarize aggregates per-row scores the way the SDKs do:
// bools -> {true_count, true_fraction}, numbers -> {mean}, dicts recursively.
func Summarize(values []any) any {
	var bools, nums []float64
	var dicts []map[string]any
	for _, v := range values {
		switch t := v.(type) {
		case bool:
			b := 0.0
			if t {
				b = 1
			}
			bools = append(bools, b)
		case map[string]any:
			dicts = append(dicts, t)
		default:
			if f, ok := store.ToFloat(v); ok {
				nums = append(nums, f)
			}
		}
	}
	switch {
	case len(bools) > 0 && len(nums) == 0 && len(dicts) == 0:
		var c float64
		for _, b := range bools {
			c += b
		}
		return map[string]any{"true_count": int(c), "true_fraction": c / float64(len(bools))}
	case len(nums) > 0 && len(dicts) == 0:
		all := append(nums, bools...)
		var s float64
		for _, n := range all {
			s += n
		}
		return map[string]any{"mean": s / float64(len(all))}
	case len(dicts) > 0:
		keys := map[string]bool{}
		for _, d := range dicts {
			for k := range d {
				keys[k] = true
			}
		}
		out := map[string]any{}
		for k := range keys {
			var vs []any
			for _, d := range dicts {
				if v, ok := d[k]; ok {
					vs = append(vs, v)
				}
			}
			if s := Summarize(vs); s != nil {
				out[k] = s
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	return nil
}

func (e *Engine) setStatus(id string, fn func(*Status)) {
	e.statusMu.Lock()
	defer e.statusMu.Unlock()
	st := e.status[id]
	if st == nil {
		st = &Status{Code: "running"}
		e.status[id] = st
	}
	fn(st)
}

// EvaluationStatus reports progress; falls back to stored calls after restarts.
func (e *Engine) EvaluationStatus(ctx context.Context, projectID, callID string) (Status, error) {
	e.statusMu.Lock()
	if st, ok := e.status[callID]; ok {
		cp := *st
		e.statusMu.Unlock()
		return cp, nil
	}
	e.statusMu.Unlock()
	c, err := e.Store.GetCall(ctx, projectID, callID, false, false, false)
	if errors.Is(err, store.ErrNotFound) {
		return Status{Code: "unknown"}, nil
	}
	if err != nil {
		return Status{}, err
	}
	n, _, err := e.Store.CountCalls(ctx, projectID, &store.CallsFilter{ParentIDs: []string{callID}, OpNames: []string{"Evaluation.predict_and_score"}}, nil, "")
	if err != nil {
		return Status{}, err
	}
	st := Status{Code: "running", CompletedRows: int(n), TotalRows: int(n)}
	if c.EndedAt != nil {
		st.Code = "complete"
		if c.Exception != nil {
			st.Code = "failed"
			st.Error = *c.Exception
		}
	}
	return st, nil
}

type evalPlan struct {
	projectID  string
	evalRef    string
	modelRef   string
	model      map[string]any
	datasetRef string
	rows       []store.TableRow
	scorers    []*Scorer
	trials     int
}

// EvaluateModel validates inputs, records the root call and runs the
// evaluation in the background. It returns the root call id.
func (e *Engine) EvaluateModel(ctx context.Context, projectID, evaluationRef, modelRef string, userID *string) (string, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return "", &store.InvalidError{Msg: err.Error()}
	}
	evalObj, evalVal, err := e.loadObject(ctx, refs.Normalize(evaluationRef))
	if err != nil {
		return "", err
	}
	modelObj, modelVal, err := e.loadObject(ctx, refs.Normalize(modelRef))
	if err != nil {
		return "", err
	}
	if c := className(modelVal); c != "LLMStructuredCompletionModel" {
		return "", store.Invalidf("model class %q cannot run on the server; use an LLMStructuredCompletionModel", c)
	}
	plan := &evalPlan{projectID: pid, evalRef: evalObj.Ref(), modelRef: modelObj.Ref(), model: modelVal, trials: 1}
	if t, ok := store.ToFloat(evalVal["trials"]); ok && t >= 1 {
		plan.trials = min(int(t), 20)
	}
	dsRef, _ := evalVal["dataset"].(string)
	if dsRef == "" {
		return "", store.Invalidf("evaluation has no dataset")
	}
	dsObj, dsVal, err := e.loadObject(ctx, dsRef)
	if err != nil {
		return "", err
	}
	plan.datasetRef = dsObj.Ref()
	rowsRef, _ := dsVal["rows"].(string)
	rr, err := refs.Parse(rowsRef)
	if err != nil || rr.Kind != refs.KindTable {
		return "", store.Invalidf("dataset rows must be a table ref")
	}
	plan.rows, err = e.Store.QueryTable(ctx, store.TableQuery{ProjectID: rr.ProjectID(), Digest: rr.Name, Limit: 100000})
	if err != nil {
		return "", err
	}
	scorerRefs, _ := evalVal["scorers"].([]any)
	for _, s := range scorerRefs {
		sr, ok := s.(string)
		if !ok {
			return "", store.Invalidf("scorers must be refs to scorer objects")
		}
		sc, err := e.loadScorer(ctx, sr)
		if err != nil {
			return "", err
		}
		plan.scorers = append(plan.scorers, sc)
	}
	display := fmt.Sprintf("%s · %s", modelObj.ObjectID, evalObj.ObjectID)
	root, err := e.startCall(ctx, pid, "Evaluation.evaluate", nil,
		map[string]any{"self": plan.evalRef, "model": plan.modelRef},
		map[string]any{"weft": map[string]any{"server_side": true}}, &display)
	if err != nil {
		return "", err
	}
	_ = userID
	e.setStatus(root.id, func(s *Status) { s.TotalRows = len(plan.rows) * plan.trials })
	go e.runEvaluation(plan, root)
	return root.id, nil
}

func (e *Engine) runEvaluation(plan *evalPlan, root *callRec) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	type job struct {
		row   store.TableRow
		trial int
	}
	type result struct {
		scores  map[string]any
		latency float64
	}
	jobs := make(chan job)
	var mu sync.Mutex
	var results []result
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				scores, latency := e.predictAndScore(ctx, plan, root, j.row)
				mu.Lock()
				results = append(results, result{scores, latency})
				mu.Unlock()
				e.setStatus(root.id, func(s *Status) { s.CompletedRows++ })
			}
		}()
	}
	for t := 0; t < plan.trials; t++ {
		for _, r := range plan.rows {
			jobs <- job{r, t}
		}
	}
	close(jobs)
	wg.Wait()

	summary := map[string]any{}
	for _, sc := range plan.scorers {
		var vals []any
		for _, r := range results {
			if v, ok := r.scores[sc.Name]; ok && v != nil {
				vals = append(vals, v)
			}
		}
		if s := Summarize(vals); s != nil {
			summary[sc.Name] = s
		}
	}
	var lat float64
	for _, r := range results {
		lat += r.latency
	}
	if len(results) > 0 {
		summary["model_latency"] = map[string]any{"mean": lat / float64(len(results))}
	}
	if sumRec, err := e.startCall(ctx, plan.projectID, "Evaluation.summarize", root, map[string]any{"self": plan.evalRef}, nil, nil); err == nil {
		_ = sumRec.end(ctx, summary, nil, nil)
	}
	var endErr error
	if ctx.Err() != nil {
		endErr = ctx.Err()
	}
	if err := root.end(ctx, summary, nil, endErr); err != nil {
		e.Log.Error("ending evaluation call", "err", err)
	}
	e.setStatus(root.id, func(s *Status) {
		s.Code = "complete"
		if endErr != nil {
			s.Code, s.Error = "failed", endErr.Error()
		}
	})
}

func (e *Engine) predictAndScore(ctx context.Context, plan *evalPlan, root *callRec, row store.TableRow) (map[string]any, float64) {
	example, _ := row.Val.(map[string]any)
	if example == nil {
		example = map[string]any{"input": row.Val}
	}
	exampleRef := plan.datasetRef + "/attr/rows/id/" + row.Digest
	pas, err := e.startCall(ctx, plan.projectID, "Evaluation.predict_and_score", root,
		map[string]any{"self": plan.evalRef, "model": plan.modelRef, "example": exampleRef}, nil, nil)
	if err != nil {
		e.Log.Error("recording predict_and_score", "err", err)
		return nil, 0
	}
	start := time.Now()
	output, predErr := e.predict(ctx, plan, pas, example)
	latency := time.Since(start).Seconds()
	scores := map[string]any{}
	if predErr == nil {
		for _, sc := range plan.scorers {
			score, _, err := e.RunScorer(ctx, plan.projectID, sc, example, output, pas, nil)
			if err == nil {
				scores[sc.Name] = jsonSafe(score)
			}
		}
	}
	out := map[string]any{"output": output, "scores": scores, "model_latency": latency}
	_ = pas.end(ctx, out, nil, nil)
	return scores, latency
}

// predict runs an LLMStructuredCompletionModel on one example.
func (e *Engine) predict(ctx context.Context, plan *evalPlan, parent *callRec, example map[string]any) (any, error) {
	inputs := map[string]any{"self": plan.modelRef}
	for k, v := range example {
		inputs[k] = v
	}
	rec, err := e.startCall(ctx, plan.projectID, "LLMStructuredCompletionModel.predict", parent, inputs, nil, nil)
	if err != nil {
		return nil, err
	}
	output, predErr := e.PredictWith(ctx, plan.projectID, plan.model, example, rec)
	if err := rec.end(ctx, output, nil, predErr); err != nil {
		return nil, err
	}
	return output, predErr
}

// PredictWith runs a model value against variables.
func (e *Engine) PredictWith(ctx context.Context, projectID string, model map[string]any, vars map[string]any, parent *callRec) (any, error) {
	modelID, _ := model["llm_model_id"].(string)
	if modelID == "" {
		return nil, fmt.Errorf("model has no llm_model_id")
	}
	params, _ := model["default_params"].(map[string]any)
	var messages []any
	if tmpl, ok := params["messages_template"].([]any); ok && len(tmpl) > 0 {
		for _, m := range tmpl {
			mm, _ := m.(map[string]any)
			content := mm["content"]
			if s, ok := content.(string); ok {
				content = Render(s, vars)
			}
			messages = append(messages, map[string]any{"role": mm["role"], "content": content})
		}
	} else {
		text := ""
		for _, k := range []string{"input", "question", "prompt", "query", "text"} {
			if v, ok := vars[k]; ok {
				text = Stringify(v)
				break
			}
		}
		if text == "" {
			b, _ := json.Marshal(vars)
			text = string(b)
		}
		messages = []any{map[string]any{"role": "user", "content": text}}
	}
	inputs := map[string]any{"model": modelID, "messages": messages}
	jsonMode := false
	for _, k := range []string{"temperature", "max_tokens", "top_p", "frequency_penalty", "presence_penalty", "stop"} {
		if v, ok := params[k]; ok && v != nil {
			inputs[k] = v
		}
	}
	switch rf := params["response_format"].(type) {
	case string:
		if rf == "json_object" {
			inputs["response_format"] = map[string]any{"type": "json_object"}
			jsonMode = true
		}
	case map[string]any:
		inputs["response_format"] = rf
		jsonMode = rf["type"] == "json_object" || rf["type"] == "json_schema"
	}
	resp, _, err := e.TrackedCompletion(ctx, projectID, parent, inputs, true)
	if err != nil {
		return nil, err
	}
	text := outputText(resp)
	if jsonMode {
		if v, err := refs.Decode([]byte(strings.TrimSpace(text))); err == nil {
			return v, nil
		}
	}
	return text, nil
}

// ---------------------------------------------------------------- compare

// CompareEvaluation is one evaluation run in a comparison.
type CompareEvaluation struct {
	CallID         string   `json:"call_id"`
	DisplayName    *string  `json:"display_name"`
	EvaluationRef  string   `json:"evaluation_ref"`
	EvaluationName string   `json:"evaluation_name"`
	ModelRef       string   `json:"model_ref"`
	ModelName      string   `json:"model_name"`
	StartedAt      string   `json:"started_at"`
	EndedAt        *string  `json:"ended_at"`
	Status         string   `json:"status"`
	Summary        any      `json:"summary"`
	DatasetRef     *string  `json:"dataset_ref"`
	Scorers        []string `json:"scorers"`
	TotalTokens    float64  `json:"total_tokens"`
	CostUSD        float64  `json:"cost_usd"`
	RowCount       int      `json:"row_count"`
}

// CompareMetric is one summary metric across evaluations.
type CompareMetric struct {
	Key    string         `json:"key"`
	Scorer string         `json:"scorer"`
	Path   string         `json:"path"`
	Kind   string         `json:"kind"`
	Values map[string]any `json:"values"`
}

// CompareRowResult is one model's result on one example.
type CompareRowResult struct {
	CallID       string         `json:"call_id"`
	Output       any            `json:"output"`
	Scores       map[string]any `json:"scores"`
	ModelLatency any            `json:"model_latency"`
	TotalTokens  float64        `json:"total_tokens"`
	Trial        int            `json:"trial"`
}

// CompareRow is one dataset example across evaluations.
type CompareRow struct {
	RowKey  string                        `json:"row_key"`
	Inputs  any                           `json:"inputs"`
	Results map[string][]CompareRowResult `json:"results"`
}

// Comparison is the /evaluations/compare response.
type Comparison struct {
	Evaluations []CompareEvaluation `json:"evaluations"`
	Metrics     []CompareMetric     `json:"metrics"`
	Rows        []*CompareRow       `json:"rows"`
}

func flattenMetrics(prefix []string, v any, fn func(path []string, val any)) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			flattenMetrics(append(append([]string{}, prefix...), k), t[k], fn)
		}
	case bool:
		fn(prefix, t)
	default:
		if f, ok := store.ToFloat(v); ok {
			fn(prefix, f)
		}
	}
}

// Compare assembles a comparison of evaluation calls.
func (e *Engine) Compare(ctx context.Context, projectID string, callIDs []string) (*Comparison, error) {
	pid, err := refs.NormalizeProject(projectID)
	if err != nil {
		return nil, &store.InvalidError{Msg: err.Error()}
	}
	if len(callIDs) == 0 || len(callIDs) > 50 {
		return nil, store.Invalidf("evaluation_call_ids must list 1 to 50 calls")
	}
	cmp := &Comparison{Evaluations: []CompareEvaluation{}, Metrics: []CompareMetric{}, Rows: []*CompareRow{}}
	prices, err := e.Store.LoadPriceBook(ctx, pid)
	if err != nil {
		return nil, err
	}
	metricIdx := map[string]*CompareMetric{}
	var metricOrder []string
	for _, id := range callIDs {
		c, err := e.Store.GetCall(ctx, pid, id, true, false, false)
		if errors.Is(err, store.ErrNotFound) {
			return nil, store.Invalidf("evaluation call %q not found", id)
		}
		if err != nil {
			return nil, err
		}
		ce := CompareEvaluation{CallID: c.ID, DisplayName: c.DisplayName, StartedAt: c.StartedAt, EndedAt: c.EndedAt, Status: c.Status(),
			Summary: c.Output, Scorers: []string{}}
		ce.EvaluationRef, _ = c.Inputs["self"].(string)
		ce.EvaluationName = refName(c.Inputs["self"])
		if s, ok := c.Inputs["model"].(string); ok {
			ce.ModelRef = s
		}
		ce.ModelName = refName(c.Inputs["model"])
		usage := c.TotalUsage()
		ce.TotalTokens = usage.Tokens()
		ce.CostUSD = prices.TotalCost(usage, c.StartedMicros())
		if refs.IsRef(ce.EvaluationRef) {
			if _, ev, err := e.loadObject(ctx, ce.EvaluationRef); err == nil {
				if ds, ok := ev["dataset"].(string); ok {
					ce.DatasetRef = &ds
				}
				if sc, ok := ev["scorers"].([]any); ok {
					for _, s := range sc {
						ce.Scorers = append(ce.Scorers, refName(s))
					}
				}
			}
		}
		if sm, ok := c.Output.(map[string]any); ok {
			if len(ce.Scorers) == 0 {
				for k := range sm {
					if k != "model_latency" {
						ce.Scorers = append(ce.Scorers, k)
					}
				}
				sort.Strings(ce.Scorers)
			}
			flattenMetrics(nil, sm, func(path []string, val any) {
				if len(path) < 2 {
					return
				}
				key := strings.Join(path, ".")
				m := metricIdx[key]
				if m == nil {
					kind := "other"
					switch path[len(path)-1] {
					case "true_fraction":
						kind = "fraction"
					case "mean":
						kind = "mean"
					}
					m = &CompareMetric{Key: key, Scorer: path[0], Path: strings.Join(path[1:], "."), Kind: kind, Values: map[string]any{}}
					metricIdx[key] = m
					metricOrder = append(metricOrder, key)
				}
				m.Values[c.ID] = val
			})
		}
		cmp.Evaluations = append(cmp.Evaluations, ce)
	}
	for _, k := range metricOrder {
		cmp.Metrics = append(cmp.Metrics, *metricIdx[k])
	}

	children, err := e.Store.ListCalls(ctx, store.CallsQuery{ProjectID: pid,
		Filter: &store.CallsFilter{ParentIDs: callIDs, OpNames: []string{"Evaluation.predict_and_score"}},
		SortBy: []store.SortBy{{Field: "started_at", Direction: "asc"}}})
	if err != nil {
		return nil, err
	}
	// Resolve example refs in one batch.
	var exRefs []string
	seen := map[string]bool{}
	for _, ch := range children {
		if s, ok := ch.Inputs["example"].(string); ok && refs.IsRef(s) && !seen[s] {
			seen[s] = true
			exRefs = append(exRefs, s)
		}
	}
	vals, err := e.Store.ReadRefs(ctx, exRefs)
	if err != nil {
		return nil, err
	}
	resolved := map[string]any{}
	for i, r := range exRefs {
		resolved[r] = vals[i]
	}
	rowIdx := map[string]*CompareRow{}
	trials := map[string]int{}
	for _, ch := range children {
		ex := ch.Inputs["example"]
		var key string
		var inputs any
		if s, ok := ex.(string); ok && refs.IsRef(s) {
			r, _ := refs.Parse(s)
			key = s
			if n := len(r.Extra); n >= 2 && r.Extra[n-2] == "id" {
				key = r.Extra[n-1]
			}
			inputs = resolved[s]
		} else {
			d, _, _ := refs.Digest(ex)
			key, inputs = d, ex
		}
		row := rowIdx[key]
		if row == nil {
			row = &CompareRow{RowKey: key, Inputs: inputs, Results: map[string][]CompareRowResult{}}
			rowIdx[key] = row
			cmp.Rows = append(cmp.Rows, row)
		}
		parent := ""
		if ch.ParentID != nil {
			parent = *ch.ParentID
		}
		res := CompareRowResult{CallID: ch.ID, Scores: map[string]any{}, TotalTokens: ch.TotalUsage().Tokens(), Trial: trials[parent+key]}
		trials[parent+key]++
		if out, ok := ch.Output.(map[string]any); ok {
			res.Output = out["output"]
			if s, ok := out["scores"].(map[string]any); ok {
				res.Scores = s
			}
			res.ModelLatency = out["model_latency"]
		}
		row.Results[parent] = append(row.Results[parent], res)
	}
	for i := range cmp.Evaluations {
		for _, row := range cmp.Rows {
			cmp.Evaluations[i].RowCount += len(row.Results[cmp.Evaluations[i].CallID])
		}
	}
	return cmp, nil
}

// ---------------------------------------------------------------- leaderboard

// LeaderboardColumn is a resolved leaderboard column.
type LeaderboardColumn struct {
	Index               int    `json:"index"`
	EvaluationObjectRef string `json:"evaluation_object_ref"`
	EvaluationName      string `json:"evaluation_name"`
	ScorerName          string `json:"scorer_name"`
	SummaryMetricPath   string `json:"summary_metric_path"`
	ShouldMinimize      bool   `json:"should_minimize"`
}

// LeaderboardCell is one model's value for a column.
type LeaderboardCell struct {
	Value            any    `json:"value"`
	EvaluationCallID string `json:"evaluation_call_id"`
	StartedAt        string `json:"started_at"`
}

// LeaderboardRow is one model.
type LeaderboardRow struct {
	ModelRef  string                     `json:"model_ref"`
	ModelName string                     `json:"model_name"`
	Values    map[string]LeaderboardCell `json:"values"`
}

// Leaderboard is the /leaderboard/compute response.
type Leaderboard struct {
	Leaderboard *store.Obj          `json:"leaderboard"`
	Columns     []LeaderboardColumn `json:"columns"`
	Rows        []*LeaderboardRow   `json:"rows"`
}

func pathValue(v any, path []string) (any, bool) {
	cur := v
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// ComputeLeaderboard evaluates a Leaderboard object.
func (e *Engine) ComputeLeaderboard(ctx context.Context, projectID, objectID, digest string) (*Leaderboard, error) {
	if digest == "" {
		digest = "latest"
	}
	obj, err := e.Store.ReadObject(ctx, projectID, objectID, digest)
	if err != nil {
		return nil, err
	}
	val, _ := obj.Val.(map[string]any)
	lb := &Leaderboard{Leaderboard: obj, Columns: []LeaderboardColumn{}, Rows: []*LeaderboardRow{}}
	cols, _ := val["columns"].([]any)
	rowIdx := map[string]*LeaderboardRow{}
	evalCalls := map[string][]*store.Call{}
	for i, c := range cols {
		cm, _ := c.(map[string]any)
		col := LeaderboardColumn{Index: i}
		col.EvaluationObjectRef, _ = cm["evaluation_object_ref"].(string)
		col.EvaluationObjectRef = refs.Normalize(col.EvaluationObjectRef)
		col.ScorerName, _ = cm["scorer_name"].(string)
		col.SummaryMetricPath, _ = cm["summary_metric_path"].(string)
		col.ShouldMinimize, _ = cm["should_minimize"].(bool)
		col.EvaluationName = refName(col.EvaluationObjectRef)
		lb.Columns = append(lb.Columns, col)
		calls, ok := evalCalls[col.EvaluationName]
		if !ok {
			calls, err = e.Store.ListCalls(ctx, store.CallsQuery{ProjectID: obj.ProjectID,
				Filter: &store.CallsFilter{OpNames: []string{"Evaluation.evaluate"}},
				SortBy: []store.SortBy{{Field: "started_at", Direction: "desc"}}})
			if err != nil {
				return nil, err
			}
			var keep []*store.Call
			for _, call := range calls {
				if call.EndedAt == nil || call.Exception != nil {
					continue
				}
				if refName(call.Inputs["self"]) == col.EvaluationName {
					keep = append(keep, call)
				}
			}
			evalCalls[col.EvaluationName] = keep
			calls = keep
		}
		path := []string{col.ScorerName}
		if col.SummaryMetricPath != "" {
			path = append(path, strings.Split(col.SummaryMetricPath, ".")...)
		}
		for _, call := range calls { // newest first
			modelRef, _ := call.Inputs["model"].(string)
			if modelRef == "" {
				modelRef = refName(call.Inputs["model"])
			}
			row := rowIdx[modelRef]
			if row == nil {
				row = &LeaderboardRow{ModelRef: modelRef, ModelName: refName(call.Inputs["model"]), Values: map[string]LeaderboardCell{}}
				rowIdx[modelRef] = row
				lb.Rows = append(lb.Rows, row)
			}
			key := fmt.Sprint(i)
			if _, done := row.Values[key]; done {
				continue
			}
			if v, ok := pathValue(call.Output, path); ok {
				row.Values[key] = LeaderboardCell{Value: v, EvaluationCallID: call.ID, StartedAt: call.StartedAt}
			}
		}
	}
	return lb, nil
}
