// Package weftpush sends AgentShield verdicts and user overrides to a Weft
// server's /api/v1/shield/ingest endpoint (docs/weft/DESIGN.md §5.2).
//
// Pushing never delays or changes a verdict: the request path only copies a
// small record into a bounded queue with a non-blocking send, and a record
// that does not fit is dropped (drop-newest) and counted. A background worker
// sends the queue in batches and stops when its context is cancelled or Close
// is called.
//
// Every record is accounted for (Stats): Accepted = Sent + Dropped + Failed +
// Rejected + UndeliveredAtShutdown + Pending, and every record that is not
// delivered is logged at warn level.
//
// Outages: a batch Weft cannot take (connection failure, 408, 429, 502, 503,
// 504) is kept and retried with capped exponential backoff until Weft has
// been failing for MaxRetry (weft.max_retry_sec, default 5 minutes); only then
// is it given up on (Failed). New records keep queueing meanwhile, up to the
// queue size.
//
// One bad record never takes others down with it: each record is bounded in
// size (descriptive fields are cut with a marker, rules are selected and
// cleaned as on the evaluate span, evaluate.WeftRules), a request is bounded
// in bytes, and when Weft rejects a request as invalid (400) or too large
// (413), or keeps answering it with another 5xx (500), the batch is split in
// halves until the offending records are isolated; only those are dropped
// (counted and logged), the rest are delivered.
//
// Poison or outage: a request Weft keeps answering with 500 is either
// holding a record Weft cannot store, or Weft cannot store anything. The
// pusher tells them apart by what Weft does with other requests: when Weft
// has stored another request since this one began failing, or stores a
// probe (the last detection it stored, sent again, which Weft keeps once per
// event id; or else the next queued record alone), the request's records are
// the problem: a batch is split, a lone record dropped (failed, logged). When
// the probe fails too, it is an outage: the request is retried in place
// until MaxRetry. With nothing to probe with yet, a batch is split (its
// halves probe each other) and a lone record is retried until a record to
// probe with arrives, so one poisoned record never holds up the queue for
// long.
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

// Stats are the pusher's counters. At any time Accepted = Sent + Dropped +
// Failed + Rejected + UndeliveredAtShutdown + Pending; after Close, Pending
// is 0.
type Stats struct {
	// Accepted counts the records offered to the pusher (verdicts its send
	// policy forwards, and overrides).
	Accepted uint64 `json:"accepted"`
	Queued   uint64 `json:"queued"`  // accepted into the queue
	Dropped  uint64 `json:"dropped"` // dropped because the queue was full (drop-newest) or the pusher closed
	Sent     uint64 `json:"sent"`    // records Weft stored
	// Failed counts records given up on: Weft still failing after
	// MaxRetry, a record Weft alone keeps answering with 5xx, or one that
	// cannot be encoded.
	Failed uint64 `json:"failed"`
	// Rejected counts records Weft refused on their own as invalid or too
	// large (isolated from their batch, whose other records were sent).
	Rejected uint64 `json:"rejected"`
	// UndeliveredAtShutdown counts records still queued or in flight when
	// the pusher stopped (its flush deadline passed or its context ended).
	UndeliveredAtShutdown uint64 `json:"undelivered_at_shutdown"`
	// Pending counts records queued or being delivered now.
	Pending uint64 `json:"pending"`
	// Truncated counts records whose descriptive fields were cut to fit
	// the record size limit.
	Truncated uint64 `json:"truncated"`
	// FailingSince is when Weft started failing (RFC 3339), empty while it
	// takes records.
	FailingSince string `json:"failing_since,omitempty"`
}

// Size limits of a push. Weft accepts request bodies of up to 64 MiB.
const (
	// DefaultMaxRecordBytes bounds one encoded detection. It is above the
	// largest detection the engine can produce (at most models.MaxRules
	// rules of bounded ids, titles and tags, JSON escaping included), so a
	// pushed verdict always carries the same rules and tags as its evaluate
	// span; the cut (shrinkDetection) only guards against other inputs.
	DefaultMaxRecordBytes = 512 << 10
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
	mu       sync.RWMutex // guards closed against enqueues
	closed   bool
	started  atomic.Bool
	stopOnce sync.Once
	cancel   context.CancelFunc

	accepted, queued, dropped, sent, failed, rejected, undelivered, truncated atomic.Uint64
	// failingSince is when Weft started failing (unix nanos, 0 while it
	// takes records); lastOK is when it last stored a request.
	failingSince, lastOK atomic.Int64
	lastDropLog          atomic.Int64
	// lastGood is the last detection Weft stored (the probe); held are
	// records taken from the queue as probes that Weft did not take, sent
	// next. Both belong to the sender goroutine.
	lastGood *record
	held     []record

	// BatchSize is the most records sent in one request.
	BatchSize int
	// MaxRecordBytes bounds one encoded record (DefaultMaxRecordBytes).
	MaxRecordBytes int
	// MaxBatchBytes bounds one request body (DefaultMaxBatchBytes); a
	// record is always sent, alone if need be.
	MaxBatchBytes int
	// MaxRetry is the longest outage ridden out: a batch is retried until
	// Weft has been failing for this long (weft.max_retry_sec).
	MaxRetry time.Duration
	// ServerErrorAttempts is how many 500 answers a request gets before it
	// is split to isolate a record Weft cannot store.
	ServerErrorAttempts int
	// Backoff returns the wait before retry n (n starts at 1).
	Backoff func(n int) time.Duration
	// Now is the clock used for timestamps and the outage clock (tests).
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
		size = config.DefaultWeftQueueSize
	}
	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	maxRetry := time.Duration(cfg.MaxRetrySec) * time.Second
	if maxRetry <= 0 {
		maxRetry = time.Duration(config.DefaultMaxRetrySec) * time.Second
	}
	p := &Pusher{
		url:                 strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/") + IngestPath,
		project:             cfg.Project,
		sendAll:             cfg.Send == config.WeftSendAll,
		client:              &http.Client{Timeout: timeout},
		queue:               make(chan item, size),
		log:                 slog.Default(),
		closing:             make(chan struct{}),
		done:                make(chan struct{}),
		BatchSize:           100,
		MaxRecordBytes:      DefaultMaxRecordBytes,
		MaxBatchBytes:       DefaultMaxBatchBytes,
		MaxRetry:            maxRetry,
		ServerErrorAttempts: 3,
		Backoff:             defaultBackoff,
		Now:                 time.Now,
	}
	if cfg.APIKeyEnv != "" {
		p.apiKey = os.Getenv(cfg.APIKeyEnv)
		if p.apiKey == "" {
			p.log.Warn("weft push: the API key environment variable is empty", "env", cfg.APIKeyEnv)
		}
	}
	return p, nil
}

// maxBackoff caps the wait between retries.
const maxBackoff = 10 * time.Second

func defaultBackoff(n int) time.Duration {
	if n > 16 {
		return maxBackoff
	}
	d := 250 * time.Millisecond << (n - 1)
	if d > maxBackoff || d <= 0 {
		d = maxBackoff
	}
	return d
}

// Stats returns the current counters.
func (p *Pusher) Stats() Stats {
	st := Stats{Sent: p.sent.Load(), Dropped: p.dropped.Load(), Failed: p.failed.Load(), Rejected: p.rejected.Load(),
		UndeliveredAtShutdown: p.undelivered.Load(), Truncated: p.truncated.Load(), Queued: p.queued.Load()}
	// Read Accepted last: every terminal count above was preceded by its
	// record's acceptance, so Pending never goes negative.
	st.Accepted = p.accepted.Load()
	if done := st.Sent + st.Dropped + st.Failed + st.Rejected + st.UndeliveredAtShutdown; st.Accepted > done {
		st.Pending = st.Accepted - done
	}
	if since := p.failingSince.Load(); since != 0 {
		st.FailingSince = time.Unix(0, since).UTC().Format(time.RFC3339)
	}
	return st
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
	// The same rules, in the same order, as the evaluate span carries.
	for _, r := range evaluate.WeftRules(resp.Alerts) {
		d.Rules = append(d.Rules, Rule{ID: r.ID, Title: r.Title, Level: r.Level, Tags: r.Tags})
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
	p.accepted.Add(1)
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		p.dropped.Add(1)
		p.warnDrop(it, "the pusher is stopped")
		return
	}
	select {
	case p.queue <- it:
		p.queued.Add(1)
	default:
		p.dropped.Add(1)
		p.warnDrop(it, "the queue is full")
	}
}

// warnDrop logs a dropped record at warn level, at most once per second
// (the dropped counter has them all).
func (p *Pusher) warnDrop(it item, why string) {
	now := p.Now().UnixNano()
	last := p.lastDropLog.Load()
	if now-last < int64(time.Second) || !p.lastDropLog.CompareAndSwap(last, now) {
		return
	}
	p.log.Warn("weft push: record dropped, "+why, "event_id", it.eventID(), "dropped_total", p.dropped.Load(), "queue_size", cap(p.queue))
}

func (it item) eventID() string {
	if it.det != nil {
		return it.det.EventID
	}
	if it.ovr != nil {
		return it.ovr.EventID
	}
	return ""
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
// and stops the sender. Records it could not deliver by then are counted
// (UndeliveredAtShutdown) and logged at warn level.
func (p *Pusher) Close(ctx context.Context) {
	p.stop()
	if p.started.Load() {
		select {
		case <-p.done:
		case <-ctx.Done():
			p.cancel()
			<-p.done
		}
	}
	// Nothing can be queued any more: count what is left.
	p.drainUndelivered()
	st := p.Stats()
	if st.UndeliveredAtShutdown > 0 {
		p.log.Warn("weft push: records not delivered at shutdown (lost)", "records", st.UndeliveredAtShutdown,
			"sent", st.Sent, "dropped", st.Dropped, "failed", st.Failed, "rejected", st.Rejected, "accepted", st.Accepted)
	}
}

// stop refuses further records.
func (p *Pusher) stop() {
	p.stopOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
		close(p.closing)
	})
}

// drainUndelivered counts the records left in the queue as undelivered.
func (p *Pusher) drainUndelivered() {
	for {
		select {
		case <-p.queue:
			p.undelivered.Add(1)
		default:
			return
		}
	}
}

func (p *Pusher) loop(ctx context.Context) {
	defer close(p.done)
	defer p.cancel()
	for {
		select {
		case <-ctx.Done():
			// Stopped without Close: refuse further records and count
			// what is queued.
			p.stop()
			p.drainUndelivered()
			p.undelivered.Add(uint64(len(p.held)))
			p.held = nil
			return
		case <-p.closing:
			// Flush what is queued; ctx bounds it (Close cancels on its
			// deadline), and send counts what it could not deliver.
			for {
				batch := p.collect(nil)
				if len(batch) == 0 && len(p.held) == 0 {
					return
				}
				p.send(ctx, batch)
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

// suspect is a record Weft keeps answering with a server error on its own.
type suspect struct {
	rec record
	// since is when its batch first got a server error: if Weft stores
	// another request after it, the record itself is the problem.
	since int64
}

// send delivers a batch, then any records held from a probe: records are
// bounded and encoded one by one, packed into requests of at most
// MaxBatchBytes, and each request is delivered on its own. Every record ends
// up counted: sent, rejected, failed or, when ctx ends first, undelivered.
func (p *Pusher) send(ctx context.Context, batch []item) {
	recs := make([]record, 0, len(batch))
	for _, it := range batch {
		r, ok := p.encodeOrFail(it)
		if ok {
			recs = append(recs, r)
		}
	}
	p.sendRecords(ctx, recs)
	for len(p.held) > 0 {
		held := p.held
		p.held = nil
		p.sendRecords(ctx, held)
	}
}

// encodeOrFail encodes a record; one that cannot be encoded is counted as
// failed and logged.
func (p *Pusher) encodeOrFail(it item) (record, bool) {
	r, err := p.encode(it)
	if err != nil {
		p.failed.Add(1)
		p.log.Warn("weft push: dropping a record that cannot be encoded", "event_id", it.eventID(), "err", err)
		return record{}, false
	}
	return r, true
}

// sendRecords packs encoded records into requests and delivers them.
func (p *Pusher) sendRecords(ctx context.Context, recs []record) {
	maxBytes := p.MaxBatchBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBatchBytes
	}
	var suspects []suspect
	for len(recs) > 0 {
		n, size := 0, 0
		for n < len(recs) && (n == 0 || size+len(recs[n].raw)+1 <= maxBytes) {
			size += len(recs[n].raw) + 1
			n++
		}
		suspects = append(suspects, p.deliver(ctx, recs[:n], p.serverErrorAttempts(), 0)...)
		recs = recs[n:]
	}
	for _, s := range suspects {
		p.deliverSuspect(ctx, s)
	}
}

func (p *Pusher) serverErrorAttempts() int {
	if p.ServerErrorAttempts > 0 {
		return p.ServerErrorAttempts
	}
	return 1
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
const maxNameBytes = 256

// boundDetection cuts a detection's descriptive fields to Weft's limits,
// marking cut values (models.Truncate), and cleans and selects its rules as
// the evaluate span does and Weft does (models.CleanRule,
// models.SelectRules; a no-op on the rules Verdict built), so Weft stores
// what it receives and the pushed copy of a verdict matches its OTLP copy.
// It reports whether anything was cut.
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
	rules := make([]models.Rule, len(d.Rules))
	for i, r := range d.Rules {
		id, title, tags := models.CleanRule(r.ID, r.Title, r.Tags)
		rules[i] = models.Rule{ID: id, Title: title, Level: r.Level, Tags: tags}
	}
	if len(rules) > models.MaxRules {
		cut = true
	}
	d.Rules = make([]Rule, 0, min(len(rules), models.MaxRules))
	for _, r := range models.SelectRules(rules) {
		d.Rules = append(d.Rules, Rule{ID: r.ID, Title: r.Title, Level: r.Level, Tags: r.Tags})
	}
	return cut
}

// shrinkDetection fits an encoded detection into max bytes: it drops the
// rules' tags, then shortens their titles, then keeps fewer rules (the most
// severe first; the detection's severity is kept). Detections the engine
// builds never need it (DefaultMaxRecordBytes).
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

func levelRank(level string) int { return models.LevelRank(level) }

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

// outageStatus reports the answers that mean Weft cannot take any request
// right now (rather than this one): retried until MaxRetry.
func outageStatus(status int) bool {
	switch status {
	case 0, http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// deliver sends recs as one request until Weft takes it, and returns the
// records Weft keeps answering with a server error on their own (suspects,
// settled by deliverSuspect once the rest of the batch is through).
//
//   - 2xx: sent, less the records Weft lists as rejected.
//   - 400, 413: split in halves down to the offending records (rejected).
//   - an outage (outageStatus): retried with capped exponential backoff
//     until Weft has been failing for MaxRetry, then failed.
//   - another 5xx (500): after serverErrors such answers, Weft is asked
//     whether it stores anything else (stored, probe). If it does, the
//     records are the problem: a batch is split in halves (each half gets
//     one try before it splits again, a lone record ServerErrorAttempts)
//     and a lone record is dropped. If the probe fails too, it is an
//     outage: retried as above, probing again on every further 500. With
//     nothing to probe with, a batch is split and a lone record is returned
//     as a suspect (settled by deliverSuspect once the rest of its batch is
//     through). since is when the request first got a 500 (0: not yet).
//
// When ctx ends the records are counted as undelivered.
func (p *Pusher) deliver(ctx context.Context, recs []record, serverErrors int, since int64) []suspect {
	body := p.body(recs)
	n5xx := 0
	for try := 1; ; try++ {
		res, err := p.post(ctx, body)
		if err == nil {
			p.up()
			p.ack(recs, res.body)
			return nil
		}
		if ctx.Err() != nil {
			p.undelivered.Add(uint64(len(recs)))
			return nil
		}
		switch {
		case res.status == http.StatusBadRequest || res.status == http.StatusRequestEntityTooLarge:
			return p.split(ctx, recs, res, err, serverErrors, since)
		case res.status >= 500 && !outageStatus(res.status):
			if since == 0 {
				since = p.Now().UnixNano()
			}
			if n5xx++; n5xx >= serverErrors {
				switch p.stored(ctx, since) {
				case weftOut:
					// An outage: retry, probing again on the next 500.
					serverErrors = 1
				case weftStores:
					if len(recs) == 1 {
						// Weft stores other records now, but it may only just
						// have recovered from an outage that also caused this
						// record's 500s: send it once more before dropping it.
						again, err2 := p.post(ctx, body)
						if err2 == nil {
							p.up()
							p.ack(recs, again.body)
							return nil
						}
						if ctx.Err() != nil {
							p.undelivered.Add(uint64(len(recs)))
							return nil
						}
						if again.status >= 500 && !outageStatus(again.status) {
							p.poison(recs[0], err2)
							return nil
						}
						err = err2
						break
					}
					return p.halves(ctx, recs, since)
				default:
					if len(recs) == 1 {
						return []suspect{{rec: recs[0], since: since}}
					}
					return p.halves(ctx, recs, since)
				}
			}
		}
		if !p.retryAfterFailure(ctx, try, recs, err) {
			return nil
		}
	}
}

// halves delivers recs in two halves after a batch kept getting 500: a half
// gets one try before it is split again, a lone record ServerErrorAttempts.
func (p *Pusher) halves(ctx context.Context, recs []record, since int64) []suspect {
	mid := len(recs) / 2
	tries := func(n int) int {
		if n == 1 {
			return p.serverErrorAttempts()
		}
		return 1
	}
	out := p.deliver(ctx, recs[:mid], tries(mid), since)
	return append(out, p.deliver(ctx, recs[mid:], tries(len(recs)-mid), since)...)
}

// poison drops a record Weft keeps answering with a server error while it
// stores other requests.
func (p *Pusher) poison(rec record, err error) {
	p.failed.Add(1)
	p.log.Warn("weft push: dropping a record Weft cannot store (server error on this record only; Weft stores other records)",
		"event_id", rec.eventID, "override", rec.override, "bytes", len(rec.raw), "failed_total", p.failed.Load(), "err", err)
}

// Answers of stored.
const (
	weftUnknown = iota // nothing to probe with
	weftStores
	weftOut
)

// stored reports whether Weft stores requests other than one that has been
// getting 500 since since: it has stored one since then, or it stores a
// probe now: the last detection it stored, sent again (Weft keeps one
// detection per event id, so this changes nothing), or else the next queued
// record alone (delivered if Weft takes it, otherwise held and sent after
// the current batch). weftUnknown when there is nothing to probe with.
func (p *Pusher) stored(ctx context.Context, since int64) int {
	if p.lastOK.Load() > since {
		return weftStores
	}
	if p.lastGood != nil {
		if _, err := p.post(ctx, p.body([]record{*p.lastGood})); err != nil {
			return weftOut
		}
		p.up()
		return weftStores
	}
	var it item
	select {
	case it = <-p.queue:
	default:
		return weftUnknown
	}
	rec, ok := p.encodeOrFail(it)
	if !ok {
		return weftUnknown
	}
	res, err := p.post(ctx, p.body([]record{rec}))
	switch {
	case err == nil:
		p.up()
		p.ack([]record{rec}, res.body)
		return weftStores
	case ctx.Err() != nil:
		p.undelivered.Add(1)
		return weftOut
	case res.status == http.StatusBadRequest || res.status == http.StatusRequestEntityTooLarge:
		// Weft refused the probe on its own: it says nothing about
		// storing.
		p.reject([]record{rec}, err)
		return weftUnknown
	}
	p.held = append(p.held, rec)
	return weftOut
}

// deliverSuspect settles a record Weft answered with a server error on its
// own, after the rest of its batch: dropped (failed) when Weft keeps
// answering it so although it stores other requests (stored), retried as an
// outage while it stores nothing, and retried while there is nothing to
// probe with (a record arriving in the queue becomes the probe).
func (p *Pusher) deliverSuspect(ctx context.Context, s suspect) {
	recs := []record{s.rec}
	body := p.body(recs)
	for try := 1; ; try++ {
		if ctx.Err() != nil {
			p.undelivered.Add(1)
			return
		}
		res, err := p.post(ctx, body)
		if err == nil {
			p.up()
			p.ack(recs, res.body)
			return
		}
		if ctx.Err() != nil {
			p.undelivered.Add(1)
			return
		}
		if res.status == http.StatusBadRequest || res.status == http.StatusRequestEntityTooLarge {
			p.reject(recs, err)
			return
		}
		if res.status >= 500 && !outageStatus(res.status) && p.stored(ctx, s.since) == weftStores {
			p.poison(s.rec, err)
			return
		}
		if !p.retryAfterFailure(ctx, try, recs, err) {
			return
		}
	}
}

// retryAfterFailure handles a failed try of recs: it gives them up (failed,
// logged) once Weft has been failing for MaxRetry, counts them undelivered
// if ctx ends, and otherwise waits for the next try and reports true.
func (p *Pusher) retryAfterFailure(ctx context.Context, try int, recs []record, err error) bool {
	now := p.Now()
	since := p.failingSince.Load()
	if since == 0 {
		p.failingSince.CompareAndSwap(0, now.UnixNano())
		since = p.failingSince.Load()
		p.log.Warn("weft push: Weft is failing; retrying", "records", len(recs), "max_retry", p.MaxRetry, "err", err)
	}
	if now.Sub(time.Unix(0, since)) >= p.MaxRetry {
		p.failed.Add(uint64(len(recs)))
		p.log.Warn("weft push: giving up on records, Weft has been failing longer than max_retry_sec", "records", len(recs),
			"first_event_id", recs[0].eventID, "failing_since", time.Unix(0, since).UTC().Format(time.RFC3339), "err", err)
		return false
	}
	select {
	case <-ctx.Done():
		p.undelivered.Add(uint64(len(recs)))
		return false
	case <-time.After(p.Backoff(try)):
		return true
	}
}

// up records that Weft stored a request.
func (p *Pusher) up() {
	p.lastOK.Store(p.Now().UnixNano())
	if since := p.failingSince.Swap(0); since != 0 {
		p.log.Info("weft push: Weft takes records again", "failing_for", p.Now().Sub(time.Unix(0, since)).Round(time.Millisecond))
	}
}

// ack counts a request Weft acknowledged, less the records it reports as
// rejected on their own.
func (p *Pusher) ack(recs []record, body []byte) {
	var r ingestResponse
	if len(body) == 0 || json.Unmarshal(body, &r) != nil {
		r = ingestResponse{}
	}
	if r.Rejected == 0 {
		// Weft stored every record: the last detection is the probe.
		for i := len(recs) - 1; i >= 0; i-- {
			if !recs[i].override {
				good := recs[i]
				p.lastGood = &good
				break
			}
		}
	}
	if r.Rejected > 0 {
		rej := min(r.Rejected, len(recs))
		p.rejected.Add(uint64(rej))
		p.sent.Add(uint64(len(recs) - rej))
		for _, e := range r.Errors {
			p.log.Warn("weft push: weft rejected a record", "kind", e.Kind, "event_id", e.EventID, "err", e.Error)
		}
		return
	}
	p.sent.Add(uint64(len(recs)))
}

// reject counts and logs records Weft refused on their own.
func (p *Pusher) reject(recs []record, err error) {
	p.rejected.Add(uint64(len(recs)))
	for _, rec := range recs {
		p.log.Warn("weft push: weft rejected a record", "event_id", rec.eventID, "override", rec.override, "bytes", len(rec.raw), "err", err)
	}
}

// split handles a 400 or 413: a single record is dropped as rejected, a
// batch Weft reports as wholly invalid is dropped the same way, and any
// other batch is delivered in halves.
func (p *Pusher) split(ctx context.Context, recs []record, res postResult, err error, serverErrors int, since int64) []suspect {
	var r ingestResponse
	allInvalid := res.status == http.StatusBadRequest && json.Unmarshal(res.body, &r) == nil && r.Rejected == len(recs)
	if len(recs) == 1 || allInvalid {
		p.reject(recs, err)
		return nil
	}
	mid := len(recs) / 2
	out := p.deliver(ctx, recs[:mid], serverErrors, since)
	return append(out, p.deliver(ctx, recs[mid:], serverErrors, since)...)
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
