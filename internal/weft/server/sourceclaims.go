package server

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// Session source claims (store/sources.go). A session recorded by several
// producers is shown from its highest-ranked source, so a span claiming a
// ranked source can hide others. Each ingest path may only claim the
// sources it produces:
//
//   - /otel/v1/traces: the harnesses' native trace sources (*-otel-traces),
//     set by the OTLP mapping;
//   - /otel/v1/logs: the native log sources (*-otel-logs), set when spans
//     are rebuilt from log events;
//   - any other route (the span API, the adapter's routes): only a Weft
//     plugin's source (weft-claude-code, weft-codex), and only when the
//     request names it in the X-Weft-Source header, which the plugins send.
//
// A claim a path may not make is kept as weft.claimed_source and does not
// rank. The header is a declaration, not proof: any credential that may
// write to the project can send it. What bounds a false claim is that
// nothing is dropped (outranked spans are stored hidden), only a source
// with content outranks another, and every replacement is logged and
// audited (auditSourceReplaced).

// sourceHeader names the plugin source a request's spans come from.
const sourceHeader = "X-Weft-Source"

// sourceClaims is the ranked sources r may claim.
func sourceClaims(r *http.Request) []string {
	switch strings.TrimSuffix(r.URL.Path, "/") {
	case "/otel/v1/traces", "/agents/otel/v1/traces":
		return store.RankedSources("-otel-traces")
	case "/otel/v1/logs":
		return store.RankedSources("-otel-logs")
	}
	if h := strings.TrimSpace(r.Header.Get(sourceHeader)); store.IsPluginSource(h) {
		return []string{h}
	}
	return nil
}

// ingestContext is r's context with its source claims and the audit hook
// for source replacements.
func (s *Server) ingestContext(r *http.Request) context.Context {
	ctx := store.WithSourceClaims(r.Context(), sourceClaims(r)...)
	note := auditNoteFrom(r)
	actor, method, keyID := "anonymous", "none", (*string)(nil)
	if note != nil {
		if note.actor != "" {
			actor = note.actor
		}
		if note.authMethod != "" {
			method = note.authMethod
		}
		keyID = note.keyID
	}
	ip, path := clientIP(r), r.URL.Path
	return store.WithSourceReplacedHook(ctx, func(rep store.SourceReplaced) {
		froms := make([]string, 0, len(rep.From))
		hidden := 0
		for f, n := range rep.From {
			froms = append(froms, f)
			hidden += n
		}
		sort.Strings(froms)
		pid := rep.Project
		s.recordAudit(store.AuditEntry{Actor: actor, AuthMethod: method, KeyID: keyID, Method: http.MethodPost, Path: path,
			Project: &pid, Status: http.StatusOK, Outcome: "success", IP: ip,
			Detail: fmt.Sprintf("session %s now shown from %s: hid %d span(s) of %s, relinked %d verdict(s)",
				clip(rep.Session, 128), rep.To, hidden, strings.Join(froms, ","), rep.Relinked)})
	})
}
