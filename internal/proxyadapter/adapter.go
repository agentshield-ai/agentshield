package proxyadapter

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"

	"github.com/agentshield-ai/agentshield/internal/models"
)

// Stats counts what the adapter has done so far. Read it after Run returns
// (or via a separate goroutine if you take a snapshot mid-run; only the
// totals are published from the main loop).
type Stats struct {
	Lines     int // total lines read from the source
	Parsed    int // lines that decoded as valid JSON
	Forwarded int // observations successfully posted to the engine
	Dropped   int // observations dropped (parse failure, normalise failure, post failure)
}

// Forwarder is the minimal contract the adapter loop needs from a Client;
// stubbed in tests so the loop can run without a live engine.
type Forwarder interface {
	Post(req *models.EvaluationRequest) (*EvalResponse, error)
}

// Run consumes NDJSON observations from r, normalises each into an
// AgentShield evaluation request, and forwards it via fwd. Cancel via ctx.
//
// Behaviour:
//   - Empty / whitespace-only lines: skipped silently.
//   - JSON parse errors, missing required fields, post failures: logged at
//     debug level, counted as Dropped, loop continues. Adapters running
//     against a noisy proxy log shouldn't crash on the first malformed line.
//   - Returns the final Stats and whichever scanner/post error stopped the
//     stream (io.EOF returns nil).
func Run(ctx context.Context, r io.Reader, fwd Forwarder, source string) (Stats, error) {
	var stats Stats
	scanner := bufio.NewScanner(r)
	// Allow long lines (proxy logs can include large bodies).
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return stats, ctx.Err()
		default:
		}

		stats.Lines++
		line := scanner.Bytes()
		if len(bytesTrimSpace(line)) == 0 {
			continue
		}

		var obs Observation
		if err := json.Unmarshal(line, &obs); err != nil {
			slog.Debug("proxyadapter: parse failure", "error", err)
			stats.Dropped++
			continue
		}
		stats.Parsed++

		req, err := Normalize(obs, source)
		if err != nil {
			slog.Debug("proxyadapter: normalise failure", "error", err, "request_id", obs.RequestID)
			stats.Dropped++
			continue
		}

		resp, err := fwd.Post(req)
		if err != nil {
			slog.Warn("proxyadapter: post failure", "error", err, "request_id", obs.RequestID)
			stats.Dropped++
			continue
		}
		stats.Forwarded++
		slog.Debug("proxyadapter: forwarded",
			"request_id", obs.RequestID,
			"agentshield_action", resp.Action,
			"proxy_decision", obs.Decision)
	}

	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return stats, err
	}
	return stats, nil
}

// bytesTrimSpace is a no-alloc TrimSpace for the hot-path emptiness check.
// (bufio.Scanner returns the same backing buffer each scan; we don't want
// to allocate a string just to test for whitespace.)
func bytesTrimSpace(b []byte) []byte {
	start := 0
	for start < len(b) && isSpace(b[start]) {
		start++
	}
	end := len(b)
	for end > start && isSpace(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}
