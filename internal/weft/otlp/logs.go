package otlp

// OTLP/HTTP logs and metrics (POST /otel/v1/logs, /otel/v1/metrics).
//
// Logs: the built-in OpenTelemetry exports of Claude Code
// (claudecode_logs.go), OpenAI Codex CLI (codex_logs.go) and Google Gemini
// CLI (gemini_logs.go) report a session as log events, which are turned
// into spans. Logs of any other service are accepted and ignored: Weft has
// no generic log model. Metrics are accepted and discarded, so exporters
// configured to send them do not log export errors.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// DecodeLogs parses an ExportLogsServiceRequest (protobuf or OTLP JSON).
func DecodeLogs(body []byte, isJSON bool) (*collogs.ExportLogsServiceRequest, error) {
	return DecodeLogsWith(context.Background(), body, isJSON, nil)
}

// DecodeLogsWith is DecodeLogs with the element limits and budget of
// DecodeWith.
func DecodeLogsWith(ctx context.Context, body []byte, isJSON bool, res *Reservation) (*collogs.ExportLogsServiceRequest, error) {
	if err := checkElements(ctx, body, isJSON, pbLogsReq, res); err != nil {
		return nil, err
	}
	req := &collogs.ExportLogsServiceRequest{}
	if !isJSON {
		if err := proto.Unmarshal(body, req); err != nil {
			return nil, fmt.Errorf("invalid OTLP protobuf: %w", err)
		}
		return req, nil
	}
	var generic any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&generic); err != nil {
		return nil, fmt.Errorf("invalid OTLP JSON: %w", err)
	}
	fixIDs(generic) // log records carry hex traceId / spanId too
	fixed, err := json.Marshal(generic)
	if err != nil {
		return nil, err
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(fixed, req); err != nil {
		return nil, fmt.Errorf("invalid OTLP JSON: %w", err)
	}
	n := 0
	for _, rl := range req.GetResourceLogs() {
		for _, sl := range rl.GetScopeLogs() {
			n += len(sl.GetLogRecords())
		}
	}
	if n > MaxExportSpans {
		return nil, tooLarge("OTLP request has more than %d log records; send smaller batches", MaxExportSpans)
	}
	return req, nil
}

// LogSources are the harnesses whose log events Weft maps, in the order
// their batches are stored.
var LogSources = []store.LogSource{
	{Name: ClaudeCodeLogSource, SpanSource: store.SourceClaudeCodeLogs, Build: BuildClaudeCodeLogGroup},
	{Name: CodexLogSource, SpanSource: store.SourceCodexLogs, Build: BuildCodexLogGroup},
	{Name: GeminiLogSource, SpanSource: store.SourceGeminiLogs, Build: BuildGeminiLogGroup},
}

// SessionLogs is a logs export sorted by harness.
type SessionLogs struct {
	// Events by source name (LogSources), then project.
	Events map[string]map[string][]store.LogEvent
	// Ignored counts records that describe no session; IgnoredBy breaks it
	// down by reason (Ignored* in otlp.go).
	Ignored   int
	IgnoredBy map[string]int
	// Records counts the log records received, Harness by harness ("other"
	// for logs Weft does not map), Services the service names seen.
	Records  int
	Harness  map[string]int
	Services map[string]bool
}

// SessionLogEvents extracts the session events of every harness Weft maps
// from a logs export. Each resource's records go to the harness its
// service.name (or instrumentation scope) names; the project is chosen as
// for traces (ProjectFor).
func SessionLogEvents(req *collogs.ExportLogsServiceRequest, headerProject string) (SessionLogs, error) {
	out := SessionLogs{Events: map[string]map[string][]store.LogEvent{}, IgnoredBy: map[string]int{}, Harness: map[string]int{},
		Services: map[string]bool{}}
	for _, rl := range req.GetResourceLogs() {
		sub := &collogs.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{rl}}
		n := 0
		for _, sl := range rl.GetScopeLogs() {
			n += len(sl.GetLogRecords())
		}
		out.Records += n
		if svc := aStr(KeyValues(rl.GetResource().GetAttributes()), "service.name"); svc != "" {
			out.Services[svc] = true
		}
		h := logHarness(rl)
		if h == "" {
			out.Harness["other"] += n
		} else {
			out.Harness[h] += n
		}
		var (
			b   LogBatch
			src string
			err error
		)
		switch h {
		case ClaudeCodeLogSource:
			src = ClaudeCodeLogSource
			b, err = ClaudeCodeLogEvents(sub, headerProject)
		case CodexLogSource:
			src = CodexLogSource
			b, err = CodexLogEvents(sub, headerProject)
		case GeminiLogSource:
			src = GeminiLogSource
			b, err = GeminiLogEvents(sub, headerProject)
		default:
			out.Ignored += n
			out.IgnoredBy[IgnoredOtherService] += n
			continue
		}
		if err != nil {
			return out, err
		}
		out.Ignored += b.Ignored
		for k, v := range b.IgnoredBy {
			out.IgnoredBy[k] += v
		}
		for pid, evs := range b.Events {
			if out.Events[src] == nil {
				out.Events[src] = map[string][]store.LogEvent{}
			}
			out.Events[src][pid] = append(out.Events[src][pid], evs...)
		}
	}
	return out, nil
}

// logHarness names the harness a resource's logs come from ("" if none).
func logHarness(rl *logspb.ResourceLogs) string {
	svc := aStr(KeyValues(rl.GetResource().GetAttributes()), "service.name")
	switch {
	case svc == ClaudeCodeService:
		return ClaudeCodeLogSource
	case svc == GeminiService:
		return GeminiLogSource
	case strings.HasPrefix(svc, "codex"):
		return CodexLogSource
	}
	for _, sl := range rl.GetScopeLogs() {
		switch name := sl.GetScope().GetName(); {
		case name == ClaudeCodeEventsScope:
			return ClaudeCodeLogSource
		case name == GeminiService:
			return GeminiLogSource
		case strings.HasPrefix(name, "codex_otel"):
			return CodexLogSource
		}
	}
	return ""
}

// EncodeLogsResponse returns an ExportLogsServiceResponse.
func EncodeLogsResponse(isJSON bool, rejected int64, msg string) ([]byte, string) {
	resp := &collogs.ExportLogsServiceResponse{}
	if rejected > 0 {
		resp.PartialSuccess = &collogs.ExportLogsPartialSuccess{RejectedLogRecords: rejected, ErrorMessage: msg}
	}
	return encodeResponse(resp, isJSON)
}

// EncodeMetricsResponse returns an empty ExportMetricsServiceResponse.
func EncodeMetricsResponse(isJSON bool) ([]byte, string) {
	return encodeResponse(&colmetrics.ExportMetricsServiceResponse{}, isJSON)
}

func encodeResponse(m proto.Message, isJSON bool) ([]byte, string) {
	if isJSON {
		b, _ := protojson.Marshal(m)
		return b, "application/json"
	}
	b, _ := proto.Marshal(m)
	return b, "application/x-protobuf"
}
