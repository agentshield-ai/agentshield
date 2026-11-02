package server

import (
	"testing"
	"time"
)

// verl (verl/utils/rollout_trace.py) with trace.backend=weave traces rollouts
// through the official weave client: create_call(op=<qualname>, inputs,
// attributes={step, sample_index, rollout_n, validate, experiment_name})
// and finish_call(output) with prompt/response token ids and, with
// token2text, the decoded text. The calls must be stored as sent and be
// filterable by those attributes.
func TestVerlWeaveCalls(t *testing.T) {
	e := newEnv(t)
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	var batch []any
	for i, step := range []int{3, 3, 4} {
		id := []string{"r1", "r2", "r3"}[i]
		attrs := map[string]any{"step": step, "sample_index": i, "rollout_n": 0, "validate": false, "experiment_name": "gsm8k-grpo",
			"weave": map[string]any{"client_version": "0.54.0", "source": "python-sdk"}}
		batch = append(batch,
			map[string]any{"mode": "start", "req": map[string]any{"start": map[string]any{"project_id": pid, "id": id, "trace_id": id,
				"op_name": "ToolAgentLoop.run", "started_at": ts, "attributes": attrs,
				"inputs": map[string]any{"sampling_params": map[string]any{"temperature": 1.0}, "raw_prompt": []any{map[string]any{"role": "user", "content": "2+2?"}}}}}},
			map[string]any{"mode": "end", "req": map[string]any{"end": map[string]any{"project_id": pid, "id": id, "ended_at": ts,
				"output":  map[string]any{"prompt_ids": []any{1, 2}, "response_ids": []any{3}, "num_turns": 2, "prompt_text": "2+2?", "response_text": "4"},
				"summary": map[string]any{}}}})
	}
	e.post("/call/upsert_batch", map[string]any{"batch": batch})
	res := e.post("/calls/query", map[string]any{"project_id": pid, "query": map[string]any{"$expr": map[string]any{"$and": []any{
		map[string]any{"$eq": []any{map[string]any{"$getField": "attributes.step"}, map[string]any{"$literal": 3}}},
		map[string]any{"$eq": []any{map[string]any{"$getField": "attributes.experiment_name"}, map[string]any{"$literal": "gsm8k-grpo"}}},
	}}}})
	calls := res["calls"].([]any)
	if len(calls) != 2 {
		t.Fatalf("step 3 calls = %d", len(calls))
	}
	c := calls[0].(map[string]any)
	if c["op_name"] != "ToolAgentLoop.run" || c["output"].(map[string]any)["response_text"] != "4" {
		t.Fatalf("call = %v", c)
	}
}
