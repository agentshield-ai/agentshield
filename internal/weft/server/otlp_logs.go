package server

import (
	"net/http"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/otlp"
)

// OTLP/HTTP logs and metrics. Claude Code's built-in OpenTelemetry export
// sends a session's events as logs (otlp/claudecode_logs.go); its metrics
// are accepted and discarded so the exporter reports no errors. Both take
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
	isJSON := otlp.IsJSON(r.Header.Get("Content-Type"))
	body, err := otlp.ReadBody(r.Body, r.Header.Get("Content-Encoding"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req, err := otlp.DecodeLogs(body, isJSON)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	batch, err := otlp.ClaudeCodeLogEvents(req, otlpHeaderProject(r))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// The header project wins over the resource's (otlp.ProjectFor), so a
	// project-scoped key, which must send the header, writes only there.
	for pid, events := range batch.Events {
		ended, _, err := s.store.IngestLogEvents(r.Context(), pid, otlp.ClaudeCodeLogSource, events, otlp.BuildClaudeCodeLogGroup)
		if err != nil {
			writeErr(w, err)
			return
		}
		s.engine.Notify(ended)
	}
	resp, ctype := otlp.EncodeLogsResponse(isJSON, 0, "")
	w.Header().Set("Content-Type", ctype)
	_, _ = w.Write(resp)
}

// otelMetrics accepts OTLP metrics and discards them.
func (s *Server) otelMetrics(w http.ResponseWriter, r *http.Request) {
	isJSON := otlp.IsJSON(r.Header.Get("Content-Type"))
	if _, err := otlp.ReadBody(r.Body, r.Header.Get("Content-Encoding")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp, ctype := otlp.EncodeMetricsResponse(isJSON)
	w.Header().Set("Content-Type", ctype)
	_, _ = w.Write(resp)
}
