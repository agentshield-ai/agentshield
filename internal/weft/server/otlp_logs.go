package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/otlp"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/agentshield-ai/agentshield/internal/weft/weftlog"
)

// OTLP/HTTP logs and metrics. The built-in OpenTelemetry exports of Claude
// Code, Codex and Gemini CLI send a session's events as logs
// (otlp/claudecode_logs.go, codex_logs.go, gemini_logs.go); metrics are
// accepted and discarded so the exporters report no errors. Both take
// the same authentication and project selection as /otel/v1/traces: the
// project_id (or X-Weft-Project) header, else the resource attribute
// weft.project, else default/default.

// otlpPaths are the OTLP/HTTP ingest routes (their bodies are protobuf or
// OTLP JSON, and their project comes from a header or the resource).
var otlpPaths = map[string]bool{"/otel/v1/traces": true, "/otel/v1/logs": true, "/otel/v1/metrics": true, "/agents/otel/v1/traces": true}

func isOTLPPath(p string) bool { return otlpPaths[strings.TrimSuffix(p, "/")] }

// otlpHeaderProject is the project an OTLP request names in its headers.
func otlpHeaderProject(r *http.Request) string {
	if hp := r.Header.Get("project_id"); hp != "" {
		return hp
	}
	return r.Header.Get("X-Weft-Project")
}

func (s *Server) otelLogs(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	isJSON := otlp.IsJSON(r.Header.Get("Content-Type"))
	body, err := otlp.ReadBody(r.Body, r.Header.Get("Content-Encoding"))
	if err != nil {
		otlpRejected(r, "read", len(body), err) // ingestlog.go
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req, err := otlp.DecodeLogs(body, isJSON)
	if err != nil {
		otlpRejected(r, "decode", len(body), err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	batch, err := otlp.SessionLogEvents(req, otlpHeaderProject(r))
	if err != nil {
		otlpRejected(r, "map", len(body), err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ann := weftlog.RequestFrom(r.Context())
	projects := map[string]bool{}
	for _, byProject := range batch.Events {
		for pid := range byProject {
			projects[pid] = true
		}
	}
	ann.AddProjects(setKeys(projects)...)
	ctx, stats := store.WithIngestStats(r.Context())
	// The header project wins over the resource's (otlp.ProjectFor), so a
	// project-scoped key, which must send the header, writes only there.
	for _, src := range otlp.LogSources {
		for pid, events := range batch.Events[src.Name] {
			ended, _, err := s.store.IngestLogEvents(ctx, pid, src, events)
			if err != nil {
				writeErr(w, err)
				return
			}
			s.engine.Notify(ended)
		}
	}
	s.logLogsBatch(ctx, r, batch, setKeys(projects), stats, len(body), time.Since(start)) // ingestlog.go
	ann.SetSummarized()
	resp, ctype := otlp.EncodeLogsResponse(isJSON, 0, "")
	w.Header().Set("Content-Type", ctype)
	_, _ = w.Write(resp)
}

// otelMetrics accepts OTLP metrics and discards them.
func (s *Server) otelMetrics(w http.ResponseWriter, r *http.Request) {
	isJSON := otlp.IsJSON(r.Header.Get("Content-Type"))
	body, err := otlp.ReadBody(r.Body, r.Header.Get("Content-Encoding"))
	if err != nil {
		otlpRejected(r, "read", len(body), err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	weftlog.From(r.Context()).Debug("otlp metrics discarded", "route", routeOf(r), "bytes", len(body))
	weftlog.RequestFrom(r.Context()).SetSummarized()
	resp, ctype := otlp.EncodeMetricsResponse(isJSON)
	w.Header().Set("Content-Type", ctype)
	_, _ = w.Write(resp)
}
