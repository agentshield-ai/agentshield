// Package automations evaluates Automation objects (alerts) and delivers
// their actions: a JSON webhook POST or a Slack incoming-webhook message.
//
// Triggers:
//   - score_threshold: a scorer result (wandb.runnable.<scorer> feedback, as
//     written by monitors and /calls/score) crosses a threshold;
//   - error_rate: the share of failed calls of the given ops over a window
//     reaches a threshold (checked by a background loop);
//   - feedback_created: feedback of the given types is created.
package automations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/egress"
	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// Automation is the parsed value of an Automation object.
type Automation struct {
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	Enabled         *bool    `json:"enabled"`
	Trigger         Trigger  `json:"trigger"`
	Action          Action   `json:"action"`
	CooldownSeconds *float64 `json:"cooldown_seconds"`
}

// Trigger says when an automation fires.
type Trigger struct {
	Type string `json:"type"` // score_threshold | error_rate | feedback_created

	// score_threshold
	Scorer     string  `json:"scorer"`      // scorer name, scorer ref or full feedback type
	MonitorRef string  `json:"monitor_ref"` // only scores produced by this monitor
	Path       string  `json:"path"`        // dotted path into the score
	Operator   string  `json:"operator"`    // gt gte lt lte eq ne (or > >= < <= == !=)
	Threshold  float64 `json:"threshold"`

	// error_rate (Threshold is the error fraction 0..1)
	OpNames       []string `json:"op_names"`
	WindowMinutes float64  `json:"window_minutes"`
	MinCalls      int      `json:"min_calls"`

	// feedback_created
	FeedbackTypes []string `json:"feedback_types"` // exact types or prefixes ending in *
}

// Action is what happens when an automation fires.
type Action struct {
	Type      string            `json:"type"` // webhook | slack
	URL       string            `json:"url"`
	URLSecret string            `json:"url_secret"` // secret holding the URL
	Headers   map[string]string `json:"headers"`
	Message   string            `json:"message"` // optional template, {placeholders} from the event
}

// Parse decodes and validates an Automation value.
func Parse(val any) (*Automation, error) {
	b, err := json.Marshal(val)
	if err != nil {
		return nil, err
	}
	var a Automation
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, store.Invalidf("invalid automation: %v", err)
	}
	return &a, a.Validate()
}

// Validate checks required fields.
func (a *Automation) Validate() error {
	t := &a.Trigger
	switch t.Type {
	case "score_threshold":
		if strings.TrimSpace(t.Scorer) == "" {
			return store.Invalidf("trigger.scorer is required")
		}
		if _, ok := operators[t.Operator]; !ok {
			return store.Invalidf("trigger.operator must be one of gt, gte, lt, lte, eq, ne")
		}
	case "error_rate":
		if t.Threshold < 0 || t.Threshold > 1 {
			return store.Invalidf("trigger.threshold is an error fraction between 0 and 1")
		}
		if t.WindowMinutes < 0 || t.WindowMinutes > 7*24*60 {
			return store.Invalidf("trigger.window_minutes must be between 1 and 10080")
		}
	case "feedback_created":
	default:
		return store.Invalidf("trigger.type must be score_threshold, error_rate or feedback_created")
	}
	switch a.Action.Type {
	case "webhook", "slack":
	default:
		return store.Invalidf("action.type must be webhook or slack")
	}
	if a.Action.URL == "" && a.Action.URLSecret == "" {
		return store.Invalidf("action.url or action.url_secret is required")
	}
	if a.Action.URL != "" {
		if err := checkURL(a.Action.URL); err != nil {
			return err
		}
	}
	return nil
}

func checkURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return store.Invalidf("action url must be an http(s) URL")
	}
	return nil
}

// IsEnabled reports whether the automation is on (default true).
func (a *Automation) IsEnabled() bool { return a.Enabled == nil || *a.Enabled }

// Cooldown is the minimum time between two (non-test) fires.
func (a *Automation) Cooldown() time.Duration {
	if a.CooldownSeconds != nil {
		return time.Duration(math.Max(0, *a.CooldownSeconds) * float64(time.Second))
	}
	if a.Trigger.Type == "error_rate" {
		return a.window()
	}
	return 5 * time.Minute
}

func (a *Automation) window() time.Duration {
	w := a.Trigger.WindowMinutes
	if w <= 0 {
		w = 60
	}
	return time.Duration(w * float64(time.Minute))
}

var operators = map[string]func(a, b float64) bool{
	"gt": func(a, b float64) bool { return a > b }, ">": func(a, b float64) bool { return a > b },
	"gte": func(a, b float64) bool { return a >= b }, ">=": func(a, b float64) bool { return a >= b },
	"lt": func(a, b float64) bool { return a < b }, "<": func(a, b float64) bool { return a < b },
	"lte": func(a, b float64) bool { return a <= b }, "<=": func(a, b float64) bool { return a <= b },
	"eq": func(a, b float64) bool { return a == b }, "==": func(a, b float64) bool { return a == b },
	"ne": func(a, b float64) bool { return a != b }, "!=": func(a, b float64) bool { return a != b },
}

// SecretFunc looks up a secret by name.
type SecretFunc func(ctx context.Context, name string) (string, bool)

// Runner evaluates automations and delivers actions.
type Runner struct {
	Store    *store.Store
	Secret   SecretFunc
	Log      *slog.Logger
	HTTP     *http.Client
	Interval time.Duration // error-rate check interval

	feedbackCh chan store.Feedback
	stop       chan struct{}
	stopOnce   sync.Once
	wg         sync.WaitGroup
	unhook     func()

	mu       sync.Mutex
	cache    map[string]cacheEntry
	lastFire map[string]time.Time
	loaded   map[string]bool
}

type cacheEntry struct {
	at   time.Time
	objs []*store.Obj
}

// New creates a runner; call Start to begin background work.
func New(st *store.Store, secret SecretFunc, log *slog.Logger) *Runner {
	if log == nil {
		log = slog.Default()
	}
	return &Runner{Store: st, Secret: secret, Log: log, HTTP: egress.NewClient(15*time.Second, true), Interval: time.Minute,
		feedbackCh: make(chan store.Feedback, 10000), stop: make(chan struct{}),
		cache: map[string]cacheEntry{}, lastFire: map[string]time.Time{}, loaded: map[string]bool{}}
}

// Start hooks feedback creation and starts the background loop.
func (r *Runner) Start() {
	r.unhook = r.Store.OnFeedbackCreated(func(f store.Feedback) {
		select {
		case r.feedbackCh <- f:
		default:
			r.Log.Warn("automation queue full; dropping feedback event", "feedback_id", f.ID)
		}
	})
	r.wg.Add(1)
	go r.loop()
}

// Close stops background work.
func (r *Runner) Close() {
	r.stopOnce.Do(func() {
		if r.unhook != nil {
			r.unhook()
		}
		close(r.stop)
	})
	r.wg.Wait()
}

func (r *Runner) loop() {
	defer r.wg.Done()
	interval := r.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-r.stop:
			return
		case f := <-r.feedbackCh:
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			r.HandleFeedback(ctx, f)
			cancel()
		case <-tick.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			if _, err := r.CheckErrorRates(ctx, ""); err != nil {
				r.Log.Warn("automation error-rate check failed", "err", err)
			}
			cancel()
		}
	}
}

// Invalidate drops cached automations for a project.
func (r *Runner) Invalidate(projectID string) {
	r.mu.Lock()
	delete(r.cache, projectID)
	r.mu.Unlock()
}

func (r *Runner) automations(ctx context.Context, projectID string) ([]*store.Obj, error) {
	r.mu.Lock()
	ent, ok := r.cache[projectID]
	r.mu.Unlock()
	if ok && time.Since(ent.at) < 5*time.Second {
		return ent.objs, nil
	}
	objs, err := r.Store.QueryObjects(ctx, store.ObjsQuery{ProjectID: projectID,
		Filter: &store.ObjsFilter{BaseObjectClasses: []string{"Automation"}, LatestOnly: true}})
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.cache[projectID] = cacheEntry{at: time.Now(), objs: objs}
	r.mu.Unlock()
	return objs, nil
}

func feedbackCallID(f store.Feedback) string {
	if rf, err := refs.Parse(f.WeaveRef); err == nil && rf.Kind == refs.KindCall {
		return rf.Name
	}
	return ""
}

// ScorerFeedbackType maps a trigger's scorer field to the feedback type it watches.
func ScorerFeedbackType(scorer string) string {
	scorer = strings.TrimSpace(scorer)
	if strings.HasPrefix(scorer, "wandb.") {
		return scorer
	}
	if refs.IsRef(scorer) {
		if rf, err := refs.Parse(refs.Normalize(scorer)); err == nil {
			scorer = rf.Name
		}
	}
	return "wandb.runnable." + scorer
}

// scoreValue extracts the compared number from score feedback.
func scoreValue(f store.Feedback, path string) (float64, any, bool) {
	p, _ := f.Payload.(map[string]any)
	var v any
	if o, ok := p["output"]; ok {
		v = o
	} else {
		v = p["value"]
	}
	if path != "" {
		for _, seg := range strings.Split(path, ".") {
			m, ok := v.(map[string]any)
			if !ok {
				return 0, nil, false
			}
			v = m[seg]
		}
	}
	switch x := v.(type) {
	case bool:
		if x {
			return 1, v, true
		}
		return 0, v, true
	}
	n, ok := store.ToFloat(v)
	return n, v, ok
}

func typeMatches(patterns []string, t string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == t || (strings.HasSuffix(p, "*") && strings.HasPrefix(t, strings.TrimSuffix(p, "*"))) {
			return true
		}
	}
	return false
}

// HandleFeedback evaluates score_threshold and feedback_created triggers.
func (r *Runner) HandleFeedback(ctx context.Context, f store.Feedback) []*store.AutomationRun {
	objs, err := r.automations(ctx, f.ProjectID)
	if err != nil {
		r.Log.Warn("loading automations failed", "err", err)
		return nil
	}
	var runs []*store.AutomationRun
	for _, o := range objs {
		a, err := Parse(o.Val)
		if err != nil || !a.IsEnabled() {
			continue
		}
		var event map[string]any
		switch a.Trigger.Type {
		case "score_threshold":
			if f.FeedbackType != ScorerFeedbackType(a.Trigger.Scorer) {
				continue
			}
			if mr := a.Trigger.MonitorRef; mr != "" && (f.TriggerRef == nil || refs.Normalize(mr) != *f.TriggerRef) {
				continue
			}
			n, raw, ok := scoreValue(f, a.Trigger.Path)
			if !ok || !operators[a.Trigger.Operator](n, a.Trigger.Threshold) {
				continue
			}
			event = map[string]any{"type": "score_threshold", "feedback_id": f.ID, "feedback_type": f.FeedbackType, "weave_ref": f.WeaveRef,
				"call_id": feedbackCallID(f), "value": raw, "path": a.Trigger.Path, "operator": a.Trigger.Operator, "threshold": a.Trigger.Threshold,
				"monitor_ref": f.TriggerRef}
		case "feedback_created":
			if !typeMatches(a.Trigger.FeedbackTypes, f.FeedbackType) {
				continue
			}
			event = map[string]any{"type": "feedback_created", "feedback_id": f.ID, "feedback_type": f.FeedbackType, "weave_ref": f.WeaveRef,
				"call_id": feedbackCallID(f), "payload": f.Payload, "creator": f.Creator, "wb_user_id": f.WBUserID, "queue_id": f.QueueID}
		default:
			continue
		}
		if run := r.fireWithCooldown(ctx, o, a, event); run != nil {
			runs = append(runs, run)
		}
	}
	return runs
}

// CheckErrorRates evaluates error_rate triggers for one project ("" = all).
func (r *Runner) CheckErrorRates(ctx context.Context, projectID string) ([]*store.AutomationRun, error) {
	var pids []string
	if projectID != "" {
		pids = []string{projectID}
	} else {
		ps, err := r.Store.ListProjects(ctx)
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			pids = append(pids, p.ProjectID)
		}
	}
	var runs []*store.AutomationRun
	for _, pid := range pids {
		objs, err := r.automations(ctx, pid)
		if err != nil {
			return runs, err
		}
		for _, o := range objs {
			a, err := Parse(o.Val)
			if err != nil || !a.IsEnabled() || a.Trigger.Type != "error_rate" {
				continue
			}
			ops := make([]string, 0, len(a.Trigger.OpNames))
			for _, n := range a.Trigger.OpNames {
				if n = strings.TrimSpace(n); n != "" {
					ops = append(ops, store.OpBaseName(refs.Normalize(n)))
				}
			}
			since := time.Now().Add(-a.window())
			total, errs, err := r.Store.ErrorRate(ctx, pid, ops, store.Micros(since))
			if err != nil {
				return runs, err
			}
			minCalls := int64(max(a.Trigger.MinCalls, 1))
			if total < minCalls {
				continue
			}
			rate := float64(errs) / float64(total)
			if rate < a.Trigger.Threshold || errs == 0 {
				continue
			}
			event := map[string]any{"type": "error_rate", "total_calls": total, "error_calls": errs, "error_rate": rate,
				"threshold": a.Trigger.Threshold, "window_minutes": a.window().Minutes(), "op_names": a.Trigger.OpNames}
			if run := r.fireWithCooldown(ctx, o, a, event); run != nil {
				runs = append(runs, run)
			}
		}
	}
	return runs, nil
}

func (r *Runner) fireWithCooldown(ctx context.Context, o *store.Obj, a *Automation, event map[string]any) *store.AutomationRun {
	key := o.ProjectID + "\x00" + o.ObjectID
	r.mu.Lock()
	if !r.loaded[key] {
		r.loaded[key] = true
		r.mu.Unlock()
		if us, err := r.Store.LastAutomationFire(ctx, o.ProjectID, o.ObjectID); err == nil && us > 0 {
			r.mu.Lock()
			if t := time.UnixMicro(us); t.After(r.lastFire[key]) {
				r.lastFire[key] = t
			}
			r.mu.Unlock()
		}
		r.mu.Lock()
	}
	last := r.lastFire[key]
	if cd := a.Cooldown(); cd > 0 && !last.IsZero() && time.Since(last) < cd {
		r.mu.Unlock()
		return nil
	}
	r.lastFire[key] = time.Now()
	r.mu.Unlock()
	run, err := r.Fire(ctx, o, a, event, false)
	if err != nil {
		r.Log.Warn("automation run not recorded", "automation", o.ObjectID, "err", err)
	}
	return run
}

// SampleEvent builds a representative event for a test fire.
func SampleEvent(a *Automation) map[string]any {
	switch a.Trigger.Type {
	case "score_threshold":
		return map[string]any{"type": "score_threshold", "feedback_type": ScorerFeedbackType(a.Trigger.Scorer), "call_id": "test-call",
			"value": a.Trigger.Threshold, "path": a.Trigger.Path, "operator": a.Trigger.Operator, "threshold": a.Trigger.Threshold}
	case "error_rate":
		return map[string]any{"type": "error_rate", "total_calls": 10, "error_calls": 5, "error_rate": 0.5,
			"threshold": a.Trigger.Threshold, "window_minutes": a.window().Minutes(), "op_names": a.Trigger.OpNames}
	}
	ft := "wandb.note.1"
	if len(a.Trigger.FeedbackTypes) > 0 {
		ft = strings.TrimSuffix(a.Trigger.FeedbackTypes[0], "*")
	}
	return map[string]any{"type": "feedback_created", "feedback_type": ft, "call_id": "test-call", "payload": map[string]any{"note": "test"}}
}

// Summary is a one-line description of an event.
func Summary(event map[string]any) string {
	f := func(v any) string {
		switch x := v.(type) {
		case float64:
			return fmt.Sprintf("%.4g", x)
		case nil:
			return ""
		}
		b, _ := json.Marshal(v)
		return strings.Trim(string(b), `"`)
	}
	switch event["type"] {
	case "score_threshold":
		s := fmt.Sprintf("%s", f(event["feedback_type"]))
		if p := f(event["path"]); p != "" {
			s += "." + p
		}
		return fmt.Sprintf("score %s = %s (%s %s) on call %s", s, f(event["value"]), f(event["operator"]), f(event["threshold"]), f(event["call_id"]))
	case "error_rate":
		rate, _ := event["error_rate"].(float64)
		return fmt.Sprintf("error rate %.1f%% (%v of %v calls) over %v min reached threshold %.1f%%", rate*100, event["error_calls"], event["total_calls"],
			event["window_minutes"], toF(event["threshold"])*100)
	case "feedback_created":
		return fmt.Sprintf("feedback %s created on call %s", f(event["feedback_type"]), f(event["call_id"]))
	}
	return "automation fired"
}

func toF(v any) float64 {
	n, _ := store.ToFloat(v)
	return n
}

var placeholderRe = regexp.MustCompile(`\{[A-Za-z_][A-Za-z0-9_]*\}`)

// Render fills {placeholders} in a message template from vars.
func Render(tmpl string, vars map[string]any) string {
	return placeholderRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		v, ok := vars[m[1:len(m)-1]]
		if !ok {
			return m
		}
		switch x := v.(type) {
		case string:
			return x
		case nil:
			return ""
		}
		b, _ := json.Marshal(v)
		return string(b)
	})
}

// Fire delivers the action and records the run. test marks manual test fires
// (which ignore cooldowns and the enabled flag).
func (r *Runner) Fire(ctx context.Context, o *store.Obj, a *Automation, event map[string]any, test bool) (*store.AutomationRun, error) {
	run := &store.AutomationRun{ProjectID: o.ProjectID, AutomationID: o.ObjectID, AutomationRef: o.Ref(),
		TriggerType: a.Trigger.Type, ActionType: a.Action.Type, Test: test, Event: event}
	start := time.Now()
	status, body, err := r.deliver(ctx, o, a, event, test)
	ms := float64(time.Since(start).Microseconds()) / 1000
	run.DurationMS = &ms
	if status != 0 {
		run.ResponseStatus = &status
	}
	if body != "" {
		run.ResponseBody = &body
	}
	run.Status = "success"
	if err != nil {
		msg := err.Error()
		run.Error = &msg
		run.Status = "error"
		r.Log.Warn("automation action failed", "automation", o.ObjectID, "err", err)
	}
	if rerr := r.Store.RecordAutomationRun(ctx, run); rerr != nil {
		return run, rerr
	}
	return run, nil
}

func (r *Runner) deliver(ctx context.Context, o *store.Obj, a *Automation, event map[string]any, test bool) (int, string, error) {
	target := strings.TrimSpace(a.Action.URL)
	if a.Action.URLSecret != "" {
		if r.Secret == nil {
			return 0, "", errors.New("secrets unavailable")
		}
		v, ok := r.Secret(ctx, a.Action.URLSecret)
		if !ok || strings.TrimSpace(v) == "" {
			return 0, "", fmt.Errorf("secret %q is not set", a.Action.URLSecret)
		}
		target = strings.TrimSpace(v)
	}
	if err := checkURL(target); err != nil {
		return 0, "", err
	}
	name := a.Name
	if name == "" {
		name = o.ObjectID
	}
	summary := Summary(event)
	vars := map[string]any{"automation": name, "project_id": o.ProjectID, "summary": summary, "test": test}
	for k, v := range event {
		if _, exists := vars[k]; !exists {
			vars[k] = v
		}
	}
	message := fmt.Sprintf("Weft automation %q fired in %s: %s", name, o.ProjectID, summary)
	if test {
		message = "[test] " + message
	}
	if a.Action.Message != "" {
		message = Render(a.Action.Message, vars)
	}
	var payload any
	if a.Action.Type == "slack" {
		payload = map[string]any{"text": message}
	} else {
		payload = map[string]any{
			"automation": map[string]any{"name": name, "object_id": o.ObjectID, "ref": o.Ref()},
			"project_id": o.ProjectID, "trigger": a.Trigger, "event": event, "message": message,
			"fired_at": time.Now().UTC().Format(time.RFC3339Nano), "test": test,
		}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(b))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "weft-automations")
	keys := make([]string, 0, len(a.Action.Headers))
	for k := range a.Action.Headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		req.Header.Set(k, a.Action.Headers[k])
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("delivering webhook: %w", egress.Sanitize(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return resp.StatusCode, string(raw), fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return resp.StatusCode, string(raw), nil
}
