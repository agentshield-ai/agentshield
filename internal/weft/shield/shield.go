// Package shield re-evaluates recorded agent tool calls with AgentShield's
// rule engine (docs/weft/DESIGN.md §5.4).
//
// A replay rebuilds an AgentShield EvaluationRequest from every execute_tool
// span of the chosen sessions, mapping tool names and arguments exactly as
// internal/replay does for public trace datasets (which in turn mirrors the
// shipped plugins), evaluates it as enforce mode would decide without acting
// on it (detections with mode shadow), stores the results as
// detections with source=replay and a ruleset label, and diffs them against
// the engine's original verdicts for the same tool spans.
//
// Evaluation runs in-process with internal/evaluate (rules loaded fresh from
// a directory for every job, so edited rules take effect on the next
// replay), or against a running engine's /api/v1/evaluate.
package shield

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/engine"
	"github.com/agentshield-ai/agentshield/internal/evaluate"
	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/agentshield-ai/agentshield/internal/replay"
	"github.com/agentshield-ai/agentshield/internal/session"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/google/uuid"
)

// Config configures replay. With RulesDir set, replay runs in-process; with
// only URL set, it calls that engine.
type Config struct {
	// RulesDir is the default ruleset for in-process replay.
	RulesDir string
	// RulesRoots are directories under which a replay request may name its
	// own rules_dir (RulesDir is always allowed).
	RulesRoots []string
	// URL and Token address a running AgentShield engine.
	URL, Token string
	// Timeout bounds each remote evaluation (default 10s).
	Timeout time.Duration
	// MaxConcurrent bounds jobs running at once (default 2).
	MaxConcurrent int
	Logger        *slog.Logger
}

// Service runs replay jobs.
type Service struct {
	cfg    Config
	st     *store.Store
	log    *slog.Logger
	client *http.Client
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	sem    chan struct{}
}

// New validates cfg and builds a service. A service with neither RulesDir
// nor URL is valid but cannot replay (Mode returns "").
func New(cfg Config, st *store.Store) (*Service, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 2
	}
	if cfg.RulesDir != "" {
		dir, err := resolveDir(cfg.RulesDir)
		if err != nil {
			return nil, fmt.Errorf("--shield-rules-dir: %w", err)
		}
		cfg.RulesDir = dir
	}
	roots := cfg.RulesRoots[:0:0]
	for _, r := range cfg.RulesRoots {
		dir, err := resolveDir(r)
		if err != nil {
			return nil, fmt.Errorf("--shield-rules-roots %s: %w", r, err)
		}
		roots = append(roots, dir)
	}
	cfg.RulesRoots = roots
	if cfg.URL != "" {
		if !strings.HasPrefix(cfg.URL, "http://") && !strings.HasPrefix(cfg.URL, "https://") {
			return nil, errors.New("--shield-url must be an http(s) URL")
		}
		cfg.URL = strings.TrimRight(cfg.URL, "/")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		cfg: cfg, st: st, log: cfg.Logger, ctx: ctx, cancel: cancel,
		// The engine URL is operator configuration (a flag), like the PII
		// analyzer or the OIDC issuer, so it is not subject to the egress
		// guard that restricts user-supplied URLs; loopback is expected.
		client: &http.Client{Timeout: cfg.Timeout},
		sem:    make(chan struct{}, cfg.MaxConcurrent),
	}, nil
}

// Mode is "in-process", "remote", or "" when replay is not configured.
func (s *Service) Mode() string {
	switch {
	case s == nil:
		return ""
	case s.cfg.RulesDir != "" || len(s.cfg.RulesRoots) > 0:
		return "in-process"
	case s.cfg.URL != "":
		return "remote"
	}
	return ""
}

// Close cancels running jobs and waits for them.
func (s *Service) Close() {
	if s == nil {
		return
	}
	s.cancel()
	s.wg.Wait()
}

func resolveDir(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s is not a directory", p)
	}
	return real, nil
}

// rulesDirFor resolves a request's rules_dir against the allowed roots.
func (s *Service) rulesDirFor(requested string) (string, error) {
	if requested == "" {
		if s.cfg.RulesDir == "" {
			return "", store.Invalidf("rules_dir is required: the server has no default ruleset (--shield-rules-dir)")
		}
		return s.cfg.RulesDir, nil
	}
	dir, err := resolveDir(requested)
	if err != nil {
		return "", store.Invalidf("rules_dir is not an accessible directory")
	}
	allowed := append([]string{s.cfg.RulesDir}, s.cfg.RulesRoots...)
	for _, root := range allowed {
		if root == "" {
			continue
		}
		if rel, err := filepath.Rel(root, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return dir, nil
		}
	}
	return "", store.Invalidf("rules_dir must be inside --shield-rules-dir or a directory listed in --shield-rules-roots")
}

// RulesetHash fingerprints the rule files of dir ("rules:" + 12 hex digits).
func RulesetHash(dir string) (string, error) {
	h := sha256.New()
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if !d.IsDir() && (ext == ".yml" || ext == ".yaml") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(dir, f)
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), len(b))
		h.Write(b)
	}
	return "rules:" + hex.EncodeToString(h.Sum(nil))[:12], nil
}

// ReplayFilter selects sessions by agent and time when no session ids are given.
type ReplayFilter struct {
	Agent string `json:"agent,omitempty"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
	Limit int    `json:"limit,omitempty"` // tool calls (default and maximum 10000)
}

// ReplayRequest is the body of POST /api/v1/shield/replay.
type ReplayRequest struct {
	ProjectID  string        `json:"project_id"`
	SessionIDs []string      `json:"session_ids,omitempty"`
	Filter     *ReplayFilter `json:"filter,omitempty"`
	// RulesDir picks a ruleset other than the default (in-process only).
	RulesDir string `json:"rules_dir,omitempty"`
	// Label names the ruleset on the stored detections (default: the
	// ruleset hash, or "remote:<host>").
	Label string `json:"label,omitempty"`
	// EventSource is the `source` field rules see when the engine's
	// original verdict for a call did not record one (default "weft").
	EventSource string `json:"event_source,omitempty"`
}

// MaxReplaySessionIDBytes bounds a session id in a replay request. It is
// the recorded id, which OTLP lets be any string (the engine's 256-byte id
// limit applies to the replay session id, which hashes long ids), so the
// bound only keeps requests reasonable.
const MaxReplaySessionIDBytes = 64 << 10

// Validate checks a request.
func (r *ReplayRequest) Validate() error {
	if len(r.SessionIDs) == 0 && (r.Filter == nil || (r.Filter.Agent == "" && r.Filter.From == "" && r.Filter.To == "")) {
		return store.Invalidf("give session_ids or a filter (agent, from, to)")
	}
	if len(r.SessionIDs) > 1000 {
		return store.Invalidf("at most 1000 session_ids")
	}
	for _, id := range r.SessionIDs {
		if id == "" || len(id) > MaxReplaySessionIDBytes {
			return store.Invalidf("invalid session id: give the recorded session id (1 to %d bytes)", MaxReplaySessionIDBytes)
		}
	}
	if len(r.Label) > 128 || strings.ContainsAny(r.Label, "\x00\n\r\t") {
		return store.Invalidf("label must be at most 128 characters without control characters")
	}
	if len(r.EventSource) > 64 {
		return store.Invalidf("event_source is too long")
	}
	return nil
}

// Verdict summarises one evaluation of a tool call.
type Verdict struct {
	Action      string   `json:"action"`
	Severity    string   `json:"severity"`
	RuleIDs     []string `json:"rule_ids"`
	DetectionID string   `json:"detection_id,omitempty"`
	Mode        string   `json:"mode,omitempty"`
}

// DiffItem is one tool call whose verdict changed.
type DiffItem struct {
	SpanID     string   `json:"span_id"`
	TraceID    string   `json:"trace_id"`
	SessionID  string   `json:"session_id"`
	ToolCallID string   `json:"tool_call_id"`
	ToolName   string   `json:"tool_name"`
	AgentName  string   `json:"agent_name"`
	StartedAt  string   `json:"started_at"`
	Before     *Verdict `json:"before"` // nil: the engine left no verdict for this call
	After      Verdict  `json:"after"`
}

// Summary counts a replay's outcome.
type Summary struct {
	ToolCalls         int `json:"tool_calls"`
	Evaluated         int `json:"evaluated"`
	Errors            int `json:"errors"`
	Detections        int `json:"detections"` // replay detections stored (any rule matched)
	Blocked           int `json:"blocked"`
	ApprovalsRequired int `json:"approvals_required"`
	WithEngineVerdict int `json:"with_engine_verdict"`
	NewlyBlocked      int `json:"newly_blocked"`
	NoLongerBlocked   int `json:"no_longer_blocked"`
	Changed           int `json:"changed"`
	Unchanged         int `json:"unchanged"`
}

// Result is a finished replay.
type Result struct {
	Label           string     `json:"label"`
	Ruleset         string     `json:"ruleset"`
	Engine          string     `json:"engine"`
	RulesLoaded     int        `json:"rules_loaded"`
	SessionIDs      []string   `json:"session_ids"`
	Summary         Summary    `json:"summary"`
	NewlyBlocked    []DiffItem `json:"newly_blocked"`
	NoLongerBlocked []DiffItem `json:"no_longer_blocked"`
	Changed         []DiffItem `json:"changed"`
	Truncated       bool       `json:"truncated"`
	FirstError      string     `json:"first_error,omitempty"`
	// ReplaySessions maps the hashed replay session keys this job used
	// (replaySessionKey: "weft-replay:<job>:sha256:<hex>" on the engine) to
	// the recorded session ids; sessions replayed under their own id are
	// not listed.
	ReplaySessions map[string]string `json:"replay_sessions,omitempty"`
}

// maxDiffItems bounds each diff list in a stored result.
const maxDiffItems = 500

// Start validates a request, records a job and runs it in the background.
func (s *Service) Start(ctx context.Context, req ReplayRequest, createdBy *string) (string, error) {
	id, err := s.CreateJob(ctx, req, createdBy)
	if err != nil {
		return "", err
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		select {
		case s.sem <- struct{}{}:
		case <-s.ctx.Done():
			_ = s.st.UpdateReplayJob(context.Background(), req.ProjectID, id, store.ReplayFailed, "", nil, "server shutting down")
			return
		}
		defer func() { <-s.sem }()
		if _, err := s.Run(s.ctx, req.ProjectID, id, req); err != nil {
			s.log.Warn("shield replay failed", "project", req.ProjectID, "job", id, "err", err)
		}
	}()
	return id, nil
}

// CreateJob validates a request and records a queued job without running it.
func (s *Service) CreateJob(ctx context.Context, req ReplayRequest, createdBy *string) (string, error) {
	if s.Mode() == "" {
		return "", store.Invalidf("replay is not configured: start weft with --shield-rules-dir or --shield-url")
	}
	if err := req.Validate(); err != nil {
		return "", err
	}
	if s.Mode() != "in-process" && req.RulesDir != "" {
		return "", store.Invalidf("rules_dir needs in-process replay (--shield-rules-dir)")
	}
	if s.Mode() == "in-process" {
		if _, err := s.rulesDirFor(req.RulesDir); err != nil {
			return "", err
		}
	}
	id := uuid.Must(uuid.NewV7()).String()
	if err := s.st.CreateReplayJob(ctx, req.ProjectID, id, req.Label, req, createdBy); err != nil {
		return "", err
	}
	return id, nil
}

// Run executes a job synchronously and records its status and result.
func (s *Service) Run(ctx context.Context, projectID, jobID string, req ReplayRequest) (*Result, error) {
	_ = s.st.UpdateReplayJob(ctx, projectID, jobID, store.ReplayRunning, "", nil, "")
	res, err := s.run(ctx, projectID, jobID, req)
	if err != nil {
		_ = s.st.UpdateReplayJob(context.Background(), projectID, jobID, store.ReplayFailed, "", nil, err.Error())
		return nil, err
	}
	if err := s.st.UpdateReplayJob(context.Background(), projectID, jobID, store.ReplayDone, res.Label, res, ""); err != nil {
		return nil, err
	}
	return res, nil
}

// evaluator evaluates one replay request (see markReplay).
type evaluator interface {
	evaluate(ctx context.Context, req *models.EvaluationRequest) ([]engine.RuleResult, error)
}

// markReplay namespaces a replay request, the same way in-process and
// remotely. replay is set and the session id is "weft-replay:<job>:<key>"
// (the event id already starts with "weft-replay-"), where key is
// replaySessionKey of the recorded session id, so the engine tags its
// evaluate span agentshield.replay, neither stores nor pushes the verdict,
// keeps the session out of live sessions' cross-session fields, and computes
// the session's own cross-session fields over this job's sessions only
// (session.Registry.Isolate): a replay's verdicts depend on the recording
// and the ruleset, not on live traffic or on other jobs. The engine reserves
// both prefixes for replay, and Weft's OTLP ingest also recognises engines
// that predate the flag by the session id.
//
// Correlation ids the engine would reject (a recorded tool call id or agent
// name with control characters, padding or over its length limit) are left
// out: rules do not read them, and the replay result keeps the recorded
// values. The context is the engine's default, "prod".
func markReplay(req *models.EvaluationRequest, jobID, sessionID string) {
	req.Replay = true
	req.SessionID = store.ReplaySessionPrefix + jobID + ":" + replaySessionKey(jobID, sessionID)
	if !cleanID(req.ToolCallID) {
		req.ToolCallID = ""
	}
	if req.AgentName != "" && models.ValidateString(req.AgentName, 256, "agent_name") != nil {
		req.AgentName = ""
	}
	req.Context = "prod"
}

// replayKeyHashPrefix starts a replay session key that is a hash of the
// recorded session id (replaySessionKey).
const replayKeyHashPrefix = "sha256:"

// replaySessionKey names a recorded session inside its replay job's session
// namespace. It is the recorded id itself when the full replay session id
// is one the engine accepts (at most models.MaxIDLength bytes, valid UTF-8,
// no control characters or padding); otherwise, and for a recorded id that
// itself starts with "sha256:", it is "sha256:" + the hex SHA-256 of the
// recorded id, so every replay session id is valid and distinct sessions
// keep distinct ids. Results are reported under the recorded id (the job's
// replay_sessions lists the hashed keys).
func replaySessionKey(jobID, sessionID string) string {
	if len(store.ReplaySessionPrefix)+len(jobID)+1+len(sessionID) <= models.MaxIDLength && cleanID(sessionID) &&
		!strings.HasPrefix(sessionID, replayKeyHashPrefix) {
		return sessionID
	}
	sum := sha256.Sum256([]byte(sessionID))
	return replayKeyHashPrefix + hex.EncodeToString(sum[:])
}

// cleanID reports whether id is an identifier the engine's HTTP API accepts
// as is (empty is accepted: the field is left out).
func cleanID(id string) bool {
	return id == "" || (len(id) <= models.MaxIDLength && utf8.ValidString(id) && models.CheckID("id", id) == nil)
}

// localEvaluator is one job's in-process engine. Its session registry holds
// only this job's sessions, which is what a remote engine's isolated replay
// scope sees too.
type localEvaluator struct {
	ev *evaluate.Evaluator
}

// newLocalEvaluator builds one job's engine. The engine evaluates a replay
// as enforce mode would act whatever its configured mode, and keeps replay
// sessions in a fixed window
// (session.ReplayMaxEvents events, session.ReplayTTL); the local evaluator
// does the same, so session fields that count verdicts or events
// (session.approval_count, session.tool_count) match a remote replay.
func newLocalEvaluator(eng *engine.Engine) *localEvaluator {
	ev := evaluate.NewEvaluator(eng, config.ModeEnforce, "", nil, nil)
	ev.SetSessionRegistry(session.NewRegistry(session.ReplayMaxEvents, session.ReplayTTL))
	return &localEvaluator{ev: ev}
}

// evaluate applies the engine HTTP API's input validation first, so the
// local evaluator evaluates (and rejects) exactly the requests a remote
// engine does.
func (l *localEvaluator) evaluate(ctx context.Context, req *models.EvaluationRequest) ([]engine.RuleResult, error) {
	if err := models.ValidateEvaluationRequest(req); err != nil {
		return nil, fmt.Errorf("invalid input: %w", err)
	}
	if err := models.CheckReplayNamespace(req); err != nil {
		return nil, fmt.Errorf("invalid input: %w", err)
	}
	resp, err := l.ev.EvaluateWithContext(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Alerts, nil
}

type remoteEvaluator struct {
	s *Service
}

func (r *remoteEvaluator) evaluate(ctx context.Context, req *models.EvaluationRequest) ([]engine.RuleResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		hr, err := http.NewRequestWithContext(ctx, http.MethodPost, r.s.cfg.URL+"/api/v1/evaluate", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		hr.Header.Set("Content-Type", "application/json")
		if r.s.cfg.Token != "" {
			hr.Header.Set("Authorization", "Bearer "+r.s.cfg.Token)
		}
		resp, err := r.s.client.Do(hr)
		if err != nil {
			return nil, fmt.Errorf("engine request failed: %w", err)
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) && attempt < 30 {
			// The engine rate-limits per client address (about 100/min).
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(700 * time.Millisecond):
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("engine returned %d: %s", resp.StatusCode, strings.TrimSpace(string(data[:min(len(data), 200)])))
		}
		var out struct {
			Alerts []engine.RuleResult `json:"alerts"`
		}
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, fmt.Errorf("decoding engine response: %w", err)
		}
		return out.Alerts, nil
	}
}

// BuildRequest rebuilds the evaluation request for a recorded tool call, as
// a shipped plugin would have sent it (internal/replay field mapping).
func BuildRequest(t store.ToolSpan, eventID, eventSource string) *models.EvaluationRequest {
	ev := replay.EventFromToolCall(t.SessionID, t.ToolName, t.Args)
	req := replay.BuildProductionRequest(ev)
	req.EventID = eventID
	req.ToolCallID = t.ToolCallID
	req.AgentName = t.AgentName
	req.Source = eventSource
	req.Fields["source"] = eventSource
	return req
}

func (s *Service) run(ctx context.Context, projectID, jobID string, req ReplayRequest) (*Result, error) {
	res := &Result{Engine: s.Mode(), NewlyBlocked: []DiffItem{}, NoLongerBlocked: []DiffItem{}, Changed: []DiffItem{}}
	var ev evaluator
	if res.Engine == "in-process" {
		dir, err := s.rulesDirFor(req.RulesDir)
		if err != nil {
			return nil, err
		}
		eng, err := engine.NewEngine(dir)
		if err != nil {
			return nil, fmt.Errorf("loading rules: %w", err)
		}
		res.RulesLoaded = len(eng.GetLoadedRules())
		if res.Ruleset, err = RulesetHash(dir); err != nil {
			return nil, err
		}
		ev = newLocalEvaluator(eng)
	} else {
		host := s.cfg.URL
		if i := strings.Index(host, "://"); i >= 0 {
			host = host[i+3:]
		}
		res.Ruleset = "remote:" + host
		ev = &remoteEvaluator{s: s}
	}
	res.Label = req.Label
	if res.Label == "" {
		res.Label = res.Ruleset
	}

	f := store.ToolSpanFilter{SessionIDs: req.SessionIDs}
	if req.Filter != nil {
		f.Agent, f.Limit = req.Filter.Agent, req.Filter.Limit
		var err error
		if f.From, err = store.MicrosFromQuery(req.Filter.From); err != nil {
			return nil, err
		}
		if f.To, err = store.MicrosFromQuery(req.Filter.To); err != nil {
			return nil, err
		}
	}
	spans, err := s.st.ToolSpansForReplay(ctx, projectID, f)
	if err != nil {
		return nil, err
	}
	sessSet := map[string]bool{}
	for _, t := range spans {
		sessSet[t.SessionID] = true
	}
	for id := range sessSet {
		res.SessionIDs = append(res.SessionIDs, id)
		if key := replaySessionKey(jobID, id); key != id {
			if res.ReplaySessions == nil {
				res.ReplaySessions = map[string]string{}
			}
			res.ReplaySessions[key] = id
		}
	}
	sort.Strings(res.SessionIDs)
	originals, err := s.st.EngineDetectionsBySpan(ctx, projectID, res.SessionIDs)
	if err != nil {
		return nil, err
	}

	var inputs []store.DetectionInput
	for i, t := range spans {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		res.Summary.ToolCalls++
		orig := originals[t.SpanID]
		source := req.EventSource
		if orig != nil && orig.EventSource != "" {
			source = orig.EventSource
		}
		if source == "" {
			source = "weft"
		}
		er := BuildRequest(t, fmt.Sprintf("%s%s-%d", store.ReplayEventPrefix, jobID, i), source)
		markReplay(er, jobID, t.SessionID)
		alerts, err := ev.evaluate(ctx, er)
		if err != nil {
			res.Summary.Errors++
			if res.FirstError == "" {
				res.FirstError = err.Error()
			}
			continue
		}
		res.Summary.Evaluated++
		// The rules as a live verdict carries them to Weft (cleaned and
		// selected, evaluate.WeftRules), so a replay names the same rules
		// as the engine's own verdict for the call.
		weftRules := evaluate.WeftRules(alerts)
		after := Verdict{Action: string(evaluate.EnforceAction(alerts)), Severity: evaluate.MaxSeverity(alerts), RuleIDs: ruleIDs(weftRules), Mode: "shadow"}
		switch after.Action {
		case store.DetectionBlock:
			res.Summary.Blocked++
		case store.DetectionRequireApproval:
			res.Summary.ApprovalsRequired++
		}
		if len(alerts) > 0 || after.Action != store.DetectionAllow {
			rules := make([]store.DetectionRule, 0, len(weftRules))
			for _, r := range weftRules {
				rules = append(rules, store.DetectionRule{ID: r.ID, Title: r.Title, Level: r.Level, Tags: r.Tags})
			}
			inputs = append(inputs, store.DetectionInput{
				EventID: "replay:" + res.Label + ":" + t.SpanID, SessionID: t.SessionID, ToolCallID: t.ToolCallID,
				ToolName: t.ToolName, AgentName: t.AgentName, Action: after.Action, Mode: "shadow", Rules: rules,
				Source: store.DetectionSourceReplay, Ruleset: res.Label, EventSource: source, SpanID: t.SpanID, TraceID: t.TraceID,
				ReplayJobID: jobID, CreatedAt: max(t.StartedAt, 1),
			})
		}
		var before *Verdict
		if orig != nil {
			res.Summary.WithEngineVerdict++
			before = &Verdict{Action: effectiveAction(orig), Severity: orig.Severity, DetectionID: orig.ID, Mode: orig.Mode}
			for _, r := range orig.Rules {
				before.RuleIDs = append(before.RuleIDs, r.ID)
			}
			sort.Strings(before.RuleIDs)
			if before.RuleIDs == nil {
				before.RuleIDs = []string{}
			}
		}
		beforeAction := store.DetectionAllow
		if before != nil {
			beforeAction = before.Action
		}
		item := DiffItem{SpanID: t.SpanID, TraceID: t.TraceID, SessionID: t.SessionID, ToolCallID: t.ToolCallID, ToolName: t.ToolName,
			AgentName: t.AgentName, StartedAt: store.FormatMicros(t.StartedAt), Before: before, After: after}
		add := func(list *[]DiffItem, n *int) {
			*n++
			if len(*list) < maxDiffItems {
				*list = append(*list, item)
			} else {
				res.Truncated = true
			}
		}
		beforeRules := []string{}
		if before != nil {
			beforeRules = before.RuleIDs
		}
		switch {
		case after.Action == store.DetectionBlock && beforeAction != store.DetectionBlock:
			add(&res.NewlyBlocked, &res.Summary.NewlyBlocked)
		case beforeAction == store.DetectionBlock && after.Action != store.DetectionBlock:
			add(&res.NoLongerBlocked, &res.Summary.NoLongerBlocked)
		case beforeAction != after.Action || !sameStrings(beforeRules, after.RuleIDs):
			add(&res.Changed, &res.Summary.Changed)
		default:
			res.Summary.Unchanged++
		}
	}
	if err := s.st.DeleteReplayDetections(ctx, projectID, res.Label, res.SessionIDs); err != nil {
		return nil, err
	}
	// Store each session's detections in batches; a batch that cannot be
	// stored is stored one detection at a time, so a detection that cannot
	// be stored counts as an error on its own and never takes the others
	// (of its session or job) with it.
	bySession := map[string][]store.DetectionInput{}
	for _, in := range inputs {
		bySession[in.SessionID] = append(bySession[in.SessionID], in)
	}
	storeErr := func(err error) {
		res.Summary.Errors++
		if res.FirstError == "" {
			res.FirstError = "storing detections: " + err.Error()
		}
	}
	for _, id := range res.SessionIDs {
		batch := bySession[id]
		for i := 0; i < len(batch); i += 500 {
			part := batch[i:min(i+500, len(batch))]
			if _, err := s.st.UpsertDetections(ctx, projectID, part); err == nil {
				res.Summary.Detections += len(part)
				continue
			} else if len(part) == 1 {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				storeErr(err)
				continue
			}
			for _, one := range part {
				if _, err := s.st.UpsertDetections(ctx, projectID, []store.DetectionInput{one}); err != nil {
					if ctx.Err() != nil {
						return nil, ctx.Err()
					}
					storeErr(err)
					continue
				}
				res.Summary.Detections++
			}
		}
	}
	if res.SessionIDs == nil {
		res.SessionIDs = []string{}
	}
	return res, nil
}

// effectiveAction is what the engine's verdict means in enforce terms: the
// action itself in enforce mode, otherwise the action enforce mode would have
// taken for the matched rule levels (audit logs and shadow allows).
func effectiveAction(d *store.Detection) string {
	if d.Mode == "enforce" || d.Mode == "" {
		if d.Action == store.DetectionLog {
			return store.DetectionAllow
		}
		return d.Action
	}
	alerts := make([]engine.RuleResult, 0, len(d.Rules))
	for _, r := range d.Rules {
		alerts = append(alerts, engine.RuleResult{RuleID: r.ID, Severity: engine.AlertSeverity(r.Level)})
	}
	return string(evaluate.EnforceAction(alerts))
}

func ruleIDs(rules []models.Rule) []string {
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		out = append(out, r.ID)
	}
	sort.Strings(out)
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
