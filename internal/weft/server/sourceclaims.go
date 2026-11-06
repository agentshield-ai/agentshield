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
// write to the project (member role or a write key) can send it, and one
// span claiming a plugin source, with or without content, outranks the
// session's native spans. What bounds a false claim: nothing is dropped
// (outranked spans are stored hidden, never deleted); every replacement is
// logged and audited (ingestContext below); deleting the claiming spans
// shows the hidden ones again; and a project admin can restore a session,
// which also stops honouring plugin claims for it
// (/api/v1/spans/sessions/sources/restore, store/sources_restore.go).

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

// sessionSourcesReq is the body of /api/v1/spans/sessions/sources and
// /restore.
type sessionSourcesReq struct {
	ProjectID string `json:"project_id"`
	SessionID string `json:"session_id"`
	// IgnorePluginClaims (restore; default true): stop honouring plugin
	// claims for the session and show its hidden spans again; false
	// honours later claims again.
	IgnorePluginClaims *bool `json:"ignore_plugin_claims"`
}

// v1SessionSources lists a session's ranked sources: shown and hidden
// spans of each, and whether plugin claims are honoured.
func (s *Server) v1SessionSources(ctx context.Context, _ *http.Request, req *sessionSourcesReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	return s.store.SessionSourceInfo(ctx, pid, req.SessionID)
}

// v1SessionSourcesRestore is a project admin's restore of a session's
// hidden spans (projectAdminPaths).
func (s *Server) v1SessionSourcesRestore(ctx context.Context, r *http.Request, req *sessionSourcesReq) (any, error) {
	pid, err := project(req.ProjectID)
	if err != nil {
		return nil, err
	}
	ignore := req.IgnorePluginClaims == nil || *req.IgnorePluginClaims
	actor := "anonymous"
	if note := auditNoteFrom(r); note != nil && note.actor != "" {
		actor = note.actor
	}
	out, err := s.store.RestoreSessionSources(ctx, pid, req.SessionID, ignore, actor)
	if err != nil {
		return nil, err
	}
	s.log.Info("session sources restored", "project", pid, "session_id", clip(req.SessionID, 128), "ignore_plugin_claims", ignore,
		"spans_restored", out.Restored, "by", actor)
	return out, nil
}
