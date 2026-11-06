package otlp

// OTLP/HTTP logs and metrics (POST /otel/v1/logs, /otel/v1/metrics).
//
// Logs: Claude Code's built-in OpenTelemetry export reports a session as
// log events (claudecode_logs.go turns them into spans). Logs of any other
// service are accepted and ignored: Weft has no generic log model.
// Metrics are accepted and discarded, so exporters configured to send them
// (Claude Code with OTEL_METRICS_EXPORTER=otlp) do not log export errors.

import (
	"bytes"
	"encoding/json"
	"fmt"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// DecodeLogs parses an ExportLogsServiceRequest (protobuf or OTLP JSON).
func DecodeLogs(body []byte, isJSON bool) (*collogs.ExportLogsServiceRequest, error) {
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
	return req, nil
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
