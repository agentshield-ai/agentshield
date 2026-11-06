package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/weftlog"
	"github.com/go-chi/chi/v5"
)

// The request log: one line per request (docs/weft/guides/setup.md, "Logs").
//
//	writes and refusals (4xx)     info
//	server errors (5xx)           error, with the error
//	successful reads, the UI's
//	static files, /health         debug
//	ingest batches                debug when they succeed: the handler logs
//	                              its own summary line at info
//
// A line carries the method, the route pattern (the raw path only at debug,
// it holds ids), status, duration_ms, bytes_in, bytes_out, the projects
// named, the caller (user name and managed key id, never the key), how it
// authenticated, the remote address and a request id that the request's
// other lines (ingest, verdict matching) carry too.

var requestSeq atomic.Uint64

// countingBody counts the bytes a handler reads from a request body.
type countingBody struct {
	io.ReadCloser
	n int64
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n += int64(n)
	return n, err
}

// requestLog logs each request once it is served and recovers a handler
// panic as a 500 (logged with its stack).
func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := strconv.FormatUint(requestSeq.Add(1), 36)
		ann := &weftlog.Request{}
		rlog := s.log.With("req_id", id)
		ctx := weftlog.WithRequest(weftlog.WithLogger(r.Context(), rlog), ann)
		r = r.WithContext(ctx)
		var body *countingBody
		if r.Body != nil && r.Body != http.NoBody {
			body = &countingBody{ReadCloser: r.Body}
			r.Body = body
		}
		rec := &weftlog.Recorder{ResponseWriter: w, Req: ann}
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler { //nolint:errorlint // sentinel compared as net/http does
					panic(v)
				}
				rlog.Error("panic serving request", "method", r.Method, "route", routeOf(r), "panic", weftlog.Reason(fmt.Sprint(v)),
					"stack", string(debug.Stack()))
				if rec.Status == 0 {
					rec.Header().Set("Content-Type", "application/json")
					rec.WriteHeader(http.StatusInternalServerError)
					_, _ = rec.Write([]byte(`{"reason":"internal error"}` + "\n"))
				} else {
					rec.Status = http.StatusInternalServerError
				}
			}
			var in int64
			if body != nil {
				in = body.n
			}
			s.logRequest(r.Context(), rlog, r, rec, in, time.Since(start))
		}()
		next.ServeHTTP(rec, r)
	})
}

// routeOf is the chi route pattern that served r ("" when none matched:
// the UI or a 404).
func routeOf(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		return rc.RoutePattern()
	}
	return ""
}

func (s *Server) logRequest(ctx context.Context, log *slog.Logger, r *http.Request, rec *weftlog.Recorder, bytesIn int64, d time.Duration) {
	status := rec.Status
	if status == 0 { // the handler wrote nothing: net/http sends 200
		status = http.StatusOK
	}
	route := routeOf(r)
	ann := rec.Req.Snapshot()
	level := slog.LevelInfo
	quiet := r.URL.Path == "/health" || (route == "" && (r.Method == http.MethodGet || r.Method == http.MethodHead) && status < 400 && !s.apiPath(r))
	switch {
	case quiet:
		level = slog.LevelDebug
	case status >= 500:
		level = slog.LevelError
	case status >= 400:
		level = slog.LevelInfo
	case ann.Summarized, isReadRequest(r):
		level = slog.LevelDebug
	}
	if !log.Enabled(ctx, level) {
		return
	}
	if route == "" {
		route = "(unmatched)"
		if quiet {
			route = "(ui)"
		}
	}
	attrs := make([]slog.Attr, 0, 16)
	attrs = append(attrs, slog.String("method", r.Method), slog.String("route", route), slog.Int("status", status),
		slog.Int64("duration_ms", d.Milliseconds()), slog.Int64("bytes_in", bytesIn), slog.Int64("bytes_out", rec.Bytes))
	if log.Enabled(ctx, slog.LevelDebug) {
		attrs = append(attrs, slog.String("path", weftlog.Truncate(r.URL.Path, 200)))
	}
	switch len(ann.Projects) {
	case 0:
	case 1:
		attrs = append(attrs, slog.String("project", ann.Projects[0]))
	default:
		ps, _ := weftlog.Capped(ann.Projects)
		attrs = append(attrs, slog.String("projects", strings.Join(ps, ",")))
	}
	if ann.Actor != "" {
		attrs = append(attrs, slog.String("user", weftlog.Name(ann.Actor)))
	}
	if ann.KeyID != "" {
		attrs = append(attrs, slog.String("key_id", ann.KeyID))
	}
	if ann.AuthMethod != "" {
		attrs = append(attrs, slog.String("auth", ann.AuthMethod))
	}
	attrs = append(attrs, slog.String("remote", clientIP(r)))
	if status >= 400 && ann.Reason != "" {
		attrs = append(attrs, slog.String("reason", weftlog.Reason(ann.Reason)))
	}
	if ann.Err != nil {
		attrs = append(attrs, slog.String("err", weftlog.Err(ann.Err)))
	}
	log.LogAttrs(ctx, level, "request", attrs...)
}
