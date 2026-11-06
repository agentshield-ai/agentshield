// Package weftlog holds Weft's logging conventions: the handler the server
// writes with (text or JSON, log/slog), the request-scoped logger and the
// annotations handlers add to their request's log line.
//
// Weft logs ids, names, counts, sizes, durations and reasons only: never API
// keys, Authorization or cookie headers, provider keys, prompt text, tool
// arguments or outputs, request or response bodies, or attribute values from
// telemetry. Attribute names are shared across lines: project, session_id,
// trace_id, span_id, harness, source, count, reason, duration_ms, status.
package weftlog

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

// Formats are the values of --log-format.
const (
	FormatText = "text"
	FormatJSON = "json"
)

// NewHandler returns the handler for a format ("text" or "json").
func NewHandler(w io.Writer, format string, level slog.Leveler) (slog.Handler, error) {
	opts := &slog.HandlerOptions{Level: level}
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", FormatText:
		return slog.NewTextHandler(w, opts), nil
	case FormatJSON:
		return slog.NewJSONHandler(w, opts), nil
	}
	return nil, fmt.Errorf("invalid log format %q (text or json)", format)
}

// ---------------------------------------------------------------- logger in context

type loggerKey struct{}

// WithLogger returns a context whose log lines go to l (with its attributes,
// e.g. the request id).
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, l)
}

// From returns the context's logger, else slog.Default().
func From(ctx context.Context) *slog.Logger {
	if ctx != nil {
		if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok && l != nil {
			return l
		}
	}
	return slog.Default()
}

// ---------------------------------------------------------------- request annotations

// Request collects what a request's handlers know that its log line should
// say: the projects it touched, who made it, why it failed.
type Request struct {
	mu         sync.Mutex
	projects   []string
	actor      string
	keyID      string
	authMethod string
	reason     string
	err        error
	summarized bool
}

type requestKey struct{}

// WithRequest attaches a request annotation to ctx.
func WithRequest(ctx context.Context, r *Request) context.Context {
	return context.WithValue(ctx, requestKey{}, r)
}

// RequestFrom returns the request annotation of ctx (nil outside a request).
func RequestFrom(ctx context.Context) *Request {
	r, _ := ctx.Value(requestKey{}).(*Request)
	return r
}

// AddProjects records projects the request named or wrote to.
func (r *Request) AddProjects(pids ...string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range pids {
		if p == "" {
			continue
		}
		dup := false
		for _, have := range r.projects {
			if have == p {
				dup = true
				break
			}
		}
		if !dup {
			r.projects = append(r.projects, p)
		}
	}
}

// SetIdentity records who made the request: a user name (or "static-key"),
// the managed key id (never the key) and how it authenticated.
func (r *Request) SetIdentity(actor, keyID, method string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.actor, r.keyID, r.authMethod = actor, keyID, method
	r.mu.Unlock()
}

// SetSummarized marks a request whose handler logged its own summary line
// (an ingest batch): its request line moves to debug when it succeeds.
func (r *Request) SetSummarized() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.summarized = true
	r.mu.Unlock()
}

// Snapshot is a copy of a request annotation.
type Snapshot struct {
	Projects                 []string
	Actor, KeyID, AuthMethod string
	Reason                   string
	Err                      error
	Summarized               bool
}

// Snapshot copies the annotation.
func (r *Request) Snapshot() Snapshot {
	if r == nil {
		return Snapshot{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return Snapshot{Projects: append([]string(nil), r.projects...), Actor: r.actor, KeyID: r.keyID, AuthMethod: r.authMethod,
		Reason: r.reason, Err: r.err, Summarized: r.summarized}
}

// errRecorder is implemented by the request log's response writer.
type errRecorder interface {
	weftlogRequest() *Request
}

// Recorder wraps a response writer so that RecordError and RecordReason find
// the request annotation from the writer alone (writeErr has no request).
type Recorder struct {
	http.ResponseWriter
	Req *Request
	// Status and Bytes are what was written.
	Status int
	Bytes  int64
}

func (w *Recorder) weftlogRequest() *Request { return w.Req }

// WriteHeader records the status.
func (w *Recorder) WriteHeader(code int) {
	if w.Status == 0 {
		w.Status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

// Write counts the bytes written.
func (w *Recorder) Write(b []byte) (int, error) {
	if w.Status == 0 {
		w.Status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.Bytes += int64(n)
	return n, err
}

// Flush passes a flush on (streams).
func (w *Recorder) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		if w.Status == 0 {
			w.Status = http.StatusOK
		}
		f.Flush()
	}
}

// Unwrap returns the wrapped writer (http.ResponseController).
func (w *Recorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// requestOf finds the annotation through a chain of wrapping writers.
func requestOf(w http.ResponseWriter) *Request {
	for i := 0; w != nil && i < 16; i++ {
		if rec, ok := w.(errRecorder); ok {
			return rec.weftlogRequest()
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return nil
		}
		w = u.Unwrap()
	}
	return nil
}

// RecordError attaches an internal error to the request's log line. It
// reports false when w is not served through the request log (the caller
// then logs the error itself).
func RecordError(w http.ResponseWriter, err error) bool {
	r := requestOf(w)
	if r == nil {
		return false
	}
	r.mu.Lock()
	r.err = err
	r.mu.Unlock()
	return true
}

// RecordReason attaches the reason a request was refused (the message of a
// 4xx answer) to its log line.
func RecordReason(w http.ResponseWriter, reason string) {
	if r := requestOf(w); r != nil {
		r.mu.Lock()
		if r.reason == "" {
			r.reason = reason
		}
		r.mu.Unlock()
	}
}

// ---------------------------------------------------------------- safe values

var quoted = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'`)

// Reason makes an error message or refusal safe to log: quoted fragments,
// which may echo a value from the request (a decoder's "unexpected token
// \"...\""), are elided, and the text is cut to 300 bytes.
func Reason(s string) string {
	s = quoted.ReplaceAllStringFunc(s, func(q string) string {
		if len(q) <= 2 {
			return q
		}
		return q[:1] + "…" + q[len(q)-1:]
	})
	return Truncate(s, 300)
}

// Err is Reason of an error's message ("" for nil).
func Err(err error) string {
	if err == nil {
		return ""
	}
	return Reason(err.Error())
}

// Truncate cuts s to at most n bytes on a rune boundary, marking the cut.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// Name makes a name taken from a request (a service, a tool, a route
// segment) safe to log: control characters dropped, at most 64 bytes.
func Name(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	return Truncate(s, 64)
}

// MaxListed bounds the ids a log line lists.
const MaxListed = 20

// Capped returns at most MaxListed of xs, sorted, and how many there were.
func Capped(xs []string) ([]string, int) {
	out := append([]string(nil), xs...)
	sort.Strings(out)
	if len(out) > MaxListed {
		out = out[:MaxListed]
	}
	return out, len(xs)
}

// Counts renders a breakdown as "a=1,b=2" (sorted keys, zero counts left
// out), "" when empty.
func Counts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k, v := range m {
		if v != 0 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%s=%d", k, m[k])
	}
	return b.String()
}
