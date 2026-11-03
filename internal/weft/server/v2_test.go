package server

import (
	"strings"
	"testing"
	"time"
)

func TestV2ObjectsCRUD(t *testing.T) {
	e := newEnv(t)
	base := "/v2/acme/evals"
	op := e.admJSON("POST", base+"/ops", "", map[string]any{"source_code": "def shout(x):\n    return x.upper()\n"})
	if op["object_id"] != "shout" || op["version_index"] != float64(0) {
		t.Fatalf("op create = %v", op)
	}
	rd := e.admJSON("GET", base+"/ops/shout/versions/"+op["digest"].(string), "", nil)
	if !strings.Contains(rd["code"].(string), "upper") {
		t.Fatalf("op read = %v", rd)
	}
	ds := e.admJSON("POST", base+"/datasets", "", map[string]any{"name": "qa", "description": "d", "rows": []any{map[string]any{"q": "1"}, map[string]any{"q": "2"}}})
	dsr := e.admJSON("GET", base+"/datasets/qa", "", nil)
	if dsr["digest"] != ds["digest"] || !strings.Contains(dsr["rows"].(string), "/table/") || dsr["description"] != "d" {
		t.Fatalf("dataset read = %v", dsr)
	}
	if n := e.post("/table/query_stats", map[string]any{"project_id": pid, "digest": dsr["rows"].(string)[strings.LastIndex(dsr["rows"].(string), "/")+1:]}); n["count"] != float64(2) {
		t.Fatalf("dataset rows = %v", n)
	}
	e.admJSON("POST", base+"/models", "", map[string]any{"name": "m", "source_code": "class M(weft.Model): ...", "attributes": map[string]any{"temp": 0.1}})
	mr := e.admJSON("GET", base+"/models/m", "", nil)
	if mr["attributes"].(map[string]any)["temp"] != 0.1 || mr["source_code"] == "" {
		t.Fatalf("model read = %v", mr)
	}
	sc := e.admJSON("POST", base+"/scorers", "", map[string]any{"name": "acc", "op_source_code": "def score(output): return True"})
	sr := e.admJSON("GET", base+"/scorers/acc/versions/"+sc["digest"].(string), "", nil)
	if !strings.Contains(sr["score_op"].(string), "/op/acc.score:") {
		t.Fatalf("scorer read = %v", sr)
	}
	dsRef := "weft:///acme/evals/object/qa:" + ds["digest"].(string)
	scRef := "weft:///acme/evals/object/acc:" + sc["digest"].(string)
	ev := e.admJSON("POST", base+"/evaluations", "", map[string]any{"name": "ev", "dataset": dsRef, "scorers": []any{scRef}, "trials": 2})
	evr := e.admJSON("GET", base+"/evaluations/ev/versions/"+ev["digest"].(string), "", nil)
	if evr["trials"] != float64(2) || len(evr["scorers"].([]any)) != 1 {
		t.Fatalf("evaluation read = %v", evr)
	}
	// list is JSONL of latest versions
	e.admJSON("POST", base+"/datasets", "", map[string]any{"name": "qa", "rows": []any{map[string]any{"q": "3"}}})
	code, body := e.admDo("GET", base+"/datasets?limit=10", "", nil)
	if items := jsonl(t, body); code != 200 || len(items) != 1 || items[0]["version_index"] != float64(1) {
		t.Fatalf("dataset list = %d %s", code, body)
	}
	// kinds do not leak into each other
	if code, _ := e.admDo("GET", base+"/models/qa", "", nil); code != 404 {
		t.Fatalf("dataset read as model -> %d", code)
	}
	if code, _ := e.admDo("POST", base+"/evaluations", "", map[string]any{"name": "bad", "dataset": "not-a-ref"}); code != 400 {
		t.Fatalf("bad dataset ref -> %d", code)
	}
	del := e.admJSON("DELETE", base+"/datasets/qa?digests="+ds["digest"].(string), "", nil)
	if del["num_deleted"] != float64(1) {
		t.Fatalf("delete = %v", del)
	}
}

func TestV2EvaluationFlow(t *testing.T) {
	e := newEnv(t)
	base := "/v2/acme/evals"
	ds := e.admJSON("POST", base+"/datasets", "", map[string]any{"name": "qa", "rows": []any{map[string]any{"q": "a", "expected": "A"}, map[string]any{"q": "b", "expected": "B"}}})
	dsRef := "weft:///acme/evals/object/qa:" + ds["digest"].(string)
	exact := publish(e, "exact", map[string]any{"_class_name": "ExactMatchScorer", "_bases": []any{"Scorer", "Object", "BaseModel"}, "target_column": "expected"})
	ev := e.admJSON("POST", base+"/evaluations", "", map[string]any{"name": "ev", "dataset": dsRef, "scorers": []any{exact}})
	evRef := "weft:///acme/evals/object/ev:" + ev["digest"].(string)
	m := e.admJSON("POST", base+"/models", "", map[string]any{"name": "upper", "source_code": "..."})
	modelRef := "weft:///acme/evals/object/upper:" + m["digest"].(string)

	runID := e.admJSON("POST", base+"/evaluation_runs", "", map[string]any{"evaluation": evRef, "model": modelRef})["evaluation_run_id"].(string)
	run := e.admJSON("GET", base+"/evaluation_runs/"+runID, "", nil)
	if run["status"] != "running" || run["evaluation"] != evRef {
		t.Fatalf("run = %v", run)
	}
	outputs := map[string]string{"a": "A", "b": "x"}
	var predIDs []string
	for _, q := range []string{"a", "b"} {
		p := e.admJSON("POST", base+"/predictions", "", map[string]any{"model": modelRef, "inputs": map[string]any{"q": q, "expected": strings.ToUpper(q)},
			"output": outputs[q], "evaluation_run_id": runID})["prediction_id"].(string)
		predIDs = append(predIDs, p)
		e.admJSON("POST", base+"/scores", "", map[string]any{"prediction_id": p, "scorer": exact, "value": outputs[q] == strings.ToUpper(q), "evaluation_run_id": runID})
		e.admJSON("POST", base+"/scores", "", map[string]any{"prediction_id": p, "scorer": "length", "value": len(outputs[q])})
		e.admJSON("POST", base+"/predictions/"+p+"/finish", "", nil)
	}
	pr := e.admJSON("GET", base+"/predictions/"+predIDs[0], "", nil)
	if pr["output"] != "A" || pr["inputs"].(map[string]any)["q"] != "a" || pr["evaluation_run_id"] != runID || pr["model"] != modelRef {
		t.Fatalf("prediction read = %v", pr)
	}
	_, body := e.admDo("GET", base+"/scores?evaluation_run_id="+runID, "", nil)
	if scores := jsonl(t, body); len(scores) != 4 {
		t.Fatalf("scores = %s", body)
	}
	_, body = e.admDo("GET", base+"/predictions?evaluation_run_id="+runID, "", nil)
	if preds := jsonl(t, body); len(preds) != 2 {
		t.Fatalf("predictions = %s", body)
	}
	if fin := e.admJSON("POST", base+"/evaluation_runs/"+runID+"/finish", "", map[string]any{}); fin["success"] != true {
		t.Fatalf("finish = %v", fin)
	}
	run = e.admJSON("GET", base+"/evaluation_runs/"+runID, "", nil)
	sum := run["summary"].(map[string]any)
	if run["status"] != "finished" || sum["exact"].(map[string]any)["true_fraction"] != 0.5 || sum["length"].(map[string]any)["mean"] != 1.0 {
		t.Fatalf("finished run = %v", run)
	}
	_, body = e.admDo("GET", base+"/evaluation_runs?evaluations="+evRef, "", nil)
	if runs := jsonl(t, body); len(runs) != 1 || runs[0]["evaluation_run_id"] != runID {
		t.Fatalf("run list = %s", body)
	}

	// Visible to the comparison view.
	cmp := e.post("/evaluations/compare", map[string]any{"project_id": pid, "evaluation_call_ids": []any{runID}})
	if rows := cmp["rows"].([]any); len(rows) != 2 {
		t.Fatalf("compare rows = %v", cmp["rows"])
	}
	found := false
	for _, x := range cmp["metrics"].([]any) {
		mm := x.(map[string]any)
		if mm["key"] == "exact.true_fraction" && mm["values"].(map[string]any)[runID] == 0.5 {
			found = true
		}
	}
	if !found || cmp["evaluations"].([]any)[0].(map[string]any)["model_name"] != "upper" {
		t.Fatalf("compare = %v", cmp)
	}
	// Feedback was attached to the predictions.
	fb := e.post("/feedback/query", map[string]any{"project_id": pid})["result"].([]any)
	if len(fb) != 4 {
		t.Fatalf("feedback = %d", len(fb))
	}

	// eval_results/query
	res := e.admJSON("POST", base+"/eval_results/query", "", map[string]any{"evaluation_run_ids": []any{runID}, "include_raw_data_rows": true,
		"sort_by": []any{map[string]any{"field": "scores.length", "direction": "desc"}}})
	rows := res["rows"].([]any)
	if res["total_rows"] != float64(2) || len(rows) != 2 {
		t.Fatalf("eval_results = %v", res)
	}
	tr := rows[0].(map[string]any)["evaluations"].([]any)[0].(map[string]any)["trials"].([]any)[0].(map[string]any)
	if tr["model_output"] != "A" || tr["predict_call_id"] == nil || len(tr["scorer_call_ids"].(map[string]any)) != 2 {
		t.Fatalf("trial = %v", tr)
	}
	if rows[0].(map[string]any)["raw_data_row"].(map[string]any)["q"] != "a" {
		t.Fatalf("raw row = %v", rows[0])
	}
	stats := res["summary"].(map[string]any)["evaluations"].([]any)[0].(map[string]any)["scorer_stats"].([]any)
	var exactStat map[string]any
	for _, s := range stats {
		if s.(map[string]any)["scorer_key"] == "exact" {
			exactStat = s.(map[string]any)
		}
	}
	if exactStat == nil || exactStat["pass_rate"] != 0.5 || exactStat["value_type"] != "binary" {
		t.Fatalf("scorer stats = %v", stats)
	}

	// Rescore the run with the server-runnable scorer.
	rs := e.post("/evaluations/rescore", map[string]any{"project_id": pid, "source_evaluation_run_id": runID, "scorer_refs": []any{exact}})
	newID := rs["evaluation_run_id"].(string)
	deadline := time.Now().Add(10 * time.Second)
	var st map[string]any
	for time.Now().Before(deadline) {
		st = e.post("/evaluations/status", map[string]any{"project_id": pid, "call_id": newID})["status"].(map[string]any)
		if st["code"] == "complete" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if st["code"] != "complete" || st["output"].(map[string]any)["exact"].(map[string]any)["true_fraction"] != 0.5 {
		t.Fatalf("rescore status = %v", st)
	}
	nr := e.admJSON("GET", base+"/evaluation_runs/"+newID, "", nil)
	if nr["source_evaluation_run_id"] != runID || nr["model"] != modelRef {
		t.Fatalf("rescored run = %v", nr)
	}
	if code, _ := e.raw("/evaluations/rescore", map[string]any{"project_id": pid, "source_evaluation_run_id": predIDs[0], "scorer_refs": []any{exact}}, nil); code != 400 {
		t.Fatalf("rescore of a non-run -> %d", code)
	}

	// Predictions outside a run, and deletes.
	lone := e.admJSON("POST", base+"/predictions", "", map[string]any{"model": modelRef, "inputs": map[string]any{"q": "z"}, "output": "Z"})["prediction_id"].(string)
	if p := e.admJSON("GET", base+"/predictions/"+lone, "", nil); p["output"] != "Z" || p["evaluation_run_id"] != nil {
		t.Fatalf("lone prediction = %v", p)
	}
	if d := e.admJSON("DELETE", base+"/predictions?prediction_ids="+lone, "", nil); d["num_deleted"] != float64(1) {
		t.Fatalf("delete = %v", d)
	}
	if code, _ := e.admDo("POST", base+"/predictions", "", map[string]any{"model": modelRef, "inputs": map[string]any{}, "output": 1, "evaluation_run_id": runID}); code != 400 {
		t.Fatalf("prediction on a finished run -> %d", code)
	}
}
