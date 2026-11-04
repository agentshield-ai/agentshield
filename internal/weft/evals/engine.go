// Package evals runs server-side evaluations, scorers and monitors, and
// assembles evaluation comparisons and leaderboards.
package evals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/llm"
	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/google/uuid"
)

// Engine owns evaluation state and the monitor worker pool.
type Engine struct {
	Store *store.Store
	LLM   *llm.Client
	Log   *slog.Logger

	opMu   sync.Mutex
	opRefs map[string]string

	statusMu sync.Mutex
	status   map[string]*Status

	monitorCh    chan store.EndedCall
	monMu        sync.Mutex
	monitorCache map[string]monitorCacheEntry
	// Sessions waiting to complete for scope monitors (monitors_scope.go),
	// with the time their latest call ended.
	pendMu   sync.Mutex
	pending  map[pendingKey]time.Time
	wg       sync.WaitGroup
	stopOnce sync.Once
	stop     chan struct{}
}

// Status is the progress of a server-side evaluation.
type Status struct {
	Code          string `json:"code"`
	CompletedRows int    `json:"completed_rows"`
	TotalRows     int    `json:"total_rows"`
	Error         string `json:"error,omitempty"`
}

// New creates an engine and starts monitor workers.
func New(s *store.Store, client *llm.Client, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	e := &Engine{
		Store: s, LLM: client, Log: log,
		opRefs: map[string]string{}, status: map[string]*Status{},
		monitorCh: make(chan store.EndedCall, 10000), monitorCache: map[string]monitorCacheEntry{},
		pending: map[pendingKey]time.Time{}, stop: make(chan struct{}),
	}
	for i := 0; i < 4; i++ {
		e.wg.Add(1)
		go e.monitorWorker()
	}
	e.wg.Add(1)
	go e.scopeWorker()
	return e
}

// Close stops background workers.
func (e *Engine) Close() {
	e.stopOnce.Do(func() { close(e.stop) })
	e.wg.Wait()
}

// ---------------------------------------------------------------- call recording

const builtinOpCode = "# Built-in Weft server-side operation.\n# Executed by the Weft server; see docs/weft/API.md."

// opRef publishes (once) a server-side Op object and returns its ref.
func (e *Engine) opRef(ctx context.Context, projectID, name string) (string, error) {
	key := projectID + "\x00" + name
	e.opMu.Lock()
	if r, ok := e.opRefs[key]; ok {
		e.opMu.Unlock()
		return r, nil
	}
	e.opMu.Unlock()
	objID := store.SanitizeObjectID(name)
	val := map[string]any{"_type": "Op", "name": name, "code": builtinOpCode, "language": "text", "signature": "(...)"}
	d, _, err := e.Store.CreateObject(ctx, projectID, objID, val, "", nil)
	if err != nil {
		return "", err
	}
	r := refs.ObjectRef(projectID, objID, d, true)
	e.opMu.Lock()
	e.opRefs[key] = r
	e.opMu.Unlock()
	return r, nil
}

type callRec struct {
	e         *Engine
	projectID string
	id        string
	traceID   string
	start     time.Time
}

func newID() string { return uuid.Must(uuid.NewV7()).String() }

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// startCall records a call start. attrs gets weft.internal when internal is set.
func (e *Engine) startCall(ctx context.Context, projectID, opName string, parent *callRec, inputs, attrs map[string]any, display *string) (*callRec, error) {
	op, err := e.opRef(ctx, projectID, opName)
	if err != nil {
		return nil, err
	}
	rec := &callRec{e: e, projectID: projectID, id: newID(), start: time.Now()}
	st := store.CallStart{ProjectID: projectID, ID: rec.id, OpName: op, StartedAt: ts(rec.start), Inputs: inputs, Attributes: attrs, DisplayName: display}
	if parent != nil {
		rec.traceID = parent.traceID
		p := parent.id
		st.ParentID = &p
	} else {
		rec.traceID = rec.id
	}
	st.TraceID = rec.traceID
	if err := e.Store.StartCall(ctx, st); err != nil {
		return nil, err
	}
	return rec, nil
}

func (r *callRec) end(ctx context.Context, output any, summary map[string]any, callErr error) error {
	en := store.CallEnd{ProjectID: r.projectID, ID: r.id, EndedAt: ts(time.Now()), Output: jsonSafe(output), Summary: summary}
	if callErr != nil {
		msg := callErr.Error()
		en.Exception = &msg
	}
	return r.e.Store.EndCall(ctx, en)
}

// jsonSafe round-trips v through JSON so stored values use json.Number etc.
func jsonSafe(v any) any {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	out, err := refs.Decode(b)
	if err != nil {
		return nil
	}
	return out
}

// usageSummary converts an OpenAI response's usage into a call summary.
func usageSummary(resp map[string]any, model string) map[string]any {
	u, ok := resp["usage"].(map[string]any)
	if !ok {
		return map[string]any{}
	}
	if m, ok := resp["model"].(string); ok && m != "" {
		model = m
	}
	entry := map[string]any{"requests": 1}
	norm := store.NormalizeUsage(u) // lifts cached_tokens etc. to the canonical cache fields
	for _, k := range []string{"prompt_tokens", "completion_tokens", "total_tokens", "cache_read_input_tokens",
		"cache_creation_input_tokens", "reasoning_tokens"} {
		if v, ok := norm[k]; ok {
			entry[k] = v
		}
	}
	return map[string]any{"usage": map[string]any{model: entry}}
}

// TrackedCompletion runs a completion and records it as a call.
func (e *Engine) TrackedCompletion(ctx context.Context, projectID string, parent *callRec, inputs map[string]any, internal bool) (map[string]any, string, error) {
	attrs := map[string]any{}
	if internal {
		attrs["weft"] = map[string]any{"internal": true}
	}
	rec, err := e.startCall(ctx, projectID, "weft.completions.create", parent, inputs, attrs, nil)
	if err != nil {
		return nil, "", err
	}
	model, _ := inputs["model"].(string)
	resp, callErr := e.LLM.Complete(ctx, projectID, inputs)
	var summary map[string]any
	if callErr == nil {
		summary = usageSummary(resp, model)
	}
	if err := rec.end(ctx, resp, summary, callErr); err != nil {
		return nil, rec.id, err
	}
	return resp, rec.id, callErr
}

// RecordStreamedCompletion records a completion that was streamed elsewhere.
func (e *Engine) RecordStreamedCompletion(ctx context.Context, projectID string, inputs map[string]any, started time.Time, resp map[string]any, callErr error) (string, error) {
	op, err := e.opRef(ctx, projectID, "weft.completions.create")
	if err != nil {
		return "", err
	}
	id := newID()
	model, _ := inputs["model"].(string)
	en := &store.CallEnd{EndedAt: ts(time.Now()), Output: jsonSafe(resp), Summary: usageSummary(resp, model)}
	if callErr != nil {
		msg := callErr.Error()
		en.Exception = &msg
	}
	_, err = e.Store.UpsertComplete(ctx, []store.CompleteCall{{
		Start: store.CallStart{ProjectID: projectID, ID: id, TraceID: id, OpName: op, StartedAt: ts(started), Inputs: inputs},
		End:   en,
	}})
	return id, err
}

// ---------------------------------------------------------------- object helpers

// loadObject resolves an object ref to its ObjSchema.
func (e *Engine) loadObject(ctx context.Context, ref string) (*store.Obj, map[string]any, error) {
	r, err := refs.Parse(ref)
	if err != nil {
		return nil, nil, &store.InvalidError{Msg: err.Error()}
	}
	if r.Kind != refs.KindObject && r.Kind != refs.KindFunction {
		return nil, nil, store.Invalidf("%q is not an object ref", ref)
	}
	o, err := e.Store.ReadObject(ctx, r.ProjectID(), r.Name, r.Digest)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, store.Invalidf("object %q not found", ref)
		}
		return nil, nil, err
	}
	m, _ := o.Val.(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	return o, m, nil
}

func className(val map[string]any) string {
	c, _ := val["_class_name"].(string)
	if c == "" {
		c, _ = val["_type"].(string)
	}
	return c
}

// refName returns a display name for a ref or value.
func refName(v any) string {
	s, ok := v.(string)
	if !ok {
		if m, ok := v.(map[string]any); ok {
			if n, ok := m["name"].(string); ok {
				return n
			}
		}
		if v == nil {
			return ""
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	if r, err := refs.Parse(s); err == nil {
		return r.Name
	}
	return s
}

var placeholderRe = regexp.MustCompile(`\{\{|\}\}|\{([A-Za-z_][A-Za-z0-9_.]*)\}`)

// Render fills {name} placeholders from vars. Unknown placeholders are left
// as-is; {{ and }} are literal braces.
func Render(tmpl string, vars map[string]any) string {
	return placeholderRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		switch m {
		case "{{":
			return "{"
		case "}}":
			return "}"
		}
		key := m[1 : len(m)-1]
		var v any
		var ok bool
		if v, ok = vars[key]; !ok {
			cur := any(vars)
			ok = true
			for _, seg := range strings.Split(key, ".") {
				mm, isMap := cur.(map[string]any)
				if !isMap {
					ok = false
					break
				}
				if cur, ok = mm[seg]; !ok {
					break
				}
			}
			v = cur
		}
		if !ok {
			return m
		}
		return Stringify(v)
	})
}

// Stringify renders a value as text for prompts and comparisons.
func Stringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
