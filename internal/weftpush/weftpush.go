// Package weftpush sends AgentShield verdicts and user overrides to a Weft
// server's /api/v1/shield/ingest endpoint (docs/weft/DESIGN.md §5.2).
//
// Pushing never delays or changes a verdict: the request path only copies a
// small record into a bounded queue with a non-blocking send, and a record
// that does not fit is dropped and counted. A background worker sends the
// queue in batches, retrying transient failures with exponential backoff, and
// stops when its context is cancelled or Close is called.
//
// One bad record never takes others down with it: each record is bounded in
// size (descriptive fields are cut with a marker, rule ids, titles and tags
// are cleaned as Weft cleans them), a request is bounded in bytes, and when
// Weft rejects a request as invalid (400) or too large (413), the batch is
// split in halves until the offending records are isolated; only those are
// dropped (counted as rejected and logged), the rest are delivered.
package weftpush

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/evaluate"
	"github.com/agentshield-ai/agentshield/internal/models"
)

// IngestPath is the Weft route verdicts are posted to.
const IngestPath = "/api/v1/shield/ingest"

// Rule is one matched rule of a detection.
type Rule struct {
	ID    string   `json:"id"`
	Title string   `json:"title,omitempty"`
	Level string   `json:"level,omitempty"`
	Tags  []string `json:"tags,omitempty"`
}

// Detection is one verdict, in the shape /api/v1/shield/ingest accepts.
type Detection struct {
	EventID     string `json:"event_id"`
	SessionID   string `json:"session_id,omitempty"`
	ToolCallID  string `json:"tool_call_id,omitempty"`
	ToolName    string `json:"tool_name,omitempty"`
	AgentName   string `json:"agent_name,omitempty"`
	Action      string `json:"action"`
	Mode        string `json:"mode,omitempty"`
	Severity    string `json:"severity,omitempty"`
	Rules       []Rule `json:"rules"`
	Source      string `json:"source"`
	EventSource string `json:"event_source,omitempty"`
	Cached      bool   `json:"cached,omitempty"`
	Timestamp   string `json:"timestamp"`
}

// Override records that a user overrode a block or require_approval verdict.
type Override struct {
	EventID   string `json:"event_id,omitempty"`
	SessionID string `json:"session_id"`
	Timestamp string `json:"timestamp"`
}

// Payload is the request body of one push.
type Payload struct {
	ProjectID  string      `json:"project_id"`
	Detections []Detection `json:"detections,omitempty"`
	Overrides  []Override  `json:"overrides,omitempty"`
}

type item struct {
	det *Detection
	ovr *Override
}

// Stats are the pusher's counters.
type Stats struct {
	Queued  uint64 `json:"queued"`  // accepted into the queue
	Dropped uint64 `json:"dropped"` // dropped because the queue was full or closed
	Sent    uint64 `json:"sent"`    // records Weft acknowledged
	Failed  uint64 `json:"failed"`  // records given up on after retries or a client error (rejected included)
	// Rejected counts records Weft refused on their own as invalid or too
	// large (isolated from their batch, whose other records were sent).
	Rejected uint64 `json:"rejected"`
	// Truncated counts records whose descriptive fields were cut to fit
	// the record size limit.
	Truncated uint64 `json:"truncated"`
}

// Size limits of a push. Weft accepts request bodies of up to 64 MiB.
const (
	// DefaultMaxRecordBytes bounds one encoded detection.
	DefaultMaxRecordBytes = 256 << 10
	// DefaultMaxBatchBytes bounds one request body.
	DefaultMaxBatchBytes = 4 << 20
)

// Pusher queues verdicts and overrides and sends them to Weft.
type Pusher struct {
	url      string
	project  string
	apiKey   string
	sendAll  bool
	client   *http.Client
	queue    chan item
	log      *slog.Logger
	closing  chan struct{}
	done     chan struct{}
	closed   atomic.Bool
	started  atomic.Bool
	stopOnce sync.Once
	cancel   context.CancelFunc

	queued, dropped, sent, failed, rejected, truncated atomic.Uint64

	// BatchSize is the most records sent in one request.
	BatchSize int
	// MaxRecordBytes bounds one encoded record (DefaultMaxRecordBytes).
	MaxRecordBytes int
	// MaxBatchBytes bounds one request body (DefaultMaxBatchBytes); a
	// record is always sent, alone if need be.
	MaxBatchBytes int
	// MaxAttempts bounds tries per batch (transport errors, 408, 429, 5xx).
	MaxAttempts int
	// Backoff returns the wait before retry n (n starts at 1).
	Backoff func(n int) time.Duration
	// Now is the clock used for timestamps (tests).
	Now func() time.Time
}

// New builds a pusher from the engine's weft: config. The API key is read
// from the environment variable cfg.APIKeyEnv names.
func New(cfg config.WeftConfig) (*Pusher, error) {
	if !cfg.Enabled() {
		return nil, errors.New("weft push is not configured")
	}
	size := cfg.QueueSize
	if size <= 0 {
		size = 1000
	}
	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	p := &Pusher{
		url:            strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/") + IngestPath,
		project:        cfg.Project,
		sendAll:        cfg.Send == config.WeftSendAll,
		client:         &http.Client{Timeout: timeout},
		queue:          make(chan item, size),
		log:            slog.Default(),
		closing:        make(chan struct{}),
		done:           make(chan struct{}),
		BatchSize:      100,
		MaxRecordBytes: DefaultMaxRecordBytes,
		MaxBatchBytes:  DefaultMaxBatchBytes,
		MaxAttempts:    6,
		Backoff:        defaultBackoff,
		Now:            time.Now,
	}
	if cfg.APIKeyEnv != "" {
		p.apiKey = os.Getenv(cfg.APIKeyEnv)
		if p.apiKey == "" {
			p.log.Warn("weft push: the API key environment variable is empty", "env", cfg.APIKeyEnv)
		}
	}
	return p, nil
}

func defaultBackoff(n int) time.Duration {
	d := 250 * time.Millisecond << (n - 1)
	if d > 10*time.Second || d <= 0 {
		d = 10 * time.Second
	}
	return d
}

// Stats returns the current counters.
func (p *Pusher) Stats() Stats {
	return Stats{Queued: p.queued.Load(), Dropped: p.dropped.Load(), Sent: p.sent.Load(), Failed: p.failed.Load(),
		Rejected: p.rejected.Load(), Truncated: p.truncated.Load()}
}

// Verdict queues an evaluation verdict. Allow verdicts are skipped unless the
// policy is send: all. It never blocks.
func (p *Pusher) Verdict(req *models.EvaluationRequest, resp *evaluate.EvaluationResponse) {
	if p == nil || req == nil || resp == nil {
		return
	}
	if resp.Action == models.ActionAllow && !p.sendAll {
		return
	}
	// Replayed evaluations are never pushed (Weft rejects the replay
	// namespace on /api/v1/shield/ingest, which would fail the whole batch).
	if req.Replay || models.IsReplaySession(req.SessionID) || models.IsReplayEventID(req.EventID) {
		return
	}
	d := &Detection{
		EventID:     req.EventID,
		SessionID:   req.SessionID,
		ToolCallID:  req.ToolCallID,
		ToolName:    req.Tool,
		AgentName:   req.AgentName,
		Action:      string(resp.Action),
		Mode:        string(resp.EffectiveMode),
		Severity:    evaluate.MaxSeverity(resp.Alerts),
		Rules:       make([]Rule, 0, len(resp.Alerts)),
		Source:      "engine",
		EventSource: req.Source,
		Cached:      resp.Cached,
	}
	ts := resp.Timestamp
	if ts.IsZero() {
		ts = p.Now()
	}
	d.Timestamp = ts.UTC().Format(time.RFC3339Nano)
	for _, a := range resp.Alerts {
		d.Rules = append(d.Rules, Rule{ID: a.RuleID, Title: a.RuleName, Level: string(a.Severity), Tags: a.Tags})
	}
	p.enqueue(item{det: d})
}

// Override queues a user override of the verdict for eventID (which may be
// empty: the session's latest blocking verdict). It never blocks.
func (p *Pusher) Override(sessionID, eventID string) {
	if p == nil || sessionID == "" || models.IsReplaySession(sessionID) || models.IsReplayEventID(eventID) {
		return
	}
	p.enqueue(item{ovr: &Override{EventID: eventID, SessionID: sessionID, Timestamp: p.Now().UTC().Format(time.RFC3339Nano)}})
}

func (p *Pusher) enqueue(it item) {
	if p.closed.Load() {
		p.dropped.Add(1)
		return
	}
	select {
	case p.queue <- it:
		p.queued.Add(1)
	default:
		p.dropped.Add(1)
	}
}

// Start runs the sender until ctx is cancelled or Close is called.
func (p *Pusher) Start(ctx context.Context) {
	if !p.started.CompareAndSwap(false, true) {
		return
	}
	ctx, p.cancel = context.WithCancel(ctx)
	go p.loop(ctx)
}

// Close stops accepting records, sends what is queued within ctx's deadline,
// and stops the sender.
func (p *Pusher) Close(ctx context.Context) {
	p.stopOnce.Do(func() {
		p.closed.Store(true)
		close(p.closing)
	})
	if !p.started.Load() {
		return
	}
	select {
	case <-p.done:
	case <-ctx.Done():
		p.cancel()
		<-p.done
	}
}

func (p *Pusher) loop(ctx context.Context) {
	defer close(p.done)
	defer p.cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.closing:
			// Flush what is queued; ctx bounds it (Close cancels on its deadline).
			for {
				batch := p.collect(nil)
				if len(batch) == 0 {
					return
				}
				p.send(ctx, batch)
				if ctx.Err() != nil {
					return
				}
			}
		case it := <-p.queue:
			p.send(ctx, p.collect([]item{it}))
		}
	}
}

// collect adds queued records to batch without waiting.
func (p *Pusher) collect(batch []item) []item {
	max := p.BatchSize
	if max <= 0 {
		max = 100
	}
	for len(batch) < max {
		select {
		case it := <-p.queue:
			batch = append(batch, it)
		default:
			return batch
		}
	}
	return batch
}

// record is one queued item, encoded.
type record struct {
	override bool
	eventID  string
	raw      []byte
}

// send delivers a batch: records are bounded and encoded one by one, packed
// into requests of at most MaxBatchBytes, and each request is delivered on
// its own.
func (p *Pusher) send(ctx context.Context, batch []item) {
	recs := make([]record, 0, len(batch))
	for _, it := range batch {
		r, err := p.encode(it)
		if err != nil {
			p.failed.Add(1)
			p.log.Warn("weft push: dropping a record that cannot be encoded", "err", err)
			continue
		}
		recs = append(recs, r)
	}
	maxBytes := p.MaxBatchBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBatchBytes
	}
	for len(recs) > 0 {
		n, size := 0, 0
		for n < len(recs) && (n == 0 || size+len(recs[n].raw)+1 <= maxBytes) {
			size += len(recs[n].raw) + 1
			n++
		}
		p.deliver(ctx, recs[:n])
		recs = recs[n:]
	}
}

// encode bounds a record and encodes it without HTML escaping (which would
// grow '<', '>' and '&' sixfold).
func (p *Pusher) encode(it item) (record, error) {
	if it.ovr != nil {
		raw, err := marshal(it.ovr)
		return record{override: true, eventID: it.ovr.EventID, raw: raw}, err
	}
	d := *it.det
	cut := boundDetection(&d)
	raw, err := marshal(&d)
	if err != nil {
		return record{}, err
	}
	max := p.MaxRecordBytes
	if max <= 0 {
		max = DefaultMaxRecordBytes
	}
	if len(raw) > max {
		cut = true
		raw, err = shrinkDetection(&d, max)
		if err != nil {
			return record{}, err
		}
	}
	if cut {
		p.truncated.Add(1)
	}
	return record{eventID: d.EventID, raw: raw}, nil
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Field limits of a pushed detection (Weft's, docs/weft/API.md). Ids are
// never altered: the engine's input validation keeps them within Weft's
// limits.
const (
	maxNameBytes = 256
	maxRules     = 200
)

// boundDetection cuts a detection's descriptive fields to Weft's limits,
// marking cut values (models.Truncate), and cleans its rules as Weft does
// (models.ShortID, models.CleanTags), so Weft stores what it receives and
// the pushed copy of a verdict matches its OTLP copy. It reports whether
// anything was cut.
func boundDetection(d *Detection) bool {
	cut := false
	trunc := func(v string, max int) string {
		if len(v) > max {
			cut = true
			return models.Truncate(v, max)
		}
		return v
	}
	d.ToolName = trunc(d.ToolName, maxNameBytes)
	d.AgentName = trunc(d.AgentName, maxNameBytes)
	d.EventSource = trunc(d.EventSource, models.MaxEventSourceLength)
	if len(d.Rules) > maxRules {
		cut = true
		rules := append([]Rule(nil), d.Rules...)
		sort.SliceStable(rules, func(i, j int) bool { return levelRank(rules[i].Level) > levelRank(rules[j].Level) })
		d.Rules = rules[:maxRules]
	}
	rules := make([]Rule, len(d.Rules))
	for i, r := range d.Rules {
		r.ID, r.Title, r.Tags = models.CleanRule(r.ID, r.Title, r.Tags)
		rules[i] = r
	}
	d.Rules = rules
	return cut
}

// shrinkDetection fits an encoded detection into max bytes: it drops the
// rules' tags, then shortens their titles, then keeps fewer rules (the most
// severe first; the detection's severity is kept).
func shrinkDetection(d *Detection, max int) ([]byte, error) {
	for i := range d.Rules {
		d.Rules[i].Tags = nil
	}
	if raw, err := marshal(d); err != nil || len(raw) <= max {
		return raw, err
	}
	for i := range d.Rules {
		d.Rules[i].Title = models.Truncate(d.Rules[i].Title, 128)
	}
	sort.SliceStable(d.Rules, func(i, j int) bool { return levelRank(d.Rules[i].Level) > levelRank(d.Rules[j].Level) })
	for {
		raw, err := marshal(d)
		if err != nil || len(raw) <= max || len(d.Rules) == 0 {
			return raw, err
		}
		d.Rules = d.Rules[:len(d.Rules)/2]
	}
}

func levelRank(level string) int {
	switch level {
	case "low":
		return 1
	case "medium":
		return 2
	case "high":
		return 3
	case "critical":
		return 4
	}
	return 0
}

// body builds a request body from encoded records.
func (p *Pusher) body(recs []record) []byte {
	var buf bytes.Buffer
	proj, _ := marshal(p.project)
	buf.WriteString(`{"project_id":`)
	buf.Write(proj)
	for _, kind := range []bool{false, true} {
		first := true
		for _, r := range recs {
			if r.override != kind {
				continue
			}
			if first {
				if kind {
					buf.WriteString(`,"overrides":[`)
				} else {
					buf.WriteString(`,"detections":[`)
				}
				first = false
			} else {
				buf.WriteByte(',')
			}
			buf.Write(r.raw)
		}
		if !first {
			buf.WriteByte(']')
		}
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

// ingestResponse is the part of Weft's ingest response the pusher reads:
// records Weft rejected on their own (absent from older Weft versions,
// which reject the whole request instead).
type ingestResponse struct {
	Rejected int `json:"rejected"`
	Errors   []struct {
		Kind    string `json:"kind"`
		Index   int    `json:"index"`
		EventID string `json:"event_id"`
		Error   string `json:"error"`
	} `json:"errors"`
}

// deliver sends one request, retrying transient failures. When Weft rejects
// it as invalid (400) or too large (413), it is split in halves and each
// half delivered on its own, down to single records; a single record Weft
// rejects is dropped and counted as rejected.
func (p *Pusher) deliver(ctx context.Context, recs []record) {
	body := p.body(recs)
	attempts := p.MaxAttempts
	if attempts <= 0 {
		attempts = 1
	}
	var lastErr error
	for n := 1; n <= attempts; n++ {
		res, err := p.post(ctx, body)
		if err == nil {
			p.accepted(recs, res.body)
			return
		}
		lastErr = err
		if res.status == http.StatusBadRequest || res.status == http.StatusRequestEntityTooLarge {
			p.split(ctx, recs, res, err)
			return
		}
		if !res.retry || n == attempts {
			break
		}
		select {
		case <-ctx.Done():
			p.failed.Add(uint64(len(recs)))
			return
		case <-time.After(p.Backoff(n)):
		}
	}
	p.failed.Add(uint64(len(recs)))
	p.log.Warn("weft push failed", "records", len(recs), "err", lastErr)
}

// accepted counts a request Weft acknowledged, less the records it reports
// as rejected on their own.
func (p *Pusher) accepted(recs []record, body []byte) {
	var r ingestResponse
	if len(body) > 0 && json.Unmarshal(body, &r) == nil && r.Rejected > 0 {
		rej := min(r.Rejected, len(recs))
		p.rejected.Add(uint64(rej))
		p.failed.Add(uint64(rej))
		p.sent.Add(uint64(len(recs) - rej))
		for _, e := range r.Errors {
			p.log.Warn("weft push: weft rejected a record", "kind", e.Kind, "event_id", e.EventID, "err", e.Error)
		}
		return
	}
	p.sent.Add(uint64(len(recs)))
}

// split handles a 400 or 413: a single record is dropped as rejected, a
// batch Weft reports as wholly invalid is dropped the same way, and any
// other batch is delivered in halves.
func (p *Pusher) split(ctx context.Context, recs []record, res postResult, err error) {
	var r ingestResponse
	allInvalid := res.status == http.StatusBadRequest && json.Unmarshal(res.body, &r) == nil && r.Rejected == len(recs)
	if len(recs) == 1 || allInvalid {
		p.rejected.Add(uint64(len(recs)))
		p.failed.Add(uint64(len(recs)))
		for _, rec := range recs {
			p.log.Warn("weft push: weft rejected a record", "event_id", rec.eventID, "override", rec.override, "bytes", len(rec.raw), "err", err)
		}
		return
	}
	if ctx.Err() != nil {
		p.failed.Add(uint64(len(recs)))
		return
	}
	mid := len(recs) / 2
	p.deliver(ctx, recs[:mid])
	p.deliver(ctx, recs[mid:])
}

// postResult is the outcome of one request.
type postResult struct {
	status int    // HTTP status, 0 on a transport error
	retry  bool   // the failure is transient
	body   []byte // response body (bounded)
}

// post sends one request.
func (p *Pusher) post(ctx context.Context, body []byte) (postResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return postResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "agentshield-weftpush/1")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return postResult{retry: ctx.Err() == nil}, err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	res := postResult{status: resp.StatusCode, body: msg}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return res, nil
	case resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
		res.retry = true
	}
	return res, fmt.Errorf("weft returned %d: %s", resp.StatusCode, strings.TrimSpace(string(msg[:min(len(msg), 512)])))
}
