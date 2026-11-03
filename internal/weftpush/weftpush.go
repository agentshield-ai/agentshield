// Package weftpush sends AgentShield verdicts and user overrides to a Weft
// server's /api/v1/shield/ingest endpoint (docs/weft/DESIGN.md §5.2).
//
// Pushing never delays or changes a verdict: the request path only copies a
// small record into a bounded queue with a non-blocking send, and a record
// that does not fit is dropped and counted. A background worker sends the
// queue in batches, retrying transient failures with exponential backoff, and
// stops when its context is cancelled or Close is called.
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
	Failed  uint64 `json:"failed"`  // records given up on after retries or a client error
}

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

	queued, dropped, sent, failed atomic.Uint64

	// BatchSize is the most records sent in one request.
	BatchSize int
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
		url:         strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/") + IngestPath,
		project:     cfg.Project,
		sendAll:     cfg.Send == config.WeftSendAll,
		client:      &http.Client{Timeout: timeout},
		queue:       make(chan item, size),
		log:         slog.Default(),
		closing:     make(chan struct{}),
		done:        make(chan struct{}),
		BatchSize:   100,
		MaxAttempts: 6,
		Backoff:     defaultBackoff,
		Now:         time.Now,
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
	return Stats{Queued: p.queued.Load(), Dropped: p.dropped.Load(), Sent: p.sent.Load(), Failed: p.failed.Load()}
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
	if p == nil || sessionID == "" {
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

func (p *Pusher) send(ctx context.Context, batch []item) {
	payload := Payload{ProjectID: p.project}
	for _, it := range batch {
		if it.det != nil {
			payload.Detections = append(payload.Detections, *it.det)
		}
		if it.ovr != nil {
			payload.Overrides = append(payload.Overrides, *it.ovr)
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		p.failed.Add(uint64(len(batch)))
		return
	}
	attempts := p.MaxAttempts
	if attempts <= 0 {
		attempts = 1
	}
	var lastErr error
	for n := 1; n <= attempts; n++ {
		retry, err := p.post(ctx, body)
		if err == nil {
			p.sent.Add(uint64(len(batch)))
			return
		}
		lastErr = err
		if !retry || n == attempts {
			break
		}
		select {
		case <-ctx.Done():
			p.failed.Add(uint64(len(batch)))
			return
		case <-time.After(p.Backoff(n)):
		}
	}
	p.failed.Add(uint64(len(batch)))
	p.log.Warn("weft push failed", "records", len(batch), "err", lastErr)
}

// post sends one request; retry reports whether the failure is transient.
func (p *Pusher) post(ctx context.Context, body []byte) (retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "agentshield-weftpush/1")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return ctx.Err() == nil, err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return false, nil
	case resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
		return true, fmt.Errorf("weft returned %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	default:
		return false, fmt.Errorf("weft returned %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
}
