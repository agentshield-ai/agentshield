// Package telemetry provides OpenTelemetry integration for AgentShield.
package telemetry

import (
	"context"
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

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/models"
)

// ShutdownFunc is called to flush and shut down the telemetry provider.
type ShutdownFunc func(ctx context.Context) error

// Init initialises the OpenTelemetry TracerProvider. When telemetry is disabled
// it returns a no-op provider. The returned ShutdownFunc must be called on
// process exit to flush pending spans.
//
// Spans are queued and exported by spanQueue (bounded in spans and bytes,
// never blocking the verdict path, exporting up to export_concurrency
// batches at once) through spanExporter, which retries failed exports for
// cfg.MaxRetrySec: connection failures and every 5xx answer count as
// transient (retryTransport), so a Weft outage of that length loses nothing
// while the queue holds. Every span the engine ends is accounted for
// (Stats): exported, given up on (failed), dropped because the queue was
// full, still pending (queued or being exported), or not exported by the
// shutdown deadline; every loss is logged at warn level.
func Init(ctx context.Context, cfg *config.TelemetryConfig, version string) (trace.TracerProvider, ShutdownFunc, error) {
	if !cfg.Enabled {
		noop := tracenoop.NewTracerProvider()
		return noop, func(_ context.Context) error { return nil }, nil
	}

	maxRetry := time.Duration(cfg.MaxRetrySec) * time.Second
	if maxRetry <= 0 {
		maxRetry = time.Duration(config.DefaultMaxRetrySec) * time.Second
	}
	queueSize := cfg.QueueSize
	if queueSize <= 0 {
		queueSize = config.DefaultTelemetryQueueSize
	}
	queueMB := cfg.QueueMaxMB
	if queueMB <= 0 {
		queueMB = config.DefaultTelemetryQueueMaxMB
	}
	workers := cfg.ExportConcurrency
	if workers <= 0 {
		workers = config.DefaultTelemetryExportConcurrency
	}
	hostPort, path := splitEndpoint(cfg.Endpoint)
	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(hostPort),
		otlptracehttp.WithHTTPClient(&http.Client{Timeout: exportRequestTimeout, Transport: retryTransport{base: http.DefaultTransport}}),
		otlptracehttp.WithRetry(otlptracehttp.RetryConfig{
			Enabled:         true,
			InitialInterval: time.Second,
			MaxInterval:     10 * time.Second,
			MaxElapsedTime:  maxRetry,
		}),
	}
	if path != "" {
		opts = append(opts, otlptracehttp.WithURLPath(path))
	}
	if cfg.Insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}
	if h := exportHeaders(cfg); len(h) > 0 {
		opts = append(opts, otlptracehttp.WithHeaders(h))
	}

	exporter, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("creating OTLP trace exporter: %w", err)
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(version),
		),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("creating OTel resource: %w", err)
	}

	sampler := sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRate))

	installErrorHandler()
	exp := &spanExporter{SpanExporter: exporter, maxRetry: maxRetry, backoff: exportBackoff}
	q := newSpanQueue(exp, queueSize, int64(queueMB)<<20, workers)
	q.start()
	activeQueue.Store(q)
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(q),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
		sdktrace.WithRawSpanLimits(SpanLimits()),
	)
	otel.SetTracerProvider(tp)

	shutdown := func(ctx context.Context) error {
		// Export what is queued within ctx's deadline; when it passes,
		// the exports still running are stopped and what is left is
		// counted as undelivered rather than failed.
		err := tp.Shutdown(ctx)
		if s := q.snapshot(); s.UndeliveredAtShutdown > 0 {
			slog.Warn("OTel export: spans not exported at shutdown (lost)", "spans", s.UndeliveredAtShutdown,
				"exported", s.Exported, "failed", s.Failed, "dropped", s.Dropped, "ended", s.Ended)
		}
		return err
	}
	return tp, shutdown, nil
}

// exportRequestTimeout bounds one export request (a retry starts a new one).
const exportRequestTimeout = 10 * time.Second

// Export size bounds. One evaluate span must not be able to make an export
// request too large for the collector (Weft accepts 64 MiB per request), or
// the whole batch would be lost with it. The engine bounds what it puts on a
// span (ids are validated to 256 bytes, the event source cut to 128, a
// verdict carries at most models.MaxRules rules with bounded ids, titles and
// tags), and these limits bound the rest; the evaluate package's tests
// compute the worst-case span from them and check it against a real export.
const (
	// MaxAttributeValueLength cuts every string attribute value (and each
	// string of a string slice) of a span or span event. Nothing Weft reads
	// is longer (ids 256 bytes, rule titles 1024, tags 256 each).
	MaxAttributeValueLength = 2048
	// MaxSpanAttributes is the most attributes a span keeps (the evaluate
	// span sets about 30).
	MaxSpanAttributes = 64
	// MaxSpanEvents is the most events a span keeps: two per rule a verdict
	// carries (the agentshield.rule.matched event and the legacy
	// rule.matched one) and a few more (cache.hit), so no rule event is
	// ever evicted.
	MaxSpanEvents = 2*models.MaxRules + 4
	// MaxEventAttributes is the most attributes a span event keeps (rule
	// events have four).
	MaxEventAttributes = 4
	// MaxSpanLinks and MaxLinkAttributes bound links: the engine adds none.
	MaxSpanLinks      = 0
	MaxLinkAttributes = 0
)

// SpanLimits are the engine's span limits (the constants above), or lower
// ones the OTEL_* span limit variables set.
func SpanLimits() sdktrace.SpanLimits {
	env := sdktrace.NewSpanLimits()
	lower := func(name string, envValue, max int) int {
		if _, set := os.LookupEnv(name); set && envValue >= 0 && envValue < max {
			return envValue
		}
		return max
	}
	return sdktrace.SpanLimits{
		AttributeValueLengthLimit:   lower("OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT", env.AttributeValueLengthLimit, MaxAttributeValueLength),
		AttributeCountLimit:         lower("OTEL_SPAN_ATTRIBUTE_COUNT_LIMIT", env.AttributeCountLimit, MaxSpanAttributes),
		EventCountLimit:             lower("OTEL_SPAN_EVENT_COUNT_LIMIT", env.EventCountLimit, MaxSpanEvents),
		AttributePerEventCountLimit: lower("OTEL_EVENT_ATTRIBUTE_COUNT_LIMIT", env.AttributePerEventCountLimit, MaxEventAttributes),
		LinkCountLimit:              lower("OTEL_SPAN_LINK_COUNT_LIMIT", env.LinkCountLimit, MaxSpanLinks),
		AttributePerLinkCountLimit:  lower("OTEL_LINK_ATTRIBUTE_COUNT_LIMIT", env.AttributePerLinkCountLimit, MaxLinkAttributes),
	}
}

// retryTransport sorts export failures for the exporter and for
// spanExporter. A connection failure is reported as 503, which the OTLP
// exporter retries (an outage), as it does 429, 502, 503 and 504; a
// cancelled request (shutdown) is passed through. Any other answer the
// exporter does not retry is marked in its status text (markServerError,
// markClientError), which the exporter's error carries, so spanExporter
// can isolate the spans a collector refuses instead of losing the batch.
type retryTransport struct{ base http.RoundTripper }

// Marks retryTransport adds to the status of answers the exporter does not
// retry.
const (
	markServerError = "[agentshield: server error]"
	markClientError = "[agentshield: refused]"
)

func (t retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		if req.Context().Err() != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Status: "503 Service Unavailable (" + err.Error() + ")",
			Header: http.Header{}, Body: io.NopCloser(strings.NewReader(err.Error())), Request: req, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1}, nil
	}
	switch c := resp.StatusCode; {
	case c < 400 || outageStatus(c):
	case c >= 500:
		resp.Status += " " + markServerError
	default:
		resp.Status += " " + markClientError
	}
	return resp, nil
}

// outageStatus reports the answers the OTLP exporter retries itself.
func outageStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// ExportStats count the spans the engine ended and what became of them. At
// any time Ended = Exported + Failed + Dropped + UndeliveredAtShutdown +
// Pending.
type ExportStats struct {
	// Ended counts sampled spans the engine ended.
	Ended uint64 `json:"ended"`
	// Exported counts spans the collector acknowledged.
	Exported uint64 `json:"exported"`
	// Failed counts spans given up on (lost): an export that still failed
	// after max_retry_sec, or a span the collector refuses.
	Failed uint64 `json:"failed"`
	// Dropped counts spans dropped because the export queue was full
	// (queue_size spans or queue_max_mb bytes; drop-newest).
	Dropped uint64 `json:"dropped"`
	// Pending counts spans queued or being exported now.
	Pending uint64 `json:"pending"`
	// UndeliveredAtShutdown counts spans not exported by the shutdown
	// deadline (lost).
	UndeliveredAtShutdown uint64 `json:"undelivered_at_shutdown"`
	// Errors counts OpenTelemetry errors reported to the error handler
	// (failed exports among them).
	Errors uint64 `json:"errors"`
}

var (
	activeQueue atomic.Pointer[spanQueue]
	otelErrors  atomic.Uint64
	handlerOnce sync.Once
)

// Stats returns the span export counters of the tracer provider Init last
// built, or nil when telemetry is disabled.
func Stats() *ExportStats {
	q := activeQueue.Load()
	if q == nil {
		return nil
	}
	s := q.snapshot()
	return &s
}

// spanExporter exports a batch, reports what became of every span
// (spanQueue.settle), logs every lost span at warn level, and keeps one span
// from taking its batch down with it: an export the collector refuses (4xx),
// or keeps answering with another 5xx (500), is split in halves until the
// offending spans are isolated; only they are dropped. A span that alone
// keeps getting 500 is dropped only once the collector has taken other spans
// since (from this export or a concurrent one); while it takes none, that is
// an outage, retried until maxRetry. (Connection failures and
// 429/502/503/504 are retried by the exporter itself.) It is safe for
// concurrent use.
type spanExporter struct {
	sdktrace.SpanExporter
	queue *spanQueue
	// stopping is set when the shutdown deadline passed: an export cut
	// short then is counted as undelivered at shutdown, not as failed.
	stopping atomic.Bool
	// maxRetry bounds an outage of server errors; backoff is the wait
	// before retry n.
	maxRetry time.Duration
	backoff  func(n int) time.Duration
	// lastOK is when an export last succeeded, failingSince when server
	// errors began (unix nanos, 0 while exports succeed).
	lastOK, failingSince atomic.Int64
}

// loggedError marks an export error spanExporter already logged.
type loggedError struct{ error }

func (e loggedError) Unwrap() error { return e.error }

// serverErrorTries is how many 500 answers an export gets before it is
// split.
const serverErrorTries = 3

// exportBatch exports spans and settles every one of them.
func (e *spanExporter) exportBatch(ctx context.Context, spans []sdktrace.ReadOnlySpan) {
	for _, s := range e.export(ctx, spans, serverErrorTries, 0) {
		e.settle(ctx, s)
	}
}

// suspectSpan is a span the collector answered with a server error on its
// own; since is when its batch first got one.
type suspectSpan struct {
	span  sdktrace.ReadOnlySpan
	since int64
}

// export delivers spans, splitting them as described on spanExporter, and
// returns the spans that alone got server errors (settled once the rest of
// the batch is through); every other span is settled.
func (e *spanExporter) export(ctx context.Context, spans []sdktrace.ReadOnlySpan, serverTries int, since int64) []suspectSpan {
	for try := 1; ; try++ {
		err := e.SpanExporter.ExportSpans(ctx, spans)
		if err == nil {
			e.ok(len(spans))
			return nil
		}
		refused, serverErr := classify(err)
		if e.stopping.Load() || ctx.Err() != nil || (!refused && !serverErr) {
			// Shutdown, or the exporter's own retries are exhausted.
			e.lose(spans, err)
			return nil
		}
		if serverErr && since == 0 {
			since = time.Now().UnixNano()
		}
		if refused || try >= serverTries {
			if len(spans) == 1 {
				if refused {
					e.lose(spans, err)
					return nil
				}
				return []suspectSpan{{span: spans[0], since: since}}
			}
			mid := len(spans) / 2
			return append(e.export(ctx, spans[:mid], 1, since), e.export(ctx, spans[mid:], 1, since)...)
		}
		if !e.wait(ctx, try) {
			e.lose(spans, err)
			return nil
		}
	}
}

// settle delivers a suspect span: dropped when the collector keeps
// answering it with a server error although it took other spans since its
// batch began failing, retried as an outage (until maxRetry) while it takes
// nothing.
func (e *spanExporter) settle(ctx context.Context, s suspectSpan) {
	spans := []sdktrace.ReadOnlySpan{s.span}
	for try := 1; ; try++ {
		err := e.SpanExporter.ExportSpans(ctx, spans)
		if err == nil {
			e.ok(1)
			return
		}
		refused, serverErr := classify(err)
		if e.stopping.Load() || ctx.Err() != nil || refused || !serverErr || e.lastOK.Load() > s.since {
			e.lose(spans, err)
			return
		}
		e.failingSince.CompareAndSwap(0, time.Now().UnixNano())
		if time.Since(time.Unix(0, e.failingSince.Load())) >= e.maxRetry || !e.wait(ctx, try) {
			e.lose(spans, err)
			return
		}
	}
}

// classify reports whether an export error is the collector refusing the
// request (4xx) or answering it with a server error the exporter does not
// retry (retryTransport's marks).
func classify(err error) (refused, serverErr bool) {
	msg := err.Error()
	return strings.Contains(msg, markClientError), strings.Contains(msg, markServerError)
}

func (e *spanExporter) ok(n int) {
	e.lastOK.Store(time.Now().UnixNano())
	e.failingSince.Store(0)
	e.queue.settle(outcomeExported, n)
}

// wait sleeps before retry n; false when ctx ended first.
func (e *spanExporter) wait(ctx context.Context, n int) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(e.backoff(n)):
		return true
	}
}

// lose counts and logs spans given up on (at shutdown they are counted as
// undelivered instead), and reports the error to the OpenTelemetry error
// handler (counted in ExportStats.Errors).
func (e *spanExporter) lose(spans []sdktrace.ReadOnlySpan, err error) {
	if e.stopping.Load() {
		e.queue.settle(outcomeUndelivered, len(spans))
		return
	}
	e.queue.settle(outcomeFailed, len(spans))
	slog.Warn("OTel export failed; spans lost", "spans", len(spans), "failed_total", e.queue.snapshot().Failed, "err", err)
	otel.Handle(loggedError{err})
}

func exportBackoff(n int) time.Duration {
	if n > 4 {
		return 10 * time.Second
	}
	return min(10*time.Second, time.Second<<(n-1))
}

// installErrorHandler logs OpenTelemetry errors (failed exports among them)
// at warn level and counts them (ExportStats.Errors). The SDK's default
// handler logs them at a level the engine's logs do not show.
func installErrorHandler() {
	handlerOnce.Do(func() {
		otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
			otelErrors.Add(1)
			var logged loggedError
			if errors.As(err, &logged) {
				return
			}
			slog.Warn("OpenTelemetry error", "err", err)
		}))
	})
}

// InitMeter creates an OTel MeterProvider with an OTLP/HTTP metric exporter.
// Returns nil when telemetry is disabled.
func InitMeter(ctx context.Context, cfg *config.TelemetryConfig) (*sdkmetric.MeterProvider, error) {
	if !cfg.Enabled || cfg.DisableMetrics {
		return nil, nil
	}

	hostPort, _ := splitEndpoint(cfg.Endpoint)
	opts := []otlpmetrichttp.Option{
		otlpmetrichttp.WithEndpoint(hostPort),
	}
	if cfg.Insecure {
		opts = append(opts, otlpmetrichttp.WithInsecure())
	}
	if h := exportHeaders(cfg); len(h) > 0 {
		opts = append(opts, otlpmetrichttp.WithHeaders(h))
	}

	exporter, err := otlpmetrichttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("creating OTLP metric exporter: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter)),
	)
	return mp, nil
}

// splitEndpoint returns the host:port of an endpoint and its URL path, if it
// has one other than "/" (e.g. Weft's /otel/v1/traces).
func splitEndpoint(endpoint string) (hostPort, path string) {
	rest := stripScheme(endpoint)
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		hostPort, path = rest[:i], rest[i:]
		if path == "/" {
			path = ""
		}
		return hostPort, path
	}
	return rest, ""
}

// exportHeaders are the configured headers plus the bearer token from
// api_key_env. Nil when neither is set, so OTEL_EXPORTER_OTLP_HEADERS still
// applies.
func exportHeaders(cfg *config.TelemetryConfig) map[string]string {
	var h map[string]string
	if len(cfg.Headers) > 0 || cfg.APIKeyEnv != "" {
		h = make(map[string]string, len(cfg.Headers)+1)
	}
	for k, v := range cfg.Headers {
		h[k] = v
	}
	if cfg.APIKeyEnv != "" {
		if key := os.Getenv(cfg.APIKeyEnv); key != "" {
			h["Authorization"] = "Bearer " + key
		}
	}
	return h
}

// stripScheme removes http:// or https:// prefix since the OTel HTTP exporter
// takes a bare host:port and uses WithInsecure() to control TLS.
func stripScheme(endpoint string) string {
	for _, prefix := range []string{"https://", "http://"} {
		if strings.HasPrefix(endpoint, prefix) {
			return endpoint[len(prefix):]
		}
	}
	return endpoint
}
