package compat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/evals"
	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// The v2 REST API under /v2/{entity}/{project}/, mapped onto Weft's
// objects and calls:
//
//   ops, datasets, models, scorers, evaluations  -> versioned objects
//   evaluation_runs -> Evaluation.evaluate calls
//   predictions     -> Evaluation.predict_and_score calls (with a <model>.predict child),
//                      or a lone <model>.predict call outside a run
//   scores          -> <scorer>.score calls under the prediction, plus
//                      weft.scorer.<scorer> feedback on it
//
// so v2 runs appear in /evaluations/compare, leaderboards and the UI.

// mountV2 registers the v2 routes.
func (s *Routes) mountV2(r chi.Router) {
	{
		const p = "/v2/{entity}/{project}"
		for _, k := range v2Kinds {
			r.Post(p+"/"+k.path, s.v2ObjCreate(k))
			r.Get(p+"/"+k.path, s.v2ObjList(k))
			r.Get(p+"/"+k.path+"/{object_id}", s.v2ObjRead(k))
			r.Delete(p+"/"+k.path+"/{object_id}", s.v2ObjDelete(k))
			r.Get(p+"/"+k.path+"/{object_id}/versions/{digest}", s.v2ObjRead(k))
		}
		r.Post(p+"/evaluation_runs", s.v2RunCreate)
		r.Get(p+"/evaluation_runs", s.v2RunList)
		r.Delete(p+"/evaluation_runs", s.v2CallsDelete("evaluation_run_ids"))
		r.Get(p+"/evaluation_runs/{id}", s.v2RunRead)
		r.Post(p+"/evaluation_runs/{id}/finish", s.v2RunFinish)
		r.Post(p+"/predictions", s.v2PredictionCreate)
		r.Get(p+"/predictions", s.v2PredictionList)
		r.Delete(p+"/predictions", s.v2CallsDelete("prediction_ids"))
		r.Get(p+"/predictions/{id}", s.v2PredictionRead)
		r.Post(p+"/predictions/{id}/finish", s.v2PredictionFinish)
		r.Post(p+"/scores", s.v2ScoreCreate)
		r.Get(p+"/scores", s.v2ScoreList)
		r.Delete(p+"/scores", s.v2CallsDelete("score_ids"))
		r.Get(p+"/scores/{id}", s.v2ScoreRead)
		r.Post(p+"/eval_results/query", s.v2EvalResults)
	}
}

func v2Project(r *http.Request) (string, error) {
	return project(pathParam(r, "entity") + "/" + pathParam(r, "project"))
}

// v2Handle decodes a JSON body, runs fn with the path's project and writes the result.
func v2Handle[T any](fn func(ctx context.Context, r *http.Request, pid string, req *T) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pid, err := v2Project(r)
		if err != nil {
			writeErr(w, err)
			return
		}
		req := new(T)
		if r.Method == http.MethodPost {
			if err := decodeBody(r, req); err != nil {
				writeErr(w, err)
				return
			}
		}
		out, err := fn(r.Context(), r, pid, req)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, out)
	}
}

func writeJSONL[T any](w http.ResponseWriter, items []T) {
	w.Header().Set("Content-Type", "application/jsonl")
	w.WriteHeader(http.StatusOK) // an empty stream is a 200 too
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, it := range items {
		if err := enc.Encode(it); err != nil {
			return
		}
	}
}

// queryList reads a repeated (or comma-separated) query parameter.
func queryList(r *http.Request, name string) []string {
	var out []string
	for _, v := range r.URL.Query()[name] {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func queryInt(r *http.Request, name string, def int) int {
	if v, err := strconv.Atoi(r.URL.Query().Get(name)); err == nil && v >= 0 {
		return v
	}
	return def
}

// ---------------------------------------------------------------- objects

type v2Kind struct {
	path  string
	class string // base_object_class filter ("" with op)
	op    bool
	// build turns a create body into an object id and value.
	build func(ctx context.Context, s *Routes, pid string, body map[string]any) (string, map[string]any, error)
	// view renders an object version.
	view func(o *store.Obj, val map[string]any) map[string]any
}

func str(m map[string]any, k string) string { v, _ := m[k].(string); return v }

func strOrNil(m map[string]any, k string) any {
	if v, ok := m[k].(string); ok {
		return v
	}
	return nil
}

var defNameRe = regexp.MustCompile(`(?m)^\s*(?:async\s+)?def\s+([A-Za-z_][A-Za-z0-9_]*)`)

func objectBase(o *store.Obj) map[string]any {
	return map[string]any{"object_id": o.ObjectID, "digest": o.Digest, "version_index": o.VersionIndex, "created_at": o.CreatedAt}
}

func requireName(body map[string]any) (string, error) {
	n := strings.TrimSpace(str(body, "name"))
	if n == "" {
		return "", store.Invalidf("name is required")
	}
	return n, nil
}

var v2Kinds = []v2Kind{
	{path: "ops", op: true,
		build: func(_ context.Context, _ *Routes, _ string, b map[string]any) (string, map[string]any, error) {
			code := str(b, "source_code")
			name := strings.TrimSpace(str(b, "name"))
			if name == "" {
				if m := defNameRe.FindStringSubmatch(code); m != nil {
					name = m[1]
				} else {
					name = "op"
				}
			}
			return store.SanitizeObjectID(name), map[string]any{"_type": "Op", "name": name, "code": code, "language": "python", "signature": ""}, nil
		},
		view: func(o *store.Obj, v map[string]any) map[string]any {
			out := objectBase(o)
			out["code"] = str(v, "code")
			return out
		}},
	{path: "datasets", class: "Dataset",
		build: func(ctx context.Context, s *Routes, pid string, b map[string]any) (string, map[string]any, error) {
			name, err := requireName(b)
			if err != nil {
				return "", nil, err
			}
			rows, ok := b["rows"].([]any)
			if !ok {
				return "", nil, store.Invalidf("rows must be a list of objects")
			}
			d, _, err := s.store.CreateTable(ctx, pid, rows)
			if err != nil {
				return "", nil, err
			}
			return store.SanitizeObjectID(name), map[string]any{"_type": "Dataset", "_class_name": "Dataset", "_bases": []any{"Object", "BaseModel"},
				"name": name, "description": b["description"], "rows": refs.TableRef(pid, d)}, nil
		},
		view: func(o *store.Obj, v map[string]any) map[string]any {
			out := objectBase(o)
			out["name"], out["description"], out["rows"] = nameOf(o, v), strOrNil(v, "description"), str(v, "rows")
			return out
		}},
	{path: "models", class: "Model",
		build: func(_ context.Context, _ *Routes, _ string, b map[string]any) (string, map[string]any, error) {
			name, err := requireName(b)
			if err != nil {
				return "", nil, err
			}
			if _, ok := b["source_code"].(string); !ok {
				return "", nil, store.Invalidf("source_code is required")
			}
			attrs, _ := b["attributes"].(map[string]any)
			val := map[string]any{"_type": "Model", "_class_name": "Model", "_bases": []any{"Model", "Object", "BaseModel"},
				"name": name, "description": b["description"], "source_code": b["source_code"], "attributes": attrs}
			return store.SanitizeObjectID(name), val, nil
		},
		view: func(o *store.Obj, v map[string]any) map[string]any {
			out := objectBase(o)
			out["name"], out["description"], out["source_code"], out["attributes"] = nameOf(o, v), strOrNil(v, "description"), str(v, "source_code"), v["attributes"]
			return out
		}},
	{path: "scorers", class: "Scorer",
		build: func(ctx context.Context, s *Routes, pid string, b map[string]any) (string, map[string]any, error) {
			name, err := requireName(b)
			if err != nil {
				return "", nil, err
			}
			code, ok := b["op_source_code"].(string)
			if !ok {
				return "", nil, store.Invalidf("op_source_code is required")
			}
			opID := store.SanitizeObjectID(name + ".score")
			d, _, err := s.store.CreateObject(ctx, pid, opID, map[string]any{"_type": "Op", "name": name + ".score", "code": code,
				"language": "python", "signature": ""}, "", nil)
			if err != nil {
				return "", nil, err
			}
			val := map[string]any{"_type": "Scorer", "_class_name": "Scorer", "_bases": []any{"Scorer", "Object", "BaseModel"},
				"name": name, "description": b["description"], "score": refs.ObjectRef(pid, opID, d, true)}
			return store.SanitizeObjectID(name), val, nil
		},
		view: func(o *store.Obj, v map[string]any) map[string]any {
			out := objectBase(o)
			out["name"], out["description"], out["score_op"] = nameOf(o, v), strOrNil(v, "description"), str(v, "score")
			return out
		}},
	{path: "evaluations", class: "Evaluation",
		build: func(_ context.Context, _ *Routes, _ string, b map[string]any) (string, map[string]any, error) {
			name, err := requireName(b)
			if err != nil {
				return "", nil, err
			}
			ds := str(b, "dataset")
			if !refs.IsRef(ds) {
				return "", nil, store.Invalidf("dataset must be a weft:/// object ref")
			}
			scorers := []any{}
			if xs, ok := b["scorers"].([]any); ok {
				for _, x := range xs {
					if sr, ok := x.(string); ok && refs.IsRef(sr) {
						scorers = append(scorers, sr)
					} else {
						return "", nil, store.Invalidf("scorers must be weft:/// refs")
					}
				}
			}
			trials := 1
			if t, ok := store.ToFloat(b["trials"]); ok && t >= 1 {
				trials = int(t)
			}
			val := map[string]any{"_type": "Evaluation", "_class_name": "Evaluation", "_bases": []any{"Object", "BaseModel"},
				"name": name, "description": b["description"], "dataset": ds, "scorers": scorers, "trials": trials,
				"evaluation_name": b["evaluation_name"], "metadata": b["eval_attributes"]}
			return store.SanitizeObjectID(name), val, nil
		},
		view: func(o *store.Obj, v map[string]any) map[string]any {
			out := objectBase(o)
			scorers := []string{}
			if xs, ok := v["scorers"].([]any); ok {
				for _, x := range xs {
					if sr, ok := x.(string); ok {
						scorers = append(scorers, sr)
					}
				}
			}
			trials := 1
			if t, ok := store.ToFloat(v["trials"]); ok {
				trials = int(t)
			}
			out["name"], out["description"], out["dataset"], out["scorers"], out["trials"] = nameOf(o, v), strOrNil(v, "description"), str(v, "dataset"), scorers, trials
			out["evaluation_name"], out["evaluate_op"], out["predict_and_score_op"], out["summarize_op"] = strOrNil(v, "evaluation_name"),
				strOrNil(v, "evaluate"), strOrNil(v, "predict_and_score"), strOrNil(v, "summarize")
			return out
		}},
}

func nameOf(o *store.Obj, v map[string]any) string {
	if n := str(v, "name"); n != "" {
		return n
	}
	return o.ObjectID
}

func (s *Routes) v2ObjCreate(k v2Kind) http.HandlerFunc {
	return v2Handle(func(ctx context.Context, r *http.Request, pid string, body *map[string]any) (any, error) {
		if *body == nil {
			*body = map[string]any{}
		}
		oid, val, err := k.build(ctx, s, pid, *body)
		if err != nil {
			return nil, err
		}
		d, v, err := s.store.CreateObject(ctx, pid, oid, val, "", s.host.User(r, nil))
		if err != nil {
			return nil, err
		}
		s.engine.InvalidateMonitors(pid)
		return map[string]any{"digest": d, "object_id": oid, "version_index": v}, nil
	})
}

func (s *Routes) v2Objects(ctx context.Context, pid string, k v2Kind, oid string, limit, offset int) ([]*store.Obj, error) {
	f := &store.ObjsFilter{LatestOnly: oid == ""}
	if k.op {
		t := true
		f.IsOp = &t
	} else {
		f.BaseObjectClasses = []string{k.class}
	}
	if oid != "" {
		f.ObjectIDs = []string{oid}
	}
	return s.store.QueryObjects(ctx, store.ObjsQuery{ProjectID: pid, Filter: f, Limit: limit, Offset: offset})
}

func (s *Routes) v2ObjList(k v2Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pid, err := v2Project(r)
		if err != nil {
			writeErr(w, err)
			return
		}
		objs, err := s.v2Objects(r.Context(), pid, k, "", queryInt(r, "limit", 1000), queryInt(r, "offset", 0))
		if err != nil {
			writeErr(w, err)
			return
		}
		out := make([]map[string]any, 0, len(objs))
		for _, o := range objs {
			v, _ := o.Val.(map[string]any)
			out = append(out, k.view(o, v))
		}
		writeJSONL(w, out)
	}
}

func (s *Routes) v2ObjRead(k v2Kind) http.HandlerFunc {
	return v2Handle(func(ctx context.Context, r *http.Request, pid string, _ *struct{}) (any, error) {
		digest := pathParam(r, "digest")
		if digest == "" {
			digest = "latest"
		}
		o, err := s.store.ReadObject(ctx, pid, pathParam(r, "object_id"), digest)
		if err != nil {
			return nil, err
		}
		if k.op != (o.Kind == "op") || (!k.op && (o.BaseObjectClass == nil || *o.BaseObjectClass != k.class)) {
			return nil, store.ErrNotFound
		}
		v, _ := o.Val.(map[string]any)
		return k.view(o, v), nil
	})
}

func (s *Routes) v2ObjDelete(k v2Kind) http.HandlerFunc {
	return v2Handle(func(ctx context.Context, r *http.Request, pid string, _ *struct{}) (any, error) {
		oid := pathParam(r, "object_id")
		objs, err := s.v2Objects(ctx, pid, k, oid, 1, 0)
		if err != nil {
			return nil, err
		}
		if len(objs) == 0 {
			return nil, store.ErrNotFound
		}
		n, err := s.store.DeleteObject(ctx, pid, oid, queryList(r, "digests"))
		if err != nil {
			return nil, err
		}
		s.engine.InvalidateMonitors(pid)
		return map[string]any{"num_deleted": n}, nil
	})
}

// ---------------------------------------------------------------- calls

const (
	opEvaluate        = "Evaluation.evaluate"
	opPredictAndScore = "Evaluation.predict_and_score"
)

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// v2Attr reads attributes.weft.<key>.
func v2Attr(c *store.Call, key string) any {
	w, _ := c.Attributes["weft"].(map[string]any)
	return w[key]
}

func (s *Routes) v2Call(ctx context.Context, pid, id, kind string) (*store.Call, error) {
	c, err := s.store.GetCall(ctx, pid, id, false, false, false)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "evaluation_run":
		if c.OpBaseName() != opEvaluate {
			return nil, store.ErrNotFound
		}
	default:
		if v2Attr(c, "v2") != kind {
			return nil, store.ErrNotFound
		}
	}
	return c, nil
}

func refBaseName(v any) string {
	sv, _ := v.(string)
	if r, err := refs.Parse(sv); err == nil {
		return r.Name
	}
	return sv
}

// -- evaluation runs

type v2RunCreateReq struct {
	Evaluation            string  `json:"evaluation"`
	Model                 string  `json:"model"`
	SourceEvaluationRunID *string `json:"source_evaluation_run_id"`
}

func (s *Routes) v2RunCreate(w http.ResponseWriter, r *http.Request) {
	v2Handle(func(ctx context.Context, r *http.Request, pid string, req *v2RunCreateReq) (any, error) {
		if !refs.IsRef(req.Evaluation) || !refs.IsRef(req.Model) {
			return nil, store.Invalidf("evaluation and model must be weft:/// refs")
		}
		evalRef, modelRef := req.Evaluation, req.Model
		weft := map[string]any{"v2": "evaluation_run"}
		if req.SourceEvaluationRunID != nil && *req.SourceEvaluationRunID != "" {
			weft["source_evaluation_run_id"] = *req.SourceEvaluationRunID
		}
		id := uuid.Must(uuid.NewV7()).String()
		display := refBaseName(modelRef) + " · " + refBaseName(evalRef)
		st := store.CallStart{ProjectID: pid, ID: id, TraceID: id, OpName: opEvaluate, StartedAt: now(), DisplayName: &display,
			Inputs: map[string]any{"self": evalRef, "model": modelRef}, Attributes: map[string]any{"weft": weft}, UserID: s.host.User(r, nil)}
		if err := s.store.StartCall(ctx, st); err != nil {
			return nil, err
		}
		return map[string]any{"evaluation_run_id": id}, nil
	})(w, r)
}

func runView(c *store.Call) map[string]any {
	status := "running"
	switch c.Status() {
	case "error":
		status = "failed"
	case "success", "descendant_error":
		status = "finished"
	}
	var summary any
	if m, ok := c.Output.(map[string]any); ok {
		summary = m
	}
	return map[string]any{"evaluation_run_id": c.ID, "evaluation": c.Inputs["self"], "model": c.Inputs["model"], "status": status,
		"started_at": c.StartedAt, "finished_at": c.EndedAt, "summary": summary, "source_evaluation_run_id": v2Attr(c, "source_evaluation_run_id")}
}

func (s *Routes) v2RunRead(w http.ResponseWriter, r *http.Request) {
	v2Handle(func(ctx context.Context, r *http.Request, pid string, _ *struct{}) (any, error) {
		c, err := s.v2Call(ctx, pid, pathParam(r, "id"), "evaluation_run")
		if err != nil {
			return nil, err
		}
		return runView(c), nil
	})(w, r)
}

func (s *Routes) v2RunList(w http.ResponseWriter, r *http.Request) {
	pid, err := v2Project(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	limit, offset := queryInt(r, "limit", 1000), queryInt(r, "offset", 0)
	f := &store.CallsFilter{OpNames: []string{opEvaluate}, CallIDs: queryList(r, "evaluation_run_ids")}
	calls, err := s.store.ListCalls(r.Context(), store.CallsQuery{ProjectID: pid, Filter: f})
	if err != nil {
		writeErr(w, err)
		return
	}
	evals, models := map[string]bool{}, map[string]bool{}
	for _, e := range queryList(r, "evaluations") {
		evals[e] = true
	}
	for _, m := range queryList(r, "models") {
		models[m] = true
	}
	out := []map[string]any{}
	for _, c := range calls {
		e, _ := c.Inputs["self"].(string)
		m, _ := c.Inputs["model"].(string)
		if (len(evals) > 0 && !evals[e]) || (len(models) > 0 && !models[m]) {
			continue
		}
		out = append(out, runView(c))
	}
	if offset >= len(out) {
		out = out[:0]
	} else {
		out = out[offset:min(len(out), offset+limit)]
	}
	writeJSONL(w, out)
}

type v2RunFinishReq struct {
	Summary map[string]any `json:"summary"`
}

func (s *Routes) v2RunFinish(w http.ResponseWriter, r *http.Request) {
	v2Handle(func(ctx context.Context, r *http.Request, pid string, req *v2RunFinishReq) (any, error) {
		c, err := s.v2Call(ctx, pid, pathParam(r, "id"), "evaluation_run")
		if err != nil {
			return nil, err
		}
		summary := req.Summary
		if summary == nil {
			if summary, err = s.v2Summarize(ctx, pid, c.ID); err != nil {
				return nil, err
			}
		}
		if err := s.store.EndCall(ctx, store.CallEnd{ProjectID: pid, ID: c.ID, EndedAt: now(), Output: summary}); err != nil {
			return nil, err
		}
		s.engine.Notify([]store.EndedCall{{ProjectID: pid, ID: c.ID}})
		return map[string]any{"success": true}, nil
	})(w, r)
}

// v2Summarize aggregates the scores of a run's finished predictions the way the SDKs do.
func (s *Routes) v2Summarize(ctx context.Context, pid, runID string) (map[string]any, error) {
	children, err := s.store.ListCalls(ctx, store.CallsQuery{ProjectID: pid,
		Filter: &store.CallsFilter{ParentIDs: []string{runID}, OpNames: []string{opPredictAndScore}}})
	if err != nil {
		return nil, err
	}
	byScorer := map[string][]any{}
	var latencies []any
	for _, ch := range children {
		out, ok := ch.Output.(map[string]any)
		if !ok {
			continue
		}
		if sc, ok := out["scores"].(map[string]any); ok {
			for k, v := range sc {
				byScorer[k] = append(byScorer[k], v)
			}
		}
		if l, ok := out["model_latency"]; ok && l != nil {
			latencies = append(latencies, l)
		}
	}
	summary := map[string]any{}
	for k, vs := range byScorer {
		if sm := evals.Summarize(vs); sm != nil {
			summary[k] = sm
		}
	}
	if sm := evals.Summarize(latencies); sm != nil {
		summary["model_latency"] = sm
	}
	return summary, nil
}

// -- predictions

type v2PredictionCreateReq struct {
	Model           string           `json:"model"`
	Inputs          map[string]any   `json:"inputs"`
	Output          any              `json:"output"`
	EvaluationRunID *string          `json:"evaluation_run_id"`
	GenAISpanRef    []map[string]any `json:"genai_span_ref"`
}

func (s *Routes) v2PredictionCreate(w http.ResponseWriter, r *http.Request) {
	v2Handle(func(ctx context.Context, r *http.Request, pid string, req *v2PredictionCreateReq) (any, error) {
		if req.Model == "" {
			return nil, store.Invalidf("model is required")
		}
		if req.Inputs == nil {
			return nil, store.Invalidf("inputs is required")
		}
		model := req.Model
		user := s.host.User(r, nil)
		ts := now()
		predictOp := refBaseName(model) + ".predict"
		predictInputs := map[string]any{"self": model}
		for k, v := range req.Inputs {
			if k != "self" {
				predictInputs[k] = v
			}
		}
		if req.EvaluationRunID == nil || *req.EvaluationRunID == "" {
			id := uuid.Must(uuid.NewV7()).String()
			weft := map[string]any{"v2": "prediction"}
			if len(req.GenAISpanRef) > 0 {
				weft["genai_span_ref"] = req.GenAISpanRef
			}
			_, err := s.store.UpsertComplete(ctx, []store.CompleteCall{{
				Start: store.CallStart{ProjectID: pid, ID: id, TraceID: id, OpName: predictOp, StartedAt: ts, Inputs: predictInputs,
					Attributes: map[string]any{"weft": weft}, UserID: user},
				End: &store.CallEnd{EndedAt: ts, Output: req.Output}}})
			if err != nil {
				return nil, err
			}
			return map[string]any{"prediction_id": id}, nil
		}
		run, err := s.v2Call(ctx, pid, *req.EvaluationRunID, "evaluation_run")
		if errors.Is(err, store.ErrNotFound) {
			return nil, store.Invalidf("evaluation run %q not found", *req.EvaluationRunID)
		}
		if err != nil {
			return nil, err
		}
		if run.EndedAt != nil {
			return nil, store.Invalidf("evaluation run %q is already finished", run.ID)
		}
		pasID, predID := uuid.Must(uuid.NewV7()).String(), uuid.Must(uuid.NewV7()).String()
		weft := map[string]any{"v2": "prediction", "evaluation_run_id": run.ID}
		if len(req.GenAISpanRef) > 0 {
			weft["genai_span_ref"] = req.GenAISpanRef
		}
		parent := run.ID
		pas := store.CallStart{ProjectID: pid, ID: pasID, TraceID: run.TraceID, ParentID: &parent, OpName: opPredictAndScore, StartedAt: ts,
			Inputs:     map[string]any{"self": run.Inputs["self"], "model": model, "example": req.Inputs},
			Attributes: map[string]any{"weft": weft}, UserID: user}
		if err := s.store.StartCall(ctx, pas); err != nil {
			return nil, err
		}
		_, err = s.store.UpsertComplete(ctx, []store.CompleteCall{{
			Start: store.CallStart{ProjectID: pid, ID: predID, TraceID: run.TraceID, ParentID: &pasID, OpName: predictOp, StartedAt: ts,
				Inputs: predictInputs, Attributes: map[string]any{"weft": map[string]any{"v2": "predict"}}, UserID: user},
			End: &store.CallEnd{EndedAt: ts, Output: req.Output}}})
		if err != nil {
			return nil, err
		}
		return map[string]any{"prediction_id": pasID}, nil
	})(w, r)
}

// predictionParts returns a prediction's inputs, output and model call.
func (s *Routes) predictionParts(ctx context.Context, pid string, c *store.Call) (map[string]any, any, *store.Call, error) {
	if c.OpBaseName() != opPredictAndScore {
		in := map[string]any{}
		for k, v := range c.Inputs {
			if k != "self" {
				in[k] = v
			}
		}
		return in, c.Output, c, nil
	}
	in, _ := c.Inputs["example"].(map[string]any)
	kids, err := s.store.ListCalls(ctx, store.CallsQuery{ProjectID: pid, Filter: &store.CallsFilter{ParentIDs: []string{c.ID}}})
	if err != nil {
		return nil, nil, nil, err
	}
	for _, k := range kids {
		if v2Attr(k, "v2") == "predict" {
			return in, k.Output, k, nil
		}
	}
	return in, nil, nil, nil
}

func (s *Routes) predictionView(ctx context.Context, pid string, c *store.Call) (map[string]any, error) {
	in, out, _, err := s.predictionParts(ctx, pid, c)
	if err != nil {
		return nil, err
	}
	model := c.Inputs["model"]
	if c.OpBaseName() != opPredictAndScore {
		model = c.Inputs["self"]
	}
	if in == nil {
		in = map[string]any{}
	}
	return map[string]any{"prediction_id": c.ID, "model": model, "inputs": in, "output": out,
		"evaluation_run_id": v2Attr(c, "evaluation_run_id"), "user_id": c.UserID}, nil
}

func (s *Routes) v2PredictionRead(w http.ResponseWriter, r *http.Request) {
	v2Handle(func(ctx context.Context, r *http.Request, pid string, _ *struct{}) (any, error) {
		c, err := s.v2Call(ctx, pid, pathParam(r, "id"), "prediction")
		if err != nil {
			return nil, err
		}
		return s.predictionView(ctx, pid, c)
	})(w, r)
}

func (s *Routes) v2KindQuery(r *http.Request, pid, kind string) ([]*store.Call, error) {
	conds := []any{map[string]any{"$eq": []any{map[string]any{"$getField": "attributes.weft.v2"}, map[string]any{"$literal": kind}}}}
	if run := r.URL.Query().Get("evaluation_run_id"); run != "" {
		conds = append(conds, map[string]any{"$eq": []any{map[string]any{"$getField": "attributes.weft.evaluation_run_id"}, map[string]any{"$literal": run}}})
	}
	limit := queryInt(r, "limit", 1000)
	return s.store.ListCalls(r.Context(), store.CallsQuery{ProjectID: pid, Query: map[string]any{"$expr": map[string]any{"$and": conds}},
		SortBy: []store.SortBy{{Field: "started_at", Direction: "asc"}}, Limit: &limit, Offset: queryInt(r, "offset", 0)})
}

func (s *Routes) v2PredictionList(w http.ResponseWriter, r *http.Request) {
	pid, err := v2Project(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	calls, err := s.v2KindQuery(r, pid, "prediction")
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(calls))
	for _, c := range calls {
		v, err := s.predictionView(r.Context(), pid, c)
		if err != nil {
			writeErr(w, err)
			return
		}
		out = append(out, v)
	}
	writeJSONL(w, out)
}

func (s *Routes) v2PredictionFinish(w http.ResponseWriter, r *http.Request) {
	v2Handle(func(ctx context.Context, r *http.Request, pid string, _ *struct{}) (any, error) {
		c, err := s.v2Call(ctx, pid, pathParam(r, "id"), "prediction")
		if err != nil {
			return nil, err
		}
		if c.EndedAt != nil {
			return map[string]any{"success": true}, nil
		}
		_, output, predict, err := s.predictionParts(ctx, pid, c)
		if err != nil {
			return nil, err
		}
		kids, err := s.store.ListCalls(ctx, store.CallsQuery{ProjectID: pid, Filter: &store.CallsFilter{ParentIDs: []string{c.ID}},
			SortBy: []store.SortBy{{Field: "started_at", Direction: "asc"}}})
		if err != nil {
			return nil, err
		}
		scores := map[string]any{}
		for _, k := range kids {
			if v2Attr(k, "v2") == "score" {
				scores[refBaseName(k.Inputs["self"])] = k.Output
			}
		}
		var latency any = 0.0
		if predict != nil {
			if l := predict.LatencyMS(); l != nil {
				latency = *l / 1000
			}
		}
		err = s.store.EndCall(ctx, store.CallEnd{ProjectID: pid, ID: c.ID, EndedAt: now(),
			Output: map[string]any{"output": output, "scores": scores, "model_latency": latency}})
		if err != nil {
			return nil, err
		}
		return map[string]any{"success": true}, nil
	})(w, r)
}

// -- scores

type v2ScoreCreateReq struct {
	PredictionID    string  `json:"prediction_id"`
	Scorer          string  `json:"scorer"`
	Value           any     `json:"value"`
	EvaluationRunID *string `json:"evaluation_run_id"`
}

func (s *Routes) v2ScoreCreate(w http.ResponseWriter, r *http.Request) {
	v2Handle(func(ctx context.Context, r *http.Request, pid string, req *v2ScoreCreateReq) (any, error) {
		if req.Scorer == "" || req.PredictionID == "" {
			return nil, store.Invalidf("prediction_id and scorer are required")
		}
		pred, err := s.v2Call(ctx, pid, req.PredictionID, "prediction")
		if errors.Is(err, store.ErrNotFound) {
			return nil, store.Invalidf("prediction %q not found", req.PredictionID)
		}
		if err != nil {
			return nil, err
		}
		in, output, _, err := s.predictionParts(ctx, pid, pred)
		if err != nil {
			return nil, err
		}
		scorer := req.Scorer
		name := refBaseName(scorer)
		runID := v2Attr(pred, "evaluation_run_id")
		if req.EvaluationRunID != nil && *req.EvaluationRunID != "" {
			runID = *req.EvaluationRunID
		}
		inputs := map[string]any{"self": scorer, "output": output}
		for k, v := range in {
			if _, ok := inputs[k]; !ok {
				inputs[k] = v
			}
		}
		id := uuid.Must(uuid.NewV7()).String()
		ts := now()
		parent := pred.ID
		weft := map[string]any{"v2": "score", "prediction_id": pred.ID}
		if runID != nil {
			weft["evaluation_run_id"] = runID
		}
		user := s.host.User(r, nil)
		_, err = s.store.UpsertComplete(ctx, []store.CompleteCall{{
			Start: store.CallStart{ProjectID: pid, ID: id, TraceID: pred.TraceID, ParentID: &parent, OpName: name + ".score", StartedAt: ts,
				Inputs: inputs, Attributes: map[string]any{"weft": weft}, UserID: user},
			End: &store.CallEnd{EndedAt: ts, Output: req.Value}}})
		if err != nil {
			return nil, err
		}
		fb := store.FeedbackCreate{ProjectID: pid, Ref: refs.SpanRef(pid, pred.ID), FeedbackType: "weft.scorer." + name,
			Payload: map[string]any{"output": req.Value}, UserID: user}
		callRef := refs.SpanRef(pid, id)
		fb.CallRef = &callRef
		if refs.IsRef(scorer) {
			fb.RunnableRef = &scorer
		}
		if _, err := s.store.CreateFeedback(ctx, fb); err != nil {
			s.log.Warn("recording v2 score feedback", "err", err)
		}
		return map[string]any{"score_id": id}, nil
	})(w, r)
}

func scoreView(c *store.Call) map[string]any {
	return map[string]any{"score_id": c.ID, "scorer": c.Inputs["self"], "value": c.Output,
		"evaluation_run_id": v2Attr(c, "evaluation_run_id"), "user_id": c.UserID}
}

func (s *Routes) v2ScoreRead(w http.ResponseWriter, r *http.Request) {
	v2Handle(func(ctx context.Context, r *http.Request, pid string, _ *struct{}) (any, error) {
		c, err := s.v2Call(ctx, pid, pathParam(r, "id"), "score")
		if err != nil {
			return nil, err
		}
		return scoreView(c), nil
	})(w, r)
}

func (s *Routes) v2ScoreList(w http.ResponseWriter, r *http.Request) {
	pid, err := v2Project(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	calls, err := s.v2KindQuery(r, pid, "score")
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(calls))
	for _, c := range calls {
		out = append(out, scoreView(c))
	}
	writeJSONL(w, out)
}

// v2CallsDelete soft-deletes runs, predictions or scores named in a query parameter.
func (s *Routes) v2CallsDelete(param string) http.HandlerFunc {
	return v2Handle(func(ctx context.Context, r *http.Request, pid string, _ *struct{}) (any, error) {
		ids := queryList(r, param)
		if len(ids) == 0 {
			return nil, store.Invalidf("%s is required", param)
		}
		n, _, err := s.store.CountCalls(ctx, pid, &store.CallsFilter{CallIDs: ids}, nil, "")
		if err != nil {
			return nil, err
		}
		if err := s.store.DeleteCalls(ctx, pid, ids); err != nil {
			return nil, err
		}
		return map[string]any{"num_deleted": n}, nil
	})
}

// ---------------------------------------------------------------- eval results

type v2EvalResultsReq struct {
	EvaluationCallIDs           []string          `json:"evaluation_call_ids"`
	EvaluationRunIDs            []string          `json:"evaluation_run_ids"`
	Filters                     []json.RawMessage `json:"filters"`
	IncludeRows                 *bool             `json:"include_rows"`
	IncludeSummary              *bool             `json:"include_summary"`
	IncludeRawDataRows          bool              `json:"include_raw_data_rows"`
	IncludePredictScoreChildren *bool             `json:"include_predict_and_score_children"`
	ResolveRowRefs              bool              `json:"resolve_row_refs"`
	RequireIntersection         bool              `json:"require_intersection"`
	SummaryRequireIntersection  *bool             `json:"summary_require_intersection"`
	Limit                       *int              `json:"limit"`
	Offset                      int               `json:"offset"`
	SortBy                      []struct {
		Field            string `json:"field"`
		Direction        string `json:"direction"`
		EvaluationCallID string `json:"evaluation_call_id"`
	} `json:"sort_by"`
}

type v2Trial struct {
	PredictAndScoreCallID string            `json:"predict_and_score_call_id"`
	PredictCallID         *string           `json:"predict_call_id"`
	ScorerCallIDs         map[string]string `json:"scorer_call_ids"`
	ModelOutput           any               `json:"model_output"`
	ModelLatencySeconds   *float64          `json:"model_latency_seconds"`
	Scores                map[string]any    `json:"scores"`
	TotalTokens           *int64            `json:"total_tokens"`
	TotalCost             *float64          `json:"total_cost"`
}

func (s *Routes) v2EvalResults(w http.ResponseWriter, r *http.Request) {
	v2Handle(func(ctx context.Context, r *http.Request, pid string, req *v2EvalResultsReq) (any, error) {
		ids := append(append([]string{}, req.EvaluationCallIDs...), req.EvaluationRunIDs...)
		cmp, err := s.engine.Compare(ctx, pid, ids)
		if err != nil {
			return nil, err
		}
		warnings := []string{}
		if len(req.Filters) > 0 {
			warnings = append(warnings, "filters are not supported by Weft and were ignored")
		}
		// predict and scorer child call ids
		children := map[string][]*store.Call{}
		if req.IncludePredictScoreChildren == nil || *req.IncludePredictScoreChildren {
			var pasIDs []string
			for _, row := range cmp.Rows {
				for _, rs := range row.Results {
					for _, x := range rs {
						pasIDs = append(pasIDs, x.CallID)
					}
				}
			}
			for start := 0; start < len(pasIDs); start += 500 {
				kids, err := s.store.ListCalls(ctx, store.CallsQuery{ProjectID: pid, Filter: &store.CallsFilter{ParentIDs: pasIDs[start:min(start+500, len(pasIDs))]},
					Columns: []string{"id", "parent_id", "op_name", "inputs"}})
				if err != nil {
					return nil, err
				}
				for _, k := range kids {
					if k.ParentID != nil {
						children[*k.ParentID] = append(children[*k.ParentID], k)
					}
				}
			}
		}
		type rowOut struct {
			RowDigest   string           `json:"row_digest"`
			RawDataRow  any              `json:"raw_data_row"`
			Evaluations []map[string]any `json:"evaluations"`
			present     int
			sortKey     any
		}
		rows := []*rowOut{}
		for _, row := range cmp.Rows {
			ro := &rowOut{RowDigest: row.RowKey, Evaluations: []map[string]any{}}
			if req.IncludeRawDataRows {
				ro.RawDataRow = row.Inputs
			}
			for _, ev := range cmp.Evaluations {
				res := row.Results[ev.CallID]
				if len(res) == 0 {
					continue
				}
				ro.present++
				trials := make([]v2Trial, 0, len(res))
				for _, x := range res {
					t := v2Trial{PredictAndScoreCallID: x.CallID, ScorerCallIDs: map[string]string{}, ModelOutput: x.Output, Scores: x.Scores}
					if f, ok := store.ToFloat(x.ModelLatency); ok {
						t.ModelLatencySeconds = &f
					}
					if x.TotalTokens > 0 {
						n := int64(x.TotalTokens)
						t.TotalTokens = &n
					}
					for _, k := range children[x.CallID] {
						base := k.OpBaseName()
						switch {
						case strings.HasSuffix(base, ".score"):
							t.ScorerCallIDs[strings.TrimSuffix(base, ".score")] = k.ID
						case strings.HasSuffix(base, ".predict") || strings.HasSuffix(base, "predict") || base == "weft.completions.create":
							id := k.ID
							t.PredictCallID = &id
						}
					}
					trials = append(trials, t)
				}
				ro.Evaluations = append(ro.Evaluations, map[string]any{"evaluation_call_id": ev.CallID, "trials": trials})
			}
			if req.RequireIntersection && ro.present < len(cmp.Evaluations) {
				continue
			}
			rows = append(rows, ro)
		}
		// sort
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].RowDigest < rows[j].RowDigest })
		if len(req.SortBy) > 0 {
			sb := req.SortBy[0]
			val := func(ro *rowOut) any {
				for _, e := range ro.Evaluations {
					if sb.EvaluationCallID != "" && e["evaluation_call_id"] != sb.EvaluationCallID {
						continue
					}
					ts := e["trials"].([]v2Trial)
					if len(ts) == 0 {
						continue
					}
					parts := strings.SplitN(sb.Field, ".", 2)
					if len(parts) < 2 {
						return nil
					}
					var src any
					switch parts[0] {
					case "scores":
						src = ts[0].Scores
					case "outputs", "output":
						src = map[string]any{"output": ts[0].ModelOutput}
						parts[1] = "output." + parts[1]
					case "inputs":
						src = ro.RawDataRow
					}
					v, _ := payloadPath(src, parts[1])
					return v
				}
				return nil
			}
			desc := strings.EqualFold(sb.Direction, "desc")
			sort.SliceStable(rows, func(i, j int) bool {
				a, b := val(rows[i]), val(rows[j])
				fa, oka := store.ToFloat(a)
				fb, okb := store.ToFloat(b)
				if oka && okb {
					if desc {
						return fa > fb
					}
					return fa < fb
				}
				if oka != okb {
					return oka
				}
				sa, sbb := fmt.Sprint(a), fmt.Sprint(b)
				if desc {
					return sa > sbb
				}
				return sa < sbb
			})
		}
		out := map[string]any{"total_rows": len(rows), "warnings": warnings, "rows": []any{}}
		if req.IncludeRows == nil || *req.IncludeRows {
			page := rows
			if req.Offset > 0 {
				page = page[min(req.Offset, len(page)):]
			}
			if req.Limit != nil && *req.Limit >= 0 {
				page = page[:min(*req.Limit, len(page))]
			}
			out["rows"] = page
		}
		if req.IncludeSummary == nil || *req.IncludeSummary {
			out["summary"] = v2ResultsSummary(cmp)
		}
		return out, nil
	})(w, r)
}

func v2ResultsSummary(cmp *evals.Comparison) map[string]any {
	type stat struct {
		scorer, path        string
		trials, numCount    int
		numSum              float64
		passTrue, passKnown int
		hasText             bool
	}
	evalsOut := []map[string]any{}
	for _, ev := range cmp.Evaluations {
		stats := map[string]*stat{}
		var order []string
		trials := 0
		var tokens int64
		for _, row := range cmp.Rows {
			for _, x := range row.Results[ev.CallID] {
				trials++
				tokens += int64(x.TotalTokens)
				for name, v := range x.Scores {
					var walk func(path string, v any)
					walk = func(path string, v any) {
						if m, ok := v.(map[string]any); ok {
							for k, y := range m {
								p := k
								if path != "" {
									p = path + "." + k
								}
								walk(p, y)
							}
							return
						}
						key := name + "\x00" + path
						st := stats[key]
						if st == nil {
							st = &stat{scorer: name, path: path}
							stats[key] = st
							order = append(order, key)
						}
						st.trials++
						switch t := v.(type) {
						case bool:
							st.passKnown++
							if t {
								st.passTrue++
							}
						case string:
							st.hasText = true
						default:
							if f, ok := store.ToFloat(v); ok {
								st.numCount++
								st.numSum += f
							}
						}
					}
					walk("", v)
				}
			}
		}
		sort.Strings(order)
		scorerStats := []map[string]any{}
		for _, k := range order {
			st := stats[k]
			m := map[string]any{"scorer_key": st.scorer, "trial_count": st.trials, "numeric_count": st.numCount, "numeric_mean": nil,
				"pass_known_count": st.passKnown, "pass_true_count": st.passTrue, "pass_rate": nil, "pass_signal_coverage": nil, "path": nil}
			if st.path != "" {
				m["path"] = st.path
			}
			switch {
			case st.passKnown > 0:
				m["value_type"] = "binary"
				m["pass_rate"] = float64(st.passTrue) / float64(st.passKnown)
				m["pass_signal_coverage"] = float64(st.passKnown) / float64(st.trials)
			case st.numCount > 0:
				m["value_type"] = "continuous"
				m["numeric_mean"] = st.numSum / float64(st.numCount)
			case st.hasText:
				m["value_type"] = "text"
			}
			scorerStats = append(scorerStats, m)
		}
		var tok any
		if tokens > 0 {
			tok = tokens
		}
		evalsOut = append(evalsOut, map[string]any{"evaluation_call_id": ev.CallID, "display_name": ev.DisplayName, "evaluation_ref": ev.EvaluationRef,
			"model_ref": ev.ModelRef, "started_at": ev.StartedAt, "trace_id": ev.CallID, "trial_count": trials, "scorer_stats": scorerStats,
			"predict_total_tokens": tok, "predict_total_cost": nil})
	}
	return map[string]any{"evaluations": evalsOut, "row_count": len(cmp.Rows)}
}
