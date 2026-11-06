package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/png"
	"time"
)

// GenerateImage creates images with an OpenAI-compatible /images/generations
// endpoint and returns the provider response (OpenAI shape: created, data[]).
// mock/* models return a deterministic PNG derived from the prompt, offline.
func (c *Client) GenerateImage(ctx context.Context, projectID string, inputs map[string]any) (map[string]any, error) {
	model, _ := inputs["model"].(string)
	prompt, _ := inputs["prompt"].(string)
	if prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	n := 1
	if v, ok := inputs["n"].(json.Number); ok {
		if i, err := v.Int64(); err == nil {
			n = int(i)
		}
	} else if v, ok := inputs["n"].(float64); ok {
		n = int(v)
	} else if v, ok := inputs["n"].(int); ok {
		n = v
	}
	if n < 1 || n > 10 {
		return nil, fmt.Errorf("n must be between 1 and 10")
	}
	rt, err := c.Resolve(ctx, projectID, model)
	if err != nil {
		return nil, err
	}
	switch rt.Kind {
	case "mock":
		data := make([]any, 0, n)
		for i := 0; i < n; i++ {
			data = append(data, map[string]any{"b64_json": MockImagePNG(fmt.Sprintf("%s#%d", prompt, i)), "revised_prompt": prompt})
		}
		return map[string]any{"created": time.Now().Unix(), "data": data, "model": model,
			"usage": map[string]any{"input_tokens": len(bytes.Fields([]byte(prompt))), "output_tokens": 0, "total_tokens": len(bytes.Fields([]byte(prompt)))}}, nil
	case "anthropic":
		return nil, fmt.Errorf("model %q does not generate images; use an OpenAI-compatible image model such as openai/gpt-image-1", model)
	}
	headers, err := c.keyHeaders(ctx, rt)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"model": rt.Model, "prompt": prompt, "n": n}
	for _, k := range []string{"size", "quality", "style", "response_format", "background", "output_format"} {
		if v, ok := inputs[k]; ok && v != nil {
			body[k] = v
		}
	}
	resp, err := c.post(ctx, rt, "/images/generations", headers, body, MaxImageResponseBytes)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, decodeErr("image response", err)
	}
	return out, nil
}

// MockImagePNG renders a small deterministic gradient PNG for text, base64-encoded.
func MockImagePNG(text string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(text))
	sum := h.Sum64()
	const size = 64
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	r0, g0, b0 := uint8(sum), uint8(sum>>8), uint8(sum>>16)
	r1, g1, b1 := uint8(sum>>24), uint8(sum>>32), uint8(sum>>40)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			t := float64(x+y) / float64(2*size-2)
			mix := func(a, b uint8) uint8 { return uint8(float64(a)*(1-t) + float64(b)*t) }
			img.Set(x, y, color.RGBA{mix(r0, r1), mix(g0, g1), mix(b0, b1), 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}
