package server

// AgentShield detections (docs/weft/DESIGN.md §3, §5):
//
//	GET  /api/v1/detections            query detections
//	GET  /api/v1/detections/stats      counts by action, severity, rule, agent and day
//	POST /api/v1/shield/ingest         the engine pushes verdicts and overrides
//	GET  /api/v1/shield/insights       approval fatigue, override escalation, cross-agent correlation
//	GET  /api/v1/shield/status         whether replay is configured
//
// Replay routes are in shield_replay.go.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/agentshield-ai/agentshield/internal/weft/evals"
	"github.com/agentshield-ai/agentshield/internal/weft/shield"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

func init() {
	routeExtensions = append(routeExtensions, func(s *Server, r chi.Router) {
		r.Get("/api/v1/detections", s.detectionsList)
		r.Get("/api/v1/detections/stats", s.detectionsStats)
		r.Post("/api/v1/shield/ingest", s.shieldIngestHandler)
		r.Get("/api/v1/shield/insights", s.shieldInsights)
		r.Get("/api/v1/shield/status", s.shieldStatus)
		r.Post("/api/v1/shield/replay", handle(s.shieldReplayStart))
		r.Get("/api/v1/shield/replay", s.shieldReplayList)
		r.Get("/api/v1/shield/replay/{job_id}", s.shieldReplayGet)
	})
	startExtensions = append(startExtensions, func(s *Server) func() {
		return func() {
			if v, ok := shieldServices.LoadAndDelete(s); ok {
				svc := v.(*shield.Service)
				evals.RegisterToolSafetyReplayer(s.store, nil)
				svc.Close()
			}
		}
	})
}

var shieldServices sync.Map // *Server -> *shield.Service

// SetShield installs the replay service (cmd/weft --shield-* flags). The
// server closes it on Close.
func (s *Server) SetShield(svc *shield.Service) {
	if svc == nil {
		shieldServices.Delete(s)
		evals.RegisterToolSafetyReplayer(s.store, nil)
		return
	}
	shieldServices.Store(s, svc)
	evals.RegisterToolSafetyReplayer(s.store, func(ctx context.Context, projectID string, sessionIDs []string, label string) (string, error) {
		req := shield.ReplayRequest{ProjectID: projectID, SessionIDs: sessionIDs, Label: label}
		if err := req.Validate(); err != nil {
			return "", err
		}
		id, err := svc.CreateJob(ctx, req, nil)
		if err != nil {
			return "", err
		}
		res, err := svc.Run(ctx, projectID, id, req)
		if err != nil {
			return "", err
		}
		return res.Label, nil
	})
}

func (s *Server) shieldService() *shield.Service {
	if v, ok := shieldServices.Load(s); ok {
		return v.(*shield.Service)
	}
	return nil
}

// ---------------------------------------------------------------- query helpers

func shieldQueryProject(r *http.Request) (string, error) {
	p := r.URL.Query().Get("project_id")
	if p == "" {
		return "", store.Invalidf("project_id is required")
	}
	return project(p)
}

func shieldQueryInt(r *http.Request, name string, def int) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, store.Invalidf("%s must be a non-negative integer", name)
	}
	return n, nil
}

func shieldQueryBool(r *http.Request, name string) (*bool, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return nil, store.Invalidf("%s must be true or false", name)
	}
	return &b, nil
}

func shieldQueryList(r *http.Request, name string) []string {
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

func shieldDetectionQuery(r *http.Request) (store.DetectionQuery, error) {
	pid, err := shieldQueryProject(r)
	if err != nil {
		return store.DetectionQuery{}, err
	}
	q := r.URL.Query()
	dq := store.DetectionQuery{
		ProjectID: pid, SessionID: q.Get("session_id"), Agent: q.Get("agent"), RuleID: q.Get("rule_id"),
		Source: q.Get("source"), Ruleset: q.Get("ruleset"), SpanID: q.Get("span_id"), ToolCallID: q.Get("tool_call_id"),
		ToolName: q.Get("tool_name"), Actions: shieldQueryList(r, "action"), SortBy: q.Get("sort_by"),
	}
	// session_ids (repeated, not comma-split: ids may hold commas) narrows to
	// detections in any of these sessions; the UI uses it to apply session
	// filters (team, status, score, …) on the Security page.
	if ids := q["session_ids"]; len(ids) > 0 {
		if len(ids) > maxDetectionSessionIDs {
			return dq, store.Invalidf("at most %d session_ids", maxDetectionSessionIDs)
		}
		dq.SessionIDs = ids
	}
	// session.<filter> (the /api/v1/sessions filters: session.team_id,
	// session.status, session.score_below, session.cost_min, …) keeps
	// detections in the sessions that match; the server resolves them over
	// every matching session, so there is no cap on how many.
	if sq, has, err := sessionsQuery(pid, q, "session."); err != nil {
		return dq, err
	} else if has {
		dq.Sessions = &sq
	}
	if dq.Source != "" && dq.Source != store.DetectionSourceEngine && dq.Source != store.DetectionSourceReplay {
		return dq, store.Invalidf("source must be engine or replay")
	}
	for _, a := range dq.Actions {
		switch a {
		case store.DetectionAllow, store.DetectionLog, store.DetectionRequireApproval, store.DetectionBlock:
		default:
			return dq, store.Invalidf("action must be allow, log, require_approval or block")
		}
	}
	if dq.Overridden, err = shieldQueryBool(r, "overridden"); err != nil {
		return dq, err
	}
	if dq.Linked, err = shieldQueryBool(r, "linked"); err != nil {
		return dq, err
	}
	if dq.From, err = store.MicrosFromQuery(q.Get("from")); err != nil {
		return dq, err
	}
	if dq.To, err = store.MicrosFromQuery(q.Get("to")); err != nil {
		return dq, err
	}
	if dq.Limit, err = shieldQueryInt(r, "limit", 100); err != nil {
		return dq, err
	}
	if dq.Offset, err = shieldQueryInt(r, "offset", 0); err != nil {
		return dq, err
	}
	return dq, nil
}

// shieldGet adapts a GET handler that returns a value or an error.
func shieldGet(fn func(r *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out, err := fn(r)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, out)
	}
}

// ---------------------------------------------------------------- detections

func (s *Server) detectionsList(w http.ResponseWriter, r *http.Request) {
	shieldGet(func(r *http.Request) (any, error) {
		q, err := shieldDetectionQuery(r)
		if err != nil {
			return nil, err
		}
		items, total, err := s.store.QueryDetections(r.Context(), q)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(items))
		for _, d := range items {
			ids = append(ids, d.SessionID)
		}
		titles, err := s.store.SessionTitles(r.Context(), q.ProjectID, uniqueStrings(ids))
		if err != nil {
			return nil, err
		}
		for i := range items {
			items[i].SessionTitle = titles[items[i].SessionID]
		}
		return map[string]any{"items": items, "total_count": total}, nil
	})(w, r)
}

func (s *Server) detectionsStats(w http.ResponseWriter, r *http.Request) {
	shieldGet(func(r *http.Request) (any, error) {
		q, err := shieldDetectionQuery(r)
		if err != nil {
			return nil, err
		}
		return s.store.DetectionStatsFor(r.Context(), q)
	})(w, r)
}

// ---------------------------------------------------------------- ingest

type ingestRule struct {
	ID       string   `json:"id"`
	RuleID   string   `json:"rule_id"`
	Title    string   `json:"title"`
	RuleName string   `json:"rule_name"`
	Level    string   `json:"level"`
	Severity string   `json:"severity"`
	Tags     []string `json:"tags"`
}

type ingestDetection struct {
	EventID     string       `json:"event_id"`
	ID          string       `json:"id"` // alias of event_id
	SessionID   string       `json:"session_id"`
	ToolCallID  string       `json:"tool_call_id"`
	ToolName    string       `json:"tool_name"`
	AgentName   string       `json:"agent_name"`
	Action      string       `json:"action"`
	Mode        string       `json:"mode"`
	Severity    string       `json:"severity"`
	Rules       []ingestRule `json:"rules"`
	Source      string       `json:"source"`
	Ruleset     string       `json:"ruleset"`
	EventSource string       `json:"event_source"`
	Overridden  bool         `json:"overridden"`
	Timestamp   string       `json:"timestamp"`
	CreatedAt   string       `json:"created_at"`
}

type ingestOverride struct {
	EventID   string `json:"event_id"`
	SessionID string `json:"session_id"`
	Timestamp string `json:"timestamp"`
}

type ingestReq struct {
	ProjectID  string            `json:"project_id"`
	Detections []ingestDetection `json:"detections"`
	Detection  *ingestDetection  `json:"detection"`
	Overrides  []ingestOverride  `json:"overrides"`
}

// maxIngestItems bounds detections and overrides per ingest request.
const maxIngestItems = 1000

// maxDetectionSessionIDs caps session_ids on a detections query (the UI sends at most one page of 1000 sessions).
const maxDetectionSessionIDs = 1000

// ingestError reports one record of an ingest request that was not stored.
type ingestError struct {
	Kind    string `json:"kind"` // detection or override
	Index   int    `json:"index"`
	EventID string `json:"event_id,omitempty"`
	Error   string `json:"error"`
}

// maxIngestErrors bounds the errors listed in an ingest response (rejected
// counts them all).
const maxIngestErrors = 100

// shieldIngestHandler stores what an ingest request holds that is valid and
// reports each invalid record on its own (docs/weft/API.md): one bad record
// never takes the rest of its batch down with it. The response is 200 when
// at least one record was stored, listing the rejected ones in `errors`;
// when every record is invalid it is 400, with the same `errors` list
// beside the usual `reason` (as before for a request of one bad record).
func (s *Server) shieldIngestHandler(w http.ResponseWriter, r *http.Request) {
	req := new(ingestReq)
	if err := decodeBody(r, req); err != nil {
		writeErr(w, err)
		return
	}
	out, err := s.shieldIngest(r.Context(), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	if out.allRejected {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(map[string]any{"reason": out.Errors[0].Kind + "s[" + strconv.Itoa(out.Errors[0].Index) + "]: " + out.Errors[0].Error,
			"accepted": 0, "rejected": out.Rejected, "errors": out.Errors})
		return
	}
	writeJSON(w, out)
}

type ingestResult struct {
	Accepted         int                     `json:"accepted"`
	Created          int                     `json:"created"`
	Duplicates       int                     `json:"duplicates"`
	OverridesApplied int                     `json:"overrides_applied"`
	OverridesPending int                     `json:"overrides_pending"`
	IDs              []store.DetectionUpsert `json:"ids"`
	// Rejected counts the detections and overrides not stored because they
	// are invalid; Errors lists them (at most maxIngestErrors).
	Rejected    int           `json:"rejected"`
	Errors      []ingestError `json:"errors"`
	allRejected bool
}

func (s *Server) shieldIngest(ctx context.Context, req *ingestReq) (*ingestResult, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	if req.Detection != nil {
		req.Detections = append(req.Detections, *req.Detection)
	}
	if len(req.Detections) == 0 && len(req.Overrides) == 0 {
		return nil, store.Invalidf("send detections or overrides")
	}
	if len(req.Detections) > maxIngestItems || len(req.Overrides) > maxIngestItems {
		return nil, store.Invalidf("at most %d detections and %d overrides per request", maxIngestItems, maxIngestItems)
	}
	out := &ingestResult{IDs: []store.DetectionUpsert{}, Errors: []ingestError{}}
	reject := func(kind string, i int, eventID string, err error) {
		out.Rejected++
		if len(out.Errors) < maxIngestErrors {
			msg := err.Error()
			var inv *store.InvalidError
			if errors.As(err, &inv) {
				msg = inv.Msg
			}
			out.Errors = append(out.Errors, ingestError{Kind: kind, Index: i, EventID: models.CleanText(eventID, 512), Error: msg})
		}
	}
	inputs := make([]store.DetectionInput, 0, len(req.Detections))
	for i, d := range req.Detections {
		eventID := d.EventID
		if eventID == "" {
			eventID = d.ID
		}
		in, err := ingestDetectionInput(d, eventID)
		if err != nil {
			reject("detection", i, eventID, err)
			continue
		}
		inputs = append(inputs, in)
	}
	ovs := make([]store.DetectionOverride, 0, len(req.Overrides))
	for i, o := range req.Overrides {
		if store.InReplayNamespace(o.EventID, o.SessionID) {
			reject("override", i, o.EventID, store.Invalidf("event ids starting %q and session ids starting %q are reserved for replay and are never pushed",
				store.ReplayEventPrefix, store.ReplaySessionPrefix))
			continue
		}
		var at int64
		if o.Timestamp != "" {
			if at, err = store.ParseTime(o.Timestamp); err != nil {
				reject("override", i, o.EventID, store.Invalidf("invalid timestamp"))
				continue
			}
		}
		ovs = append(ovs, store.DetectionOverride{EventID: o.EventID, SessionID: o.SessionID, At: at})
	}
	if len(inputs) == 0 && len(ovs) == 0 {
		out.allRejected = true
		return out, nil
	}
	if len(inputs) > 0 {
		res, err := s.store.UpsertDetections(ctx, pid, inputs)
		if err != nil {
			return nil, err
		}
		out.IDs = res
		for _, r := range res {
			if r.Created {
				out.Created++
			}
		}
		out.Accepted = len(res)
		out.Duplicates = len(res) - out.Created
	}
	if len(ovs) > 0 {
		ores, err := s.store.ApplyDetectionOverrides(ctx, pid, ovs)
		if err != nil {
			return nil, err
		}
		out.OverridesApplied, out.OverridesPending = ores.Applied, ores.Pending
	}
	return out, nil
}

// ingestDetectionInput validates and normalizes one pushed detection.
func ingestDetectionInput(d ingestDetection, eventID string) (store.DetectionInput, error) {
	// Engines never push replays (the namespace is reserved for them and
	// replay verdicts are not pushed), so a replay-namespaced id here is a
	// misbehaving or forged sender: reject it rather than store it or drop
	// it silently.
	if store.InReplayNamespace(eventID, d.SessionID) {
		return store.DetectionInput{}, store.Invalidf("event ids starting %q and session ids starting %q are reserved for replay and are never pushed",
			store.ReplayEventPrefix, store.ReplaySessionPrefix)
	}
	if d.Source != "" && d.Source != store.DetectionSourceEngine {
		return store.DetectionInput{}, store.Invalidf("source must be engine (replay detections are produced by Weft)")
	}
	ts := d.Timestamp
	if ts == "" {
		ts = d.CreatedAt
	}
	var created int64
	if ts != "" {
		var err error
		if created, err = store.ParseTime(ts); err != nil {
			return store.DetectionInput{}, store.Invalidf("invalid timestamp")
		}
	}
	in := store.DetectionInput{EventID: eventID, SessionID: d.SessionID, ToolCallID: d.ToolCallID, ToolName: d.ToolName,
		AgentName: d.AgentName, Action: d.Action, Mode: d.Mode, Severity: d.Severity, Source: store.DetectionSourceEngine,
		Ruleset: d.Ruleset, EventSource: d.EventSource, Overridden: d.Overridden, CreatedAt: created}
	for _, r := range d.Rules {
		in.Rules = append(in.Rules, store.DetectionRule{ID: shieldFirstNonEmpty(r.ID, r.RuleID), Title: shieldFirstNonEmpty(r.Title, r.RuleName),
			Level: shieldFirstNonEmpty(r.Level, r.Severity), Tags: r.Tags})
	}
	if err := in.Normalize(); err != nil {
		return store.DetectionInput{}, err
	}
	return in, nil
}

func shieldFirstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// ---------------------------------------------------------------- insights

func (s *Server) shieldInsights(w http.ResponseWriter, r *http.Request) {
	shieldGet(func(r *http.Request) (any, error) {
		pid, err := shieldQueryProject(r)
		if err != nil {
			return nil, err
		}
		q := r.URL.Query()
		iq := store.InsightsQuery{ProjectID: pid, Bucket: q.Get("bucket")}
		if iq.From, err = store.MicrosFromQuery(q.Get("from")); err != nil {
			return nil, err
		}
		if iq.To, err = store.MicrosFromQuery(q.Get("to")); err != nil {
			return nil, err
		}
		if iq.FatigueThreshold, err = shieldQueryInt(r, "fatigue_threshold", 0); err != nil {
			return nil, err
		}
		if iq.EscalationThreshold, err = shieldQueryInt(r, "escalation_threshold", 0); err != nil {
			return nil, err
		}
		if iq.Limit, err = shieldQueryInt(r, "limit", 0); err != nil {
			return nil, err
		}
		if w := q.Get("window"); w != "" {
			d, err := time.ParseDuration(w)
			if err != nil || d <= 0 || d > 7*24*time.Hour {
				return nil, store.Invalidf("window must be a duration such as 10m (at most 168h)")
			}
			iq.Window = d
		}
		ins, err := s.store.ShieldInsights(r.Context(), iq)
		if err != nil {
			return nil, err
		}
		var ids []string
		for _, x := range ins.ApprovalFatigue.BySession {
			ids = append(ids, x.SessionID)
		}
		for _, x := range ins.OverrideEscalation.BySession {
			ids = append(ids, x.SessionID)
		}
		titles, err := s.store.SessionTitles(r.Context(), pid, uniqueStrings(ids))
		if err != nil {
			return nil, err
		}
		for i := range ins.ApprovalFatigue.BySession {
			ins.ApprovalFatigue.BySession[i].SessionTitle = titles[ins.ApprovalFatigue.BySession[i].SessionID]
		}
		for i := range ins.OverrideEscalation.BySession {
			ins.OverrideEscalation.BySession[i].SessionTitle = titles[ins.OverrideEscalation.BySession[i].SessionID]
		}
		return ins, nil
	})(w, r)
}

func (s *Server) shieldStatus(w http.ResponseWriter, r *http.Request) {
	shieldGet(func(r *http.Request) (any, error) {
		if _, err := shieldQueryProject(r); err != nil {
			return nil, err
		}
		svc := s.shieldService()
		mode := svc.Mode()
		return map[string]any{"replay_enabled": mode != "", "replay_engine": mode,
			"correlation_window_ms": store.CorrelationWindow.Milliseconds()}, nil
	})(w, r)
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := in[:0:0]
	for _, x := range in {
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
