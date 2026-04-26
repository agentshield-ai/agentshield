package proxyadapter

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/agentshield-ai/agentshield/internal/models"
)

// ErrInvalid signals an Observation that's missing required fields. The
// caller should log + skip; the source loop continues with the next line.
var ErrInvalid = errors.New("invalid observation")

// Normalize maps an Observation into an AgentShield evaluation request with
// stable field names (`proxy.source`, `proxy.verdict`, etc.) so Sigma rules
// can match on either the ingested URL fields (populated server-side by the
// existing url.* enrichment from #33) or the upstream proxy's own decision.
//
// `source` distinguishes which proxy the observation came from
// (`crabtrap`, `mitmproxy`, etc.) and is set via the adapter's --source flag.
func Normalize(obs Observation, source string) (*models.EvaluationRequest, error) {
	if obs.RequestID == "" {
		return nil, fmt.Errorf("%w: missing request_id", ErrInvalid)
	}
	if obs.URL == "" {
		return nil, fmt.Errorf("%w: missing url", ErrInvalid)
	}
	if obs.Method == "" {
		return nil, fmt.Errorf("%w: missing method", ErrInvalid)
	}

	sessionID := obs.SessionID
	if sessionID == "" && obs.SrcIP != "" {
		// Best-effort fallback so per-client proxy-observations cluster in the
		// same session window. Operators wanting strict tool-call ↔ proxy
		// correlation should arrange for an upstream session header.
		sessionID = "proxy-" + source + "-" + obs.SrcIP
	}

	args := map[string]string{
		"url":    obs.URL,
		"method": obs.Method,
	}

	fields := map[string]string{
		"event_type":   "http_egress",
		"tool":         "http_egress",
		"command":      obs.Method + " " + obs.URL,
		"source":       source,
		"proxy.source": source,
	}
	if obs.Decision != "" {
		fields["proxy.verdict"] = obs.Decision
	}
	if obs.DecidedBy != "" {
		fields["proxy.decided_by"] = obs.DecidedBy
	}
	if obs.Status != 0 {
		fields["proxy.status"] = strconv.Itoa(obs.Status)
	}
	if obs.DurationMs != 0 {
		fields["proxy.duration_ms"] = strconv.FormatInt(obs.DurationMs, 10)
	}
	if obs.User != "" {
		fields["proxy.user"] = obs.User
	}

	return &models.EvaluationRequest{
		EventID:   obs.RequestID,
		EventType: "http_egress",
		SessionID: sessionID,
		Tool:      "http_egress",
		Source:    source,
		Args:      args,
		Fields:    fields,
	}, nil
}
