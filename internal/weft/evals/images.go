package evals

import "context"

// TrackedImage generates images and records the request as a
// weft.images.generate call. It returns the provider response and call id.
func (e *Engine) TrackedImage(ctx context.Context, projectID string, inputs map[string]any) (map[string]any, string, error) {
	rec, err := e.startCall(ctx, projectID, "weft.images.generate", nil, inputs, nil, nil)
	if err != nil {
		return nil, "", err
	}
	model, _ := inputs["model"].(string)
	resp, callErr := e.LLM.GenerateImage(ctx, projectID, inputs)
	var summary map[string]any
	if callErr == nil {
		// Image APIs report input_tokens/output_tokens; normalise for the roll-up.
		if u, ok := resp["usage"].(map[string]any); ok {
			norm := map[string]any{}
			for k, v := range u {
				norm[k] = v
			}
			if _, ok := norm["prompt_tokens"]; !ok {
				norm["prompt_tokens"] = u["input_tokens"]
			}
			if _, ok := norm["completion_tokens"]; !ok {
				norm["completion_tokens"] = u["output_tokens"]
			}
			resp = shallowWith(resp, "usage", norm)
		}
		summary = usageSummary(resp, model)
		if u, ok := summary["usage"].(map[string]any); ok {
			for _, entry := range u {
				if m, ok := entry.(map[string]any); ok {
					m["images"] = len(anySlice(resp["data"]))
				}
			}
		}
	}
	if err := rec.end(ctx, resp, summary, callErr); err != nil {
		return nil, rec.id, err
	}
	return resp, rec.id, callErr
}

func anySlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func shallowWith(m map[string]any, k string, v any) map[string]any {
	out := make(map[string]any, len(m))
	for kk, vv := range m {
		out[kk] = vv
	}
	out[k] = v
	return out
}
