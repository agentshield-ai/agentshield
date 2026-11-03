package telemetry

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Export batch bounds. A batch takes at most MaxExportBatchSize spans and at
// most MaxExportBatchBytes of them (by spanBytes, which never undercounts
// the encoded span); a span larger than that is exported alone. Either way a
// request stays far below Weft's 64 MiB limit: the engine's span limits
// bound any one span to about 2.5 MiB, and an evaluate span to under
// 640 KiB.
const (
	MaxExportBatchSize  = 256
	MaxExportBatchBytes = 8 << 20
	// batchOverheadBytes allows for what a request adds to its spans (the
	// resource, the instrumentation scopes and their framing).
	batchOverheadBytes = 4 << 10
)

// exportDelay is how long a queued span waits for its batch to fill before
// it is exported anyway.
const exportDelay = time.Second

// spanQueue is the engine's span processor, in place of the SDK's
// BatchSpanProcessor, whose queue (2048 spans) drops spans silently once
// full and whose exports run one at a time:
//
//   - OnEnd never blocks: a sampled span is appended to a queue bounded in
//     spans (telemetry.queue_size) and bytes (telemetry.queue_max_mb). When
//     either bound is reached the new span is dropped (drop-newest), counted
//     (Dropped) and logged at warn level, at most once a second.
//   - Up to telemetry.export_concurrency workers take batches (at most
//     MaxExportBatchSize spans and MaxExportBatchBytes bytes) and export
//     them concurrently; a worker waits up to exportDelay for a batch to
//     fill. spanExporter retries each export through an outage of up to
//     telemetry.max_retry_sec and isolates spans the collector refuses.
//   - Every span is counted once: Ended = Exported + Failed + Dropped +
//     UndeliveredAtShutdown + Pending, Pending being the spans queued or
//     being exported (ExportStats).
//   - Shutdown exports what is queued within its deadline; when the
//     deadline passes, in-flight exports are stopped and every span not
//     exported is counted as undelivered at shutdown.
type spanQueue struct {
	exp      *spanExporter
	maxSpans int
	maxBytes int64
	workers  int
	delay    time.Duration
	// batchSpans and batchBytes bound one export (MaxExportBatchSize,
	// MaxExportBatchBytes; lower in tests).
	batchSpans int
	batchBytes int64

	// ctx is the context of every export; cancel stops them (the shutdown
	// deadline passed).
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu sync.Mutex
	// wake is closed (and replaced) to wake every worker and flush waiter.
	wake     chan struct{}
	queue    []queuedSpan
	head     int
	qBytes   int64
	inflight int
	flushing int
	// closing: Shutdown began (export without delay, then stop);
	// finished: the workers have stopped.
	closing, finished                             bool
	ended, exported, failed, dropped, undelivered uint64
	lastDropLog                                   time.Time
}

type queuedSpan struct {
	span  sdktrace.ReadOnlySpan
	bytes int64
	at    time.Time
}

// outcome is what became of exported spans.
type outcome int

const (
	outcomeExported outcome = iota
	outcomeFailed
	outcomeUndelivered
)

func newSpanQueue(exp *spanExporter, maxSpans int, maxBytes int64, workers int) *spanQueue {
	q := &spanQueue{exp: exp, maxSpans: maxSpans, maxBytes: maxBytes, workers: max(1, workers), delay: exportDelay,
		batchSpans: MaxExportBatchSize, batchBytes: MaxExportBatchBytes, wake: make(chan struct{})}
	exp.queue = q
	q.ctx, q.cancel = context.WithCancel(context.Background())
	return q
}

// start runs the export workers.
func (q *spanQueue) start() {
	for i := 0; i < q.workers; i++ {
		q.wg.Add(1)
		go q.worker()
	}
}

// broadcast wakes every waiter; q.mu must be held.
func (q *spanQueue) broadcast() {
	close(q.wake)
	q.wake = make(chan struct{})
}

func (q *spanQueue) queued() int { return len(q.queue) - q.head }

// OnStart implements sdktrace.SpanProcessor.
func (q *spanQueue) OnStart(context.Context, sdktrace.ReadWriteSpan) {}

// OnEnd queues a sampled span without ever blocking.
func (q *spanQueue) OnEnd(s sdktrace.ReadOnlySpan) {
	if !s.SpanContext().IsSampled() {
		return
	}
	size := int64(spanBytes(s))
	q.mu.Lock()
	q.ended++
	n := q.queued()
	if q.finished || n >= q.maxSpans || q.qBytes+size > q.maxBytes {
		why := "the export queue is full"
		if q.finished {
			why = "the exporter is shut down"
			q.undelivered++
		} else {
			q.dropped++
		}
		total, qb := q.dropped, q.qBytes
		now := time.Now()
		logIt := now.Sub(q.lastDropLog) >= time.Second
		if logIt {
			q.lastDropLog = now
		}
		q.mu.Unlock()
		if logIt {
			slog.Warn("OTel export: span dropped, "+why, "dropped_total", total, "queued_spans", n, "queued_bytes", qb,
				"span_bytes", size, "queue_size", q.maxSpans, "queue_max_bytes", q.maxBytes)
		}
		return
	}
	if q.head > 0 && (q.head == len(q.queue) || q.head >= 4096 && q.head > len(q.queue)/2) {
		k := copy(q.queue, q.queue[q.head:])
		clear(q.queue[k:])
		q.queue, q.head = q.queue[:k], 0
	}
	q.queue = append(q.queue, queuedSpan{span: s, bytes: size, at: s.EndTime()})
	q.qBytes += size
	n++
	// Wake the workers when there is something to wait for (the first
	// span) or a batch is full.
	if n == 1 || n%q.batchSpans == 0 || (q.qBytes >= q.batchBytes && q.qBytes-size < q.batchBytes) {
		q.broadcast()
	}
	q.mu.Unlock()
}

// worker exports batches until the queue is shut down.
func (q *spanQueue) worker() {
	defer q.wg.Done()
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		q.mu.Lock()
		batch, wait, exit := q.take(time.Now())
		wake := q.wake
		q.mu.Unlock()
		switch {
		case exit:
			return
		case batch != nil:
			q.exp.exportBatch(q.ctx, batch)
			continue
		}
		var tc <-chan time.Time
		if wait > 0 {
			timer.Reset(wait)
			tc = timer.C
		}
		select {
		case <-wake:
		case <-tc:
		case <-q.ctx.Done():
		}
		timer.Stop()
	}
}

// take removes the next batch from the queue when one is due: it is full,
// its oldest span has waited exportDelay, or the queue is being flushed.
// Otherwise it reports how long to wait (0: until woken), or that the
// worker should stop. q.mu must be held.
func (q *spanQueue) take(now time.Time) (batch []sdktrace.ReadOnlySpan, wait time.Duration, exit bool) {
	if q.ctx.Err() != nil {
		return nil, 0, true
	}
	n := q.queued()
	if n == 0 {
		return nil, 0, q.closing
	}
	if age := now.Sub(q.queue[q.head].at); !q.closing && q.flushing == 0 && n < q.batchSpans && q.qBytes < q.batchBytes && age < q.delay {
		return nil, q.delay - age, false
	}
	var size int64
	for q.queued() > 0 && len(batch) < q.batchSpans {
		next := q.queue[q.head]
		if len(batch) > 0 && size+next.bytes > q.batchBytes {
			break
		}
		batch = append(batch, next.span)
		size += next.bytes
		q.queue[q.head] = queuedSpan{}
		q.head++
	}
	q.qBytes -= size
	q.inflight += len(batch)
	if q.queued() > 0 {
		// Spans are left: idle workers (waiting without a timer) look at
		// when they are due.
		q.broadcast()
	}
	return batch, 0, false
}

// settle counts n exported spans' outcome.
func (q *spanQueue) settle(o outcome, n int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.inflight -= n
	switch o {
	case outcomeExported:
		q.exported += uint64(n)
	case outcomeFailed:
		q.failed += uint64(n)
	default:
		q.undelivered += uint64(n)
	}
	if q.inflight == 0 && q.queued() == 0 {
		q.broadcast() // flush waiters
	}
}

// ForceFlush exports everything queued and waits until nothing is pending
// or ctx ends.
func (q *spanQueue) ForceFlush(ctx context.Context) error {
	q.mu.Lock()
	q.flushing++
	q.broadcast()
	defer func() {
		q.flushing--
		q.mu.Unlock()
	}()
	for q.queued()+q.inflight > 0 && !q.finished {
		wake := q.wake
		q.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			q.mu.Lock()
			return ctx.Err()
		}
		q.mu.Lock()
	}
	return nil
}

// Shutdown exports what is queued within ctx's deadline, then stops the
// workers. When the deadline passes, the exports still running are
// stopped; their spans and those still queued are counted as undelivered at
// shutdown.
func (q *spanQueue) Shutdown(ctx context.Context) error {
	q.mu.Lock()
	if q.closing {
		q.mu.Unlock()
		return nil
	}
	q.closing = true
	q.broadcast()
	q.mu.Unlock()
	done := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(done)
	}()
	var err error
	select {
	case <-done:
	case <-ctx.Done():
		err = ctx.Err()
		q.exp.stopping.Store(true)
		q.cancel()
		// Interrupt requests in flight (and the exporter's own retries).
		_ = q.exp.SpanExporter.Shutdown(context.Background())
		<-done
	}
	q.cancel()
	q.mu.Lock()
	q.finished = true
	q.undelivered += uint64(q.queued())
	clear(q.queue)
	q.queue, q.head, q.qBytes = nil, 0, 0
	q.broadcast()
	q.mu.Unlock()
	if err == nil {
		err = q.exp.SpanExporter.Shutdown(ctx)
	}
	return err
}

// snapshot returns the counters, consistent with one another.
func (q *spanQueue) snapshot() ExportStats {
	q.mu.Lock()
	defer q.mu.Unlock()
	return ExportStats{Ended: q.ended, Exported: q.exported, Failed: q.failed, Dropped: q.dropped,
		UndeliveredAtShutdown: q.undelivered, Pending: uint64(q.queued() + q.inflight), Errors: otelErrors.Load()}
}

// Allowances of spanBytes for the OTLP protobuf framing: a span's fixed
// fields (ids, times, kind, status, flags), an attribute's key and value
// framing (lengths up to 2 MiB), and an event's time and framing.
const (
	spanFixedBytes  = 192
	attrFramingSize = 16
	eventFixedBytes = 32
)

// spanBytes bounds the encoded size of a span from above (cheaply: it only
// adds up lengths).
func spanBytes(s sdktrace.ReadOnlySpan) int {
	n := spanFixedBytes + len(s.Name()) + len(s.Status().Description) + len(s.SpanContext().TraceState().String())
	for _, kv := range s.Attributes() {
		n += attrBytes(kv)
	}
	for _, ev := range s.Events() {
		n += eventFixedBytes + len(ev.Name)
		for _, kv := range ev.Attributes {
			n += attrBytes(kv)
		}
	}
	for _, l := range s.Links() {
		n += spanFixedBytes + len(l.SpanContext.TraceState().String())
		for _, kv := range l.Attributes {
			n += attrBytes(kv)
		}
	}
	return n
}

func attrBytes(kv attribute.KeyValue) int {
	n := len(kv.Key) + attrFramingSize
	switch kv.Value.Type() {
	case attribute.STRING:
		n += len(kv.Value.AsString())
	case attribute.STRINGSLICE:
		for _, v := range kv.Value.AsStringSlice() {
			n += len(v) + attrFramingSize
		}
	case attribute.BOOLSLICE:
		n += attrFramingSize * len(kv.Value.AsBoolSlice())
	case attribute.INT64SLICE:
		n += attrFramingSize * len(kv.Value.AsInt64Slice())
	case attribute.FLOAT64SLICE:
		n += attrFramingSize * len(kv.Value.AsFloat64Slice())
	default:
		n += attrFramingSize
	}
	return n
}
