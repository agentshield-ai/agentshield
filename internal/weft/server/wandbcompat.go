package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

type (
	storeCallStart    = store.CallStart
	storeCallEnd      = store.CallEnd
	storeCompleteCall = store.CompleteCall
)

var chiURLParam = chi.URLParam

// W&B compatibility for the official `weave` Python client. weave.init()
// talks to the W&B GraphQL API (WANDB_BASE_URL/graphql) to resolve the
// default entity and create the project, then sends traces to the trace
// server (WF_TRACE_SERVER_URL). Pointing both at Weft makes the stock client
// work. Only the handful of operations weave issues are implemented.

type graphqlReq struct {
	Query         string         `json:"query"`
	Variables     map[string]any `json:"variables"`
	OperationName string         `json:"operationName"`
}

func (s *Server) graphql(w http.ResponseWriter, r *http.Request) {
	var req graphqlReq
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	user := "default"
	if u := userFrom(r, nil); u != nil {
		user = *u
	}
	q := req.Query
	data := map[string]any{}
	handled := false
	str := func(k string) string { v, _ := req.Variables[k].(string); return v }

	if strings.Contains(q, "serverInfo") {
		scheme := "http"
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			scheme = "https"
		}
		data["serverInfo"] = map[string]any{"frontendHost": scheme + "://" + r.Host}
		handled = true
	}
	if strings.Contains(q, "viewer") {
		data["viewer"] = map[string]any{"username": user, "defaultEntity": map[string]any{"name": user}}
		handled = true
	}
	if strings.Contains(q, "entity(") {
		data["entity"] = map[string]any{"name": str("entityName"), "readOnly": false}
		handled = true
	}
	if strings.Contains(q, "upsertModel") {
		ent, name := str("entity"), str("name")
		if ent == "" {
			ent = user
		}
		pid, err := refs.NormalizeProject(ent + "/" + name)
		if err != nil {
			writeGraphQLError(w, err.Error())
			return
		}
		if err := s.store.EnsureProject(r.Context(), pid); err != nil {
			writeErr(w, err)
			return
		}
		data["upsertModel"] = map[string]any{"model": map[string]any{"name": name, "description": req.Variables["description"]}, "inserted": true}
		handled = true
	}
	if strings.Contains(q, "project(") && !strings.Contains(q, "upsertModel") {
		data["project"] = map[string]any{"name": str("name"), "entityName": str("entityName")}
		handled = true
	}
	if !handled {
		writeGraphQLError(w, "operation not supported by Weft's W&B compatibility endpoint")
		return
	}
	writeJSON(w, map[string]any{"data": data})
}

func writeGraphQLError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "errors": []any{map[string]any{"message": msg}}})
}

// wantsWeaveRefs reports whether the caller is the official weave client,
// which only understands weave:/// refs.
func wantsWeaveRefs(r *http.Request) bool {
	if strings.EqualFold(r.Header.Get("X-Weft-Ref-Scheme"), "weave") {
		return true
	}
	if r.Header.Get("X-Stainless-Lang") != "" {
		return true
	}
	ua := r.Header.Get("User-Agent")
	return strings.HasPrefix(ua, "WeaveTrace/") || strings.HasPrefix(ua, "weave/")
}

// refSchemeWriter buffers a response and rewrites weft:/// refs to weave:///.
type refSchemeWriter struct {
	http.ResponseWriter
	buf    []byte
	status int
}

func (w *refSchemeWriter) WriteHeader(code int) { w.status = code }
func (w *refSchemeWriter) Write(b []byte) (int, error) {
	w.buf = append(w.buf, b...)
	return len(b), nil
}
func (w *refSchemeWriter) Flush() {}

// weaveCompat rewrites ref schemes in JSON responses for weave clients.
// Requests need no rewriting: weave:/// is accepted everywhere.
func weaveCompat(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !wantsWeaveRefs(r) {
			next.ServeHTTP(w, r)
			return
		}
		rw := &refSchemeWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		body := rw.buf
		ct := w.Header().Get("Content-Type")
		if strings.Contains(ct, "json") {
			body = []byte(strings.ReplaceAll(string(body), "weft:///", "weave:///"))
		}
		w.WriteHeader(rw.status)
		_, _ = w.Write(body)
	})
}

// completeCall is one item of POST /v2/{entity}/{project}/calls/complete.
type completeCall struct {
	ID          string         `json:"id"`
	ProjectID   string         `json:"project_id"`
	OpName      string         `json:"op_name"`
	DisplayName *string        `json:"display_name"`
	TraceID     string         `json:"trace_id"`
	ParentID    *string        `json:"parent_id"`
	ThreadID    *string        `json:"thread_id"`
	TurnID      *string        `json:"turn_id"`
	StartedAt   string         `json:"started_at"`
	EndedAt     string         `json:"ended_at"`
	Attributes  map[string]any `json:"attributes"`
	Inputs      map[string]any `json:"inputs"`
	Output      any            `json:"output"`
	Summary     map[string]any `json:"summary"`
	Exception   *string        `json:"exception"`
	WBUserID    *string        `json:"wb_user_id"`
	WBRunID     *string        `json:"wb_run_id"`
}

// callsComplete ingests finished calls in one request (weave's calls_complete
// write path). Summaries from the weave client already include descendants.
func (s *Server) callsComplete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Batch []completeCall `json:"batch"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	pathProject := chiURLParam(r, "entity") + "/" + chiURLParam(r, "project")
	calls := make([]storeCompleteCall, 0, len(req.Batch))
	for _, c := range req.Batch {
		pid := c.ProjectID
		if pid == "" {
			pid = pathProject
		}
		st := storeCallStart{ProjectID: pid, ID: c.ID, OpName: c.OpName, DisplayName: c.DisplayName, TraceID: c.TraceID,
			ParentID: c.ParentID, ThreadID: c.ThreadID, TurnID: c.TurnID, StartedAt: c.StartedAt, Attributes: c.Attributes,
			Inputs: c.Inputs, WBUserID: userFrom(r, c.WBUserID), WBRunID: c.WBRunID}
		en := &storeCallEnd{ProjectID: pid, ID: c.ID, EndedAt: c.EndedAt, Exception: c.Exception, Output: c.Output,
			Summary: c.Summary, Cumulative: true}
		calls = append(calls, storeCompleteCall{Start: st, End: en})
	}
	ended, err := s.store.UpsertComplete(r.Context(), calls)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.engine.Notify(ended)
	writeJSON(w, map[string]any{})
}

// projectsInfo maps external project ids to internal ones. Weft uses the
// external "entity/project" id directly, so the mapping is the identity.
func (s *Server) projectsInfo(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectIDs []string `json:"project_ids"`
	}
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]string, 0, len(req.ProjectIDs))
	for _, p := range req.ProjectIDs {
		pid, err := refs.NormalizeProject(p)
		if err != nil {
			continue
		}
		out = append(out, map[string]string{"external_project_id": p, "internal_project_id": pid})
	}
	writeJSON(w, out)
}
