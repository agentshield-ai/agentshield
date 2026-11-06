package otlp

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// Request body limits. A body is read (and gunzipped) into memory before it
// is decoded, and decoding multiplies it, so every body is bounded three
// ways: its decompressed size (per route), its compression ratio, and a
// server-wide budget of bytes being decoded at once (BodyBudget), which
// requests wait for (then 503) instead of exhausting memory together.
const (
	// MaxExportBytes bounds a decompressed OTLP export (traces, logs,
	// metrics). OTLP exporters send batches far smaller.
	MaxExportBytes = 16 << 20
	// MaxExportSpans bounds the spans (log records) of one OTLP export,
	// and with them how long one request holds the database writer.
	MaxExportSpans = 10000
	// MaxCompressionRatio is the largest decompressed:compressed ratio a
	// gzip body may have once it is over ratioFloor bytes decompressed.
	MaxCompressionRatio = 100
	ratioFloor          = 1 << 20
	// maxExportElements bounds the protobuf messages of an export
	// (resources, scopes, spans, events, links, attributes, values) and
	// maxExportJSONValues the values of an OTLP JSON export, checked before
	// decoding: an empty message is 2 bytes on the wire and ~100 in memory.
	maxExportElements   = 2_000_000
	maxExportJSONValues = 1_000_000
	// Estimated memory per decoded protobuf message and JSON value,
	// reserved in the body budget while the request is handled.
	costPerMessage   = 96
	costPerJSONValue = 160
	// DefaultBodyBudget is the default server-wide budget.
	DefaultBodyBudget = 512 << 20
	// budgetWait is how long a request waits for budget before a 503.
	budgetWait = 10 * time.Second
)

// BodyError is a request body refused before it was decoded: Status is 413
// (too large, too compressed, too many elements) or 503 (the server is
// decoding too much at once; retry).
type BodyError struct {
	Status int
	Msg    string
}

func (e *BodyError) Error() string { return e.Msg }

func tooLarge(format string, args ...any) error {
	return &BodyError{Status: http.StatusRequestEntityTooLarge, Msg: fmt.Sprintf(format, args...)}
}

// BodyStatus is the HTTP status for an error reading or decoding a body:
// a BodyError's, 413 for http.MaxBytesReader's limit, else 400.
func BodyStatus(err error) int {
	var be *BodyError
	if errors.As(err, &be) {
		return be.Status
	}
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

// BodyBudget is a server-wide budget of bytes held by request bodies being
// read and decoded.
type BodyBudget struct {
	mu      sync.Mutex
	limit   int64
	used    int64
	changed chan struct{}
}

// NewBodyBudget returns a budget of limit bytes.
func NewBodyBudget(limit int64) *BodyBudget {
	return &BodyBudget{limit: limit, changed: make(chan struct{})}
}

var defaultBudget = NewBodyBudget(DefaultBodyBudget)

// NewReservation returns an empty reservation on b, to Grow.
func (b *BodyBudget) NewReservation() *Reservation { return &Reservation{b: b} }

// Limit is b's size in bytes.
func (b *BodyBudget) Limit() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.limit
}

// InUse reports the bytes reserved and b's size.
func (b *BodyBudget) InUse() (used, limit int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used, b.limit
}

// SetBodyBudget changes the server-wide budget's size (bytes > 0).
func SetBodyBudget(bytes int64) {
	if bytes <= 0 {
		return
	}
	defaultBudget.mu.Lock()
	defaultBudget.limit = bytes
	defaultBudget.mu.Unlock()
}

// BodyBudgetInUse reports the bytes reserved and the budget's size.
func BodyBudgetInUse() (used, limit int64) {
	defaultBudget.mu.Lock()
	defer defaultBudget.mu.Unlock()
	return defaultBudget.used, defaultBudget.limit
}

func (b *BodyBudget) acquire(ctx context.Context, n int64) error {
	if n <= 0 {
		return nil
	}
	var timer *time.Timer
	for {
		b.mu.Lock()
		if n > b.limit {
			b.mu.Unlock()
			return tooLarge("request body too large to decode")
		}
		if b.used+n <= b.limit {
			b.used += n
			b.mu.Unlock()
			if timer != nil {
				timer.Stop()
			}
			return nil
		}
		ch := b.changed
		b.mu.Unlock()
		if timer == nil {
			timer = time.NewTimer(budgetWait)
			defer timer.Stop()
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return &BodyError{Status: http.StatusServiceUnavailable, Msg: "server busy decoding other requests; retry later"}
		}
	}
}

func (b *BodyBudget) release(n int64) {
	if n <= 0 {
		return
	}
	b.mu.Lock()
	b.used -= n
	close(b.changed)
	b.changed = make(chan struct{})
	b.mu.Unlock()
}

// Reservation is a request's share of the body budget. Release it when the
// decoded request is no longer needed; a nil Reservation is valid.
type Reservation struct {
	b    *BodyBudget
	mu   sync.Mutex
	n    int64
	done bool
}

// Grow reserves n more bytes (estimated decode memory), waiting for budget.
func (r *Reservation) Grow(ctx context.Context, n int64) error {
	if r == nil || n <= 0 {
		return nil
	}
	if err := r.b.acquire(ctx, n); err != nil {
		return err
	}
	r.mu.Lock()
	if r.done { // released meanwhile
		r.mu.Unlock()
		r.b.release(n)
		return nil
	}
	r.n += n
	r.mu.Unlock()
	return nil
}

// Release returns the reservation to the budget. It is idempotent.
func (r *Reservation) Release() {
	if r == nil {
		return
	}
	r.mu.Lock()
	n := r.n
	r.n, r.done = 0, true
	r.mu.Unlock()
	r.b.release(n)
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// ReadBodyBudget reads a request body, gunzipping it when encoding is
// "gzip", into memory: at most maxBytes decompressed, at a compression ratio
// of at most MaxCompressionRatio, reserving the bytes in the server-wide
// budget as they arrive. The caller releases the Reservation (also on
// error it is released already). Errors carry their status (BodyStatus).
// This is the helper JSON request decoding shares with OTLP.
func ReadBodyBudget(ctx context.Context, r io.Reader, encoding string, maxBytes int64) ([]byte, *Reservation, error) {
	res := &Reservation{b: defaultBudget}
	body, err := readLimited(ctx, r, encoding, maxBytes, res)
	if err != nil {
		res.Release()
		return nil, nil, err
	}
	return body, res, nil
}

func readLimited(ctx context.Context, r io.Reader, encoding string, maxBytes int64, res *Reservation) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	in := &countingReader{r: r}
	var src io.Reader = in
	gz := false
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
	case "gzip":
		zr, err := gzip.NewReader(in)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, nil
			}
			return nil, fmt.Errorf("invalid gzip body: %w", err)
		}
		defer zr.Close()
		src, gz = zr, true
	default:
		return nil, &BodyError{Status: http.StatusUnsupportedMediaType, Msg: fmt.Sprintf("unsupported Content-Encoding %q", encoding)}
	}
	var buf bytes.Buffer
	chunk := make([]byte, 64<<10)
	for {
		if err := res.Grow(ctx, int64(len(chunk))); err != nil {
			return nil, err
		}
		n, err := io.ReadFull(src, chunk)
		buf.Write(chunk[:n])
		if int64(buf.Len()) > maxBytes {
			return nil, tooLarge("request body too large (over %d bytes decompressed)", maxBytes)
		}
		if gz && buf.Len() > ratioFloor && int64(buf.Len()) > MaxCompressionRatio*max(in.n, 1) {
			return nil, tooLarge("request body compressed more than %d:1", MaxCompressionRatio)
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// ------------------------------------------------------------ element counts

// Protobuf message kinds of the OTLP trace and logs requests whose counts
// bound the decoded size.
type pbKind uint8

const (
	pbNone pbKind = iota
	pbTraceReq
	pbResourceSpans
	pbResource
	pbScopeSpans
	pbScope
	pbSpan
	pbEvent
	pbLink
	pbStatus
	pbKeyValue
	pbAnyValue
	pbArrayValue
	pbKVList
	pbLogsReq
	pbResourceLogs
	pbScopeLogs
	pbLogRecord
)

// pbChildren maps a message's length-delimited fields that hold messages
// to their kind (opentelemetry-proto v1 field numbers).
var pbChildren = map[pbKind]map[protowire.Number]pbKind{
	pbTraceReq:      {1: pbResourceSpans},
	pbResourceSpans: {1: pbResource, 2: pbScopeSpans},
	pbResource:      {1: pbKeyValue},
	pbScopeSpans:    {1: pbScope, 2: pbSpan},
	pbScope:         {3: pbKeyValue},
	pbSpan:          {9: pbKeyValue, 11: pbEvent, 13: pbLink, 15: pbStatus},
	pbEvent:         {3: pbKeyValue},
	pbLink:          {5: pbKeyValue},
	pbKeyValue:      {2: pbAnyValue},
	pbAnyValue:      {5: pbArrayValue, 6: pbKVList},
	pbArrayValue:    {1: pbAnyValue},
	pbKVList:        {1: pbKeyValue},
	pbLogsReq:       {1: pbResourceLogs},
	pbResourceLogs:  {1: pbResource, 2: pbScopeLogs},
	pbScopeLogs:     {1: pbScope, 2: pbLogRecord},
	pbLogRecord:     {5: pbAnyValue, 6: pbKeyValue},
}

const maxPBDepth = 10000 // protobuf-go's default recursion limit

type elemCount struct {
	msgs, items int64 // all messages; spans or log records
}

func (c *elemCount) walk(b []byte, k pbKind, depth int) error {
	if depth > maxPBDepth {
		return tooLarge("OTLP request nested too deeply")
	}
	children := pbChildren[k]
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil // malformed: proto.Unmarshal reports it
		}
		b = b[n:]
		if typ != protowire.BytesType {
			m := protowire.ConsumeFieldValue(num, typ, b)
			if m < 0 {
				return nil
			}
			b = b[m:]
			continue
		}
		v, m := protowire.ConsumeBytes(b)
		if m < 0 {
			return nil
		}
		b = b[m:]
		ck, ok := children[num]
		if !ok {
			continue
		}
		c.msgs++
		if ck == pbSpan || ck == pbLogRecord {
			c.items++
			if c.items > MaxExportSpans {
				return tooLarge("OTLP request has more than %d spans or log records; send smaller batches", MaxExportSpans)
			}
		}
		if c.msgs > maxExportElements {
			return tooLarge("OTLP request has more than %d elements", maxExportElements)
		}
		if err := c.walk(v, ck, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// countProto counts an OTLP protobuf request's messages before it is
// unmarshalled, refusing one over the limits.
func countProto(body []byte, root pbKind) (elemCount, error) {
	var c elemCount
	err := c.walk(body, root, 0)
	return c, err
}

// JSONValues is an upper bound on the values (objects, arrays, members,
// elements) of a JSON document, counted without decoding it: decoding
// into generic values costs memory per value.
func JSONValues(body []byte) int64 {
	var n int64 = 1
	inStr, esc := false, false
	for _, ch := range body {
		if inStr {
			switch {
			case esc:
				esc = false
			case ch == '\\':
				esc = true
			case ch == '"':
				inStr = false
			}
			continue
		}
		switch ch {
		case '"':
			inStr = true
		case '{', '[', ',':
			n++
		}
	}
	return n
}

// checkElements refuses an export over the element limits and reserves
// its estimated decoded size in res (nil: no reservation).
func checkElements(ctx context.Context, body []byte, isJSON bool, root pbKind, res *Reservation) error {
	if isJSON {
		n := JSONValues(body)
		if n > maxExportJSONValues {
			return tooLarge("OTLP JSON request has more than %d values", maxExportJSONValues)
		}
		return res.Grow(ctx, n*costPerJSONValue+2*int64(len(body)))
	}
	c, err := countProto(body, root)
	if err != nil {
		return err
	}
	return res.Grow(ctx, c.msgs*costPerMessage)
}
