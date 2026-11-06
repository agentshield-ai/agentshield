package server

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/otlp"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/agentshield-ai/agentshield/internal/weft/weftlog"
)

// Ingest log lines (docs/weft/guides/setup.md, "Logs"): one info line per
// OTLP or verdict batch with counts only, the sessions it touched at debug.
// Nothing from the payload is logged except ids (sessions, spans) and the
// harness Weft recognised.

// otlpRejected logs a batch that could not be read or decoded: why, its
// content type, encoding and size, never its body.
func otlpRejected(r *http.Request, stage string, size int, err error) {
	attrs := []any{"route", routeOf(r), "stage", stage, "reason", weftlog.Err(err),
		"content_type", weftlog.Name(r.Header.Get("Content-Type")), "content_encoding", weftlog.Name(r.Header.Get("Content-Encoding")), "bytes", size}
	if p := otlpHeaderProject(r); p != "" {
		attrs = append(attrs, "project", weftlog.Name(p))
	}
	weftlog.From(r.Context()).Warn("otlp batch rejected", attrs...)
}

func setKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sumCounts(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// projectAttr is "project" for one project, "projects" for several.
func projectAttr(pids []string) slog.Attr {
	if len(pids) == 1 {
		return slog.String("project", pids[0])
	}
	ps, _ := weftlog.Capped(pids)
	return slog.String("projects", strings.Join(ps, ","))
}

// storeAttrs are the counts the store collected while writing a batch.
func storeAttrs(st *store.IngestStats) []slog.Attr {
	attrs := []slog.Attr{slog.Int("spans_stored", st.SpansStored), slog.Int("dropped_by_source", st.SpansDropped)}
	if st.SpansSampled > 0 {
		attrs = append(attrs, slog.Int("sampled_out", st.SpansSampled))
	}
	if st.SourcesReplaced > 0 {
		attrs = append(attrs, slog.Int("sources_replaced", st.SourcesReplaced))
	}
	if st.Verdicts > 0 {
		attrs = append(attrs, slog.Int("verdicts", st.Verdicts))
	}
	if links := st.LinkCounts(); len(links) > 0 {
		attrs = append(attrs, slog.String("verdicts_attached", weftlog.Counts(links)))
	}
	return attrs
}

// logSessions lists the sessions a batch touched (debug).
func logSessions(ctx context.Context, msg string, st *store.IngestStats, services map[string]bool) {
	log := weftlog.From(ctx)
	if !log.Enabled(ctx, slog.LevelDebug) {
		return
	}
	ids, n := weftlog.Capped(st.Sessions())
	if n == 0 {
		return
	}
	svcs := setKeys(services)
	for i := range svcs {
		svcs[i] = weftlog.Name(svcs[i])
	}
	if len(svcs) > 5 {
		svcs = svcs[:5]
	}
	log.Debug(msg, "count", n, "session_ids", strings.Join(ids, ","), "services", strings.Join(svcs, ","))
}

func (s *Server) logTraceBatch(ctx context.Context, r *http.Request, ts otlp.TraceStats, st *store.IngestStats, bytes int, d time.Duration) {
	log := weftlog.From(ctx)
	level := slog.LevelInfo
	if ts.Spans == 0 {
		level = slog.LevelDebug
	}
	attrs := []slog.Attr{slog.String("route", routeOf(r))}
	if len(ts.Projects) > 0 {
		attrs = append(attrs, projectAttr(setKeys(ts.Projects)))
	}
	attrs = append(attrs, slog.String("harness", weftlog.Counts(ts.Harness)), slog.Int("spans_received", ts.Spans))
	attrs = append(attrs, storeAttrs(st)...)
	attrs = append(attrs, slog.Int("ignored", sumCounts(ts.Ignored)))
	if len(ts.Ignored) > 0 {
		attrs = append(attrs, slog.String("ignored_by", weftlog.Counts(ts.Ignored)))
	}
	attrs = append(attrs, slog.Int("sessions", len(st.Sessions())), slog.Int("bytes", bytes), slog.Int64("duration_ms", d.Milliseconds()))
	log.LogAttrs(ctx, level, "otlp traces ingested", attrs...)
	logSessions(ctx, "otlp traces sessions", st, ts.Services)
}

func (s *Server) logLogsBatch(ctx context.Context, r *http.Request, b otlp.SessionLogs, projects []string, st *store.IngestStats, bytes int, d time.Duration) {
	log := weftlog.From(ctx)
	level := slog.LevelInfo
	if b.Records == 0 {
		level = slog.LevelDebug
	}
	attrs := []slog.Attr{slog.String("route", routeOf(r))}
	if len(projects) > 0 {
		attrs = append(attrs, projectAttr(projects))
	}
	attrs = append(attrs, slog.String("harness", weftlog.Counts(b.Harness)), slog.Int("records", b.Records),
		slog.Int("events_new", st.EventsNew), slog.Int("events_duplicate", st.EventsDuplicate), slog.Int("ignored", b.Ignored))
	if len(b.IgnoredBy) > 0 {
		attrs = append(attrs, slog.String("ignored_by", weftlog.Counts(b.IgnoredBy)))
	}
	attrs = append(attrs, slog.Int("turns_rebuilt", st.TurnsRebuilt))
	if st.TurnsInterrupted > 0 {
		attrs = append(attrs, slog.Int("turns_interrupted", st.TurnsInterrupted))
	}
	attrs = append(attrs, storeAttrs(st)...)
	attrs = append(attrs, slog.Int("sessions", len(st.Sessions())), slog.Int("bytes", bytes), slog.Int64("duration_ms", d.Milliseconds()))
	log.LogAttrs(ctx, level, "otlp logs ingested", attrs...)
	logSessions(ctx, "otlp logs sessions", st, b.Services)
}
