// Package llm routes chat completion requests to LLM providers and always
// returns OpenAI chat-completion shaped results.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/egress"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/google/uuid"
)

// SecretFunc returns the stored secret named name that projectID can see
// (its own, its team's, else the server's: the narrowest scope that has it),
// or nil. projectID "" sees server secrets only. Whether it may be used is
// decided by Client.UseSecret (secrets.go).
type SecretFunc func(ctx context.Context, projectID, name string) (*store.SecretRecord, error)

// CustomProvider is an OpenAI-compatible endpoint defined by a Provider object.
type CustomProvider struct {
	Name         string
	BaseURL      string
	APIKeyName   string
	ExtraHeaders map[string]string
}

// ProviderFunc resolves a custom provider by name within a project.
type ProviderFunc func(ctx context.Context, projectID, name string) (*CustomProvider, error)

// Client calls LLM providers.
type Client struct {
	Secret    SecretFunc
	Providers ProviderFunc
	// ServerSecretsOpen reports whether projects' own objects may use server
	// secrets without allowed hosts: true while every principal of the
	// server is a server admin. nil means never.
	ServerSecretsOpen func(ctx context.Context) bool
	// HTTP calls project-chosen endpoints (custom providers, stored base
	// URLs); OperatorHTTP calls the built-in provider routes at their
	// built-in or environment base URLs (egress.NewProviderClient). Neither
	// follows redirects.
	HTTP         *http.Client
	OperatorHTTP *http.Client
}

// Response size limits (decompressed bytes). A provider that sends more gets
// the call failed with ErrResponseTooLarge, so a hostile or broken endpoint
// cannot exhaust memory or have huge spans stored.
var (
	MaxCompletionResponseBytes int64 = 16 << 20
	MaxEmbeddingResponseBytes  int64 = 128 << 20
	MaxImageResponseBytes      int64 = 64 << 20
	MaxModerationResponseBytes int64 = 4 << 20
	// MaxStreamBytes bounds the raw event stream of a streamed completion.
	MaxStreamBytes int64 = 64 << 20
	// MaxStreamContentBytes bounds the accumulated content and tool-call
	// arguments of a streamed completion (what is returned and stored).
	MaxStreamContentBytes = 16 << 20
)

// ErrResponseTooLarge is returned when a provider response exceeds its limit.
var ErrResponseTooLarge = errors.New("provider response too large")

// New creates a client. Secrets come only from the secret store (secret).
// The server's own environment is consulted only for the API key of a
// built-in provider whose base URL is the operator's (the default or an
// environment variable), never for a name a project or user chose: otherwise
// anyone able to name a secret (a scorer header, an OAuth client secret, a
// custom provider key) could have the server send its environment, such as
// OPENAI_API_KEY or WEFT_DB_ENCRYPTION_KEY, to a URL they control.
func New(secret SecretFunc, providers ProviderFunc) *Client {
	return &Client{Secret: secret, Providers: providers, HTTP: egress.NewClient(10*time.Minute, false),
		OperatorHTTP: egress.NewProviderClient(10 * time.Minute)}
}

// routeKey returns the API key for rt: the stored secret (UseSecret decides
// whether this project may send it to rt.BaseURL), or, for a built-in
// provider at its operator-chosen base URL (rt.envKey), the environment.
// A stored secret that exists but may not be used is an error.
func (c *Client) routeKey(ctx context.Context, rt Route) (string, bool, error) {
	if rt.KeyName == "" {
		return "", false, nil
	}
	v, _, err := c.UseSecret(ctx, SecretUse{ProjectID: rt.project, Name: rt.KeyName, Dest: rt.BaseURL + "/", Builtin: rt.builtin,
		MinScope: rt.minScope, OperatorDest: rt.operator})
	if err == nil {
		return v, true, nil
	}
	if !IsSecretNotFound(err) {
		return "", false, err
	}
	if rt.envKey {
		v := os.Getenv(rt.KeyName)
		return v, v != "", nil
	}
	return "", false, nil
}

// openAICompatible are well-known OpenAI-compatible providers.
var openAICompatible = map[string]struct{ BaseURL, KeyName string }{
	"openai":     {"https://api.openai.com/v1", "OPENAI_API_KEY"},
	"gemini":     {"https://generativelanguage.googleapis.com/v1beta/openai", "GEMINI_API_KEY"},
	"google":     {"https://generativelanguage.googleapis.com/v1beta/openai", "GEMINI_API_KEY"},
	"groq":       {"https://api.groq.com/openai/v1", "GROQ_API_KEY"},
	"mistral":    {"https://api.mistral.ai/v1", "MISTRAL_API_KEY"},
	"deepseek":   {"https://api.deepseek.com/v1", "DEEPSEEK_API_KEY"},
	"xai":        {"https://api.x.ai/v1", "XAI_API_KEY"},
	"openrouter": {"https://openrouter.ai/api/v1", "OPENROUTER_API_KEY"},
	"together":   {"https://api.together.xyz/v1", "TOGETHER_API_KEY"},
	"ollama":     {"http://localhost:11434/v1", ""},
}

// Route describes how a model id is served.
type Route struct {
	Kind    string // openai, anthropic, mock
	Model   string // model name sent to the provider
	BaseURL string
	KeyName string
	Headers map[string]string
	// envKey: KeyName may be read from the server environment (built-in
	// provider, base URL not chosen by a project).
	envKey bool
	// project resolves stored secrets; builtin: a built-in provider route
	// names KeyName (server secrets usable); operator: BaseURL is the
	// built-in or environment base URL; minScope: KeyName must be at least
	// this narrow (BaseURL came from a secret of that scope); urlSecret:
	// BaseURL came from a stored secret, so errors must not print it; err:
	// a stored base URL could not be used.
	project   string
	builtin   bool
	operator  bool
	minScope  string
	urlSecret bool
	err       error
}

// Resolve maps a model id to a route.
func (c *Client) Resolve(ctx context.Context, projectID, model string) (Route, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return Route{}, errors.New("model is required")
	}
	if prefix, name, ok := strings.Cut(model, "/"); ok {
		switch prefix {
		case "mock":
			return Route{Kind: "mock", Model: name}, nil
		case "anthropic":
			return anthropicRoute(projectID, name), nil
		}
		if p, ok := openAICompatible[prefix]; ok {
			return c.openAIRoute(ctx, projectID, prefix, name, p.BaseURL, p.KeyName)
		}
		if c.Providers != nil {
			cp, err := c.Providers(ctx, projectID, prefix)
			if err != nil {
				return Route{}, err
			}
			if cp != nil {
				return Route{Kind: "openai", Model: name, BaseURL: strings.TrimRight(cp.BaseURL, "/"), KeyName: cp.APIKeyName, Headers: cp.ExtraHeaders,
					project: projectID}, nil
			}
		}
		return Route{}, fmt.Errorf("unknown provider %q in model %q", prefix, model)
	}
	builtin := func(provider string) (Route, error) {
		p := openAICompatible[provider]
		return c.openAIRoute(ctx, projectID, provider, model, p.BaseURL, p.KeyName)
	}
	switch {
	case strings.HasPrefix(model, "claude"):
		return anthropicRoute(projectID, model), nil
	case strings.HasPrefix(model, "gemini"):
		return builtin("gemini")
	case strings.HasPrefix(model, "mistral") || strings.HasPrefix(model, "codestral"):
		return builtin("mistral")
	case strings.HasPrefix(model, "deepseek"):
		return builtin("deepseek")
	case strings.HasPrefix(model, "grok"):
		return builtin("xai")
	case strings.HasPrefix(model, "gpt") || strings.HasPrefix(model, "o1") || strings.HasPrefix(model, "o3") ||
		strings.HasPrefix(model, "o4") || strings.HasPrefix(model, "chatgpt") || strings.HasPrefix(model, "text-"):
		return builtin("openai")
	}
	return Route{}, fmt.Errorf("cannot route model %q: use a provider prefix such as openai/, anthropic/, gemini/ or a custom provider name", model)
}

func anthropicRoute(projectID, model string) Route {
	// The Anthropic base URL comes only from the operator's environment.
	return Route{Kind: "anthropic", Model: model, BaseURL: anthropicBase(), KeyName: "ANTHROPIC_API_KEY", envKey: true,
		project: projectID, builtin: true, operator: true}
}

// openAIRoute is a built-in OpenAI-compatible provider. Its base URL is the
// built-in one, the operator's environment variable, or a stored
// OPENAI_BASE_URL / OLLAMA_BASE_URL secret visible to the project. A stored
// base URL may point anywhere, so the operator's environment key is not sent
// there, and the key must come from the same scope or a narrower one (a base
// URL a project member stored never receives a server or team key).
func (c *Client) openAIRoute(ctx context.Context, projectID, provider, model, base, key string) (Route, error) {
	rt := Route{Kind: "openai", Model: model, KeyName: key, envKey: true, project: projectID, builtin: true, operator: true}
	baseVar := map[string]string{"openai": "OPENAI_BASE_URL", "ollama": "OLLAMA_BASE_URL"}[provider]
	if baseVar != "" {
		// The base URL is not itself a credential, so http is fine here;
		// the key sent to it is checked (https) by routeKey.
		v, rec, err := c.UseSecret(ctx, SecretUse{ProjectID: projectID, Name: baseVar, URLValued: true, Builtin: true, AllowHTTP: true})
		switch {
		case err == nil:
			base, rt.envKey, rt.operator, rt.urlSecret, rt.minScope = strings.TrimSpace(v), false, false, true, rec.Scope
		case !IsSecretNotFound(err):
			return Route{}, err
		default:
			if v := os.Getenv(baseVar); v != "" {
				base = v
			}
		}
	}
	rt.BaseURL = strings.TrimRight(base, "/")
	return rt, nil
}

// keyHeaders returns the Authorization header for rt (none for a keyless
// local endpoint) plus the route's extra headers.
func (c *Client) keyHeaders(ctx context.Context, rt Route) (map[string]string, error) {
	headers := map[string]string{}
	if rt.KeyName != "" {
		key, ok, err := c.routeKey(ctx, rt)
		if err != nil {
			return nil, err
		}
		if !ok && !strings.HasPrefix(rt.BaseURL, "http://localhost") && !strings.HasPrefix(rt.BaseURL, "http://127.0.0.1") {
			return nil, fmt.Errorf("no API key configured: set the %s secret", rt.KeyName)
		}
		if key != "" {
			headers["Authorization"] = "Bearer " + key
		}
	}
	for k, v := range rt.Headers {
		headers[k] = v
	}
	return headers, nil
}

// Complete runs a non-streaming chat completion.
func (c *Client) Complete(ctx context.Context, projectID string, inputs map[string]any) (map[string]any, error) {
	model, _ := inputs["model"].(string)
	rt, err := c.Resolve(ctx, projectID, model)
	if err != nil {
		return nil, err
	}
	switch rt.Kind {
	case "mock":
		return mockComplete(rt.Model, model, inputs), nil
	case "anthropic":
		return c.anthropicComplete(ctx, rt, inputs, nil)
	default:
		return c.openAIComplete(ctx, rt, inputs, nil)
	}
}

// Stream runs a streaming chat completion. emit receives OpenAI chunk
// objects; the accumulated final response is returned.
func (c *Client) Stream(ctx context.Context, projectID string, inputs map[string]any, emit func(map[string]any) error) (map[string]any, error) {
	model, _ := inputs["model"].(string)
	rt, err := c.Resolve(ctx, projectID, model)
	if err != nil {
		return nil, err
	}
	acc := newAccumulator(model)
	wrapped := func(ch map[string]any) error {
		if err := acc.add(ch); err != nil {
			return err
		}
		return emit(ch)
	}
	switch rt.Kind {
	case "mock":
		final := mockComplete(rt.Model, model, inputs)
		content, _ := final["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)
		words := strings.SplitAfter(content, " ")
		id, _ := final["id"].(string)
		for _, w := range words {
			if err := wrapped(chunk(id, model, map[string]any{"content": w}, nil, nil)); err != nil {
				return nil, err
			}
		}
		if err := wrapped(chunk(id, model, map[string]any{}, "stop", final["usage"])); err != nil {
			return nil, err
		}
	case "anthropic":
		if _, err := c.anthropicComplete(ctx, rt, inputs, wrapped); err != nil {
			return nil, err
		}
	default:
		if _, err := c.openAIComplete(ctx, rt, inputs, wrapped); err != nil {
			return nil, err
		}
	}
	return acc.final(), nil
}

func chunk(id, model string, delta map[string]any, finish any, usage any) map[string]any {
	ch := map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
	}
	if usage != nil {
		ch["usage"] = usage
	}
	return ch
}

// ---------------------------------------------------------------- OpenAI

var openAIParams = []string{"messages", "temperature", "max_tokens", "max_completion_tokens", "top_p", "frequency_penalty",
	"presence_penalty", "stop", "n", "response_format", "tools", "tool_choice", "seed", "logprobs", "top_logprobs",
	"reasoning_effort", "parallel_tool_calls", "user"}

func (c *Client) openAIComplete(ctx context.Context, rt Route, inputs map[string]any, emit func(map[string]any) error) (map[string]any, error) {
	body := map[string]any{"model": rt.Model}
	for _, k := range openAIParams {
		if v, ok := inputs[k]; ok && v != nil {
			body[k] = v
		}
	}
	if rf, ok := body["response_format"].(string); ok {
		body["response_format"] = map[string]any{"type": rf}
	}
	if stop, ok := body["stop"].([]any); ok && len(stop) == 0 {
		delete(body, "stop")
	}
	if tools, ok := body["tools"].([]any); ok && len(tools) == 0 {
		delete(body, "tools")
	}
	m := rt.Model
	if strings.HasPrefix(m, "o1") || strings.HasPrefix(m, "o3") || strings.HasPrefix(m, "o4") || strings.HasPrefix(m, "gpt-5") {
		if v, ok := body["max_tokens"]; ok {
			body["max_completion_tokens"] = v
			delete(body, "max_tokens")
		}
	}
	if emit != nil {
		body["stream"] = true
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	headers, err := c.keyHeaders(ctx, rt)
	if err != nil {
		return nil, err
	}
	limit := MaxCompletionResponseBytes
	if emit != nil {
		limit = MaxStreamBytes
	}
	resp, err := c.post(ctx, rt, "/chat/completions", headers, body, limit)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if emit == nil {
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, decodeErr("provider response", err)
		}
		return out, nil
	}
	return nil, readSSE(resp.Body, func(event, data string) error {
		if data == "[DONE]" {
			return io.EOF
		}
		var ch map[string]any
		if err := json.Unmarshal([]byte(data), &ch); err != nil {
			return nil
		}
		return emit(ch)
	})
}

// post sends a provider request to rt.BaseURL+path. The response body is
// limited to limit bytes (after decompression). When the base URL came from
// a stored secret, errors do not include it.
func (c *Client) post(ctx context.Context, rt Route, path string, headers map[string]string, body any, limit int64) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rt.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		if rt.urlSecret {
			return nil, errors.New("calling provider: the stored base URL is not a valid URL")
		}
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	hc := c.HTTP
	if rt.operator && c.OperatorHTTP != nil {
		hc = c.OperatorHTTP
	}
	if hc == nil {
		hc = egress.NewClient(10*time.Minute, false)
	}
	resp, err := hc.Do(req)
	if err != nil {
		if rt.urlSecret {
			return nil, fmt.Errorf("calling provider at the stored base URL: %s", NetErrorKind(err))
		}
		return nil, fmt.Errorf("calling provider: %w", egress.Sanitize(err))
	}
	resp.Body = LimitBody(resp.Body, limit)
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		msg := strings.TrimSpace(string(raw))
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
			msg = e.Error.Message
		}
		return nil, fmt.Errorf("provider returned %d: %s", resp.StatusCode, msg)
	}
	return resp, nil
}

// decodeErr reports a failed decode, naming an oversized response plainly.
func decodeErr(what string, err error) error {
	if errors.Is(err, ErrResponseTooLarge) {
		return err
	}
	return fmt.Errorf("decoding %s: %w", what, err)
}

// LimitBody fails reads with ErrResponseTooLarge once more than limit bytes
// were read (limit <= 0: unlimited).
func LimitBody(rc io.ReadCloser, limit int64) io.ReadCloser {
	if limit <= 0 {
		return rc
	}
	return &limitedBody{rc: rc, left: limit}
}

type limitedBody struct {
	rc   io.ReadCloser
	left int64
}

func (l *limitedBody) Read(p []byte) (int, error) {
	if l.left <= 0 {
		// Probe for more data: exactly limit bytes is fine.
		var one [1]byte
		n, err := l.rc.Read(one[:])
		if n > 0 {
			return 0, ErrResponseTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.rc.Read(p)
	l.left -= int64(n)
	return n, err
}

func (l *limitedBody) Close() error { return l.rc.Close() }

// NetErrorKind describes a failed request without the URL, host or address
// (for destinations that are themselves secret).
func NetErrorKind(err error) string {
	var ne interface{ Timeout() bool }
	switch {
	case egress.IsBlocked(err):
		return egress.ErrBlocked.Error()
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return "the request timed out"
	case errors.Is(err, context.Canceled):
		return "the request was cancelled"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "no such host") || strings.Contains(msg, "server misbehaving"):
		return "the host name could not be resolved"
	case strings.Contains(msg, "connection refused"):
		return "the connection was refused"
	case strings.Contains(msg, "tls:") || strings.Contains(msg, "x509:") || strings.Contains(msg, "certificate"):
		return "the TLS handshake failed"
	case strings.Contains(msg, "invalid header"):
		return "a request header is invalid"
	}
	return "the request failed"
}

// readSSE parses a server-sent event stream.
func readSSE(r io.Reader, fn func(event, data string) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	event := ""
	var data []string
	dispatch := func() error {
		if len(data) == 0 {
			event = ""
			return nil
		}
		err := fn(event, strings.Join(data, "\n"))
		event, data = "", nil
		return err
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if err := dispatch(); err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[6:])
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(line[5:], " "))
		}
	}
	if err := dispatch(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return ErrResponseTooLarge
		}
		return err
	}
	return nil
}

// ---------------------------------------------------------------- accumulation

type accumulator struct {
	id, model string
	content   strings.Builder
	toolCalls map[int]map[string]any
	finish    any
	usage     any
	size      int // content and tool-call bytes accumulated
}

func newAccumulator(model string) *accumulator {
	return &accumulator{model: model, toolCalls: map[int]map[string]any{}}
}

// add folds a chunk in; it fails once the accumulated content and tool-call
// arguments exceed MaxStreamContentBytes.
func (a *accumulator) add(ch map[string]any) error {
	if id, ok := ch["id"].(string); ok && id != "" {
		a.id = id
	}
	if m, ok := ch["model"].(string); ok && m != "" {
		a.model = m
	}
	if u, ok := ch["usage"]; ok && u != nil {
		a.usage = u
	}
	choices, _ := ch["choices"].([]any)
	for _, c := range choices {
		cm, _ := c.(map[string]any)
		if fr, ok := cm["finish_reason"]; ok && fr != nil {
			a.finish = fr
		}
		delta, _ := cm["delta"].(map[string]any)
		if s, ok := delta["content"].(string); ok {
			if a.size += len(s); a.size > MaxStreamContentBytes {
				return ErrResponseTooLarge
			}
			a.content.WriteString(s)
		}
		tcs, _ := delta["tool_calls"].([]any)
		for _, t := range tcs {
			tm, _ := t.(map[string]any)
			idx := 0
			if f, ok := store.ToFloat(tm["index"]); ok {
				idx = int(f)
			}
			cur := a.toolCalls[idx]
			if cur == nil {
				cur = map[string]any{"type": "function", "function": map[string]any{"name": "", "arguments": ""}}
				a.toolCalls[idx] = cur
			}
			if id, ok := tm["id"].(string); ok && id != "" {
				cur["id"] = id
			}
			fn, _ := tm["function"].(map[string]any)
			cf := cur["function"].(map[string]any)
			n, _ := fn["name"].(string)
			args, _ := fn["arguments"].(string)
			if a.size += len(n) + len(args); a.size > MaxStreamContentBytes || len(a.toolCalls) > 1024 {
				return ErrResponseTooLarge
			}
			if n != "" {
				cf["name"] = cf["name"].(string) + n
			}
			if args != "" {
				cf["arguments"] = cf["arguments"].(string) + args
			}
		}
	}
	return nil
}

func (a *accumulator) final() map[string]any {
	msg := map[string]any{"role": "assistant", "content": a.content.String()}
	if len(a.toolCalls) > 0 {
		idxs := make([]int, 0, len(a.toolCalls))
		for i := range a.toolCalls {
			idxs = append(idxs, i)
		}
		sort.Ints(idxs)
		var tcs []any
		for _, i := range idxs {
			tcs = append(tcs, a.toolCalls[i])
		}
		msg["tool_calls"] = tcs
	}
	out := map[string]any{
		"id": a.id, "object": "chat.completion", "created": time.Now().Unix(), "model": a.model,
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": a.finish}},
	}
	if a.usage != nil {
		out["usage"] = a.usage
	}
	return out
}

// ---------------------------------------------------------------- mock

func lastUserText(inputs map[string]any) string {
	msgs, _ := inputs["messages"].([]any)
	for i := len(msgs) - 1; i >= 0; i-- {
		m, _ := msgs[i].(map[string]any)
		if m["role"] == "user" {
			return ContentText(m["content"])
		}
	}
	if len(msgs) > 0 {
		m, _ := msgs[len(msgs)-1].(map[string]any)
		return ContentText(m["content"])
	}
	return ""
}

// ContentText flattens OpenAI message content (string or parts) to text.
func ContentText(c any) string {
	switch t := c.(type) {
	case string:
		return t
	case []any:
		var parts []string
		for _, p := range t {
			if m, ok := p.(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					parts = append(parts, s)
				}
			}
		}
		return strings.Join(parts, "\n")
	case nil:
		return ""
	}
	b, _ := json.Marshal(c)
	return string(b)
}

func countWords(inputs map[string]any) int {
	n := 0
	msgs, _ := inputs["messages"].([]any)
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		n += len(strings.Fields(ContentText(mm["content"]))) + 3
	}
	return n
}

func mockComplete(kind, model string, inputs map[string]any) map[string]any {
	text := lastUserText(inputs)
	var content string
	switch kind {
	case "json":
		h := fnv.New32a()
		b, _ := json.Marshal(inputs["messages"])
		h.Write(b)
		v := h.Sum32()
		out := map[string]any{"score": int(v%5) + 1, "correct": v%2 == 0, "answer": text, "reasoning": "mock judgement"}
		bb, _ := json.Marshal(out)
		content = string(bb)
	default:
		content = text
	}
	prompt := countWords(inputs)
	completion := len(strings.Fields(content))
	return map[string]any{
		"id": "chatcmpl-mock-" + uuid.NewString()[:8], "object": "chat.completion", "created": time.Now().Unix(), "model": model,
		"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": prompt, "completion_tokens": completion, "total_tokens": prompt + completion},
	}
}

// ---------------------------------------------------------------- models

// ModelInfo describes a selectable model.
type ModelInfo struct {
	ID        string `json:"id"`
	Provider  string `json:"provider"`
	Label     string `json:"label"`
	Available bool   `json:"available"`
	MaxTokens int    `json:"max_tokens"`
}

var providerKeys = map[string]string{
	"openai": "OPENAI_API_KEY", "anthropic": "ANTHROPIC_API_KEY", "google": "GEMINI_API_KEY",
	"mistral": "MISTRAL_API_KEY", "deepseek": "DEEPSEEK_API_KEY",
}

// Models lists built-in models and whether a key is configured for each.
func (c *Client) Models(ctx context.Context) []ModelInfo {
	out := []ModelInfo{
		{ID: "mock/echo", Provider: "mock", Label: "Mock: echo (offline)", Available: true, MaxTokens: 4096},
		{ID: "mock/json", Provider: "mock", Label: "Mock: JSON judge (offline)", Available: true, MaxTokens: 4096},
	}
	avail := map[string]bool{}
	for p, k := range providerKeys {
		base := anthropicBase()
		if oc, ok := openAICompatible[p]; ok {
			base = oc.BaseURL
		}
		_, avail[p], _ = c.routeKey(ctx, Route{KeyName: k, BaseURL: base, envKey: true, builtin: true, operator: true})
	}
	for _, d := range store.DefaultPrices {
		if d.Provider == "mock" || strings.HasPrefix(d.LLMID, "text-embedding") {
			continue
		}
		id := d.LLMID
		if d.Provider == "google" {
			id = "gemini/" + d.LLMID
		}
		if d.Provider == "mistral" {
			id = d.LLMID + "-latest"
		}
		mt := 8192
		if d.Provider == "anthropic" {
			mt = 8192
		}
		out = append(out, ModelInfo{ID: id, Provider: d.Provider, Label: id, Available: avail[d.Provider], MaxTokens: mt})
	}
	return out
}
