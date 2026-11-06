// Package compat is Weft's optional adapter for clients of the W&B Weave
// product (the official `weave` Python client and links printed by it). It
// is off by default and turned on with --weave-compat (WEFT_WEAVE_COMPAT=true).
//
// With the adapter on, the server additionally:
//
//   - answers the W&B GraphQL login calls weave.init() makes (/graphql);
//   - serves the client's v2 call routes (/v2/{entity}/{project}/calls/complete,
//     .../call/start, .../call/end) and /service/projects_info;
//   - serves the route shapes Weft had before its own API (/call/*,
//     /calls/*, /obj/*, /table/*, /feedback/*, ...; routes.go), computing
//     digests in the client's form and naming function and span refs with
//     its kinds ("op", "call");
//   - accepts weave:/// refs and weave-file:// URIs in requests, the legacy
//     feedback names (weave_ref, wandb.reaction.1, wandb.runnable.<scorer>,
//     ...), the client's user and run field names (wb_user_id, wb_run_id,
//     wb_run_step, wb_run_step_end, wb_user_ids, wb_run_ids), its query
//     fields (summary.weave.*, feedback.wandb.*) and the Wandb-Api-Key
//     header;
//   - renames legacy attribute keys (weave.eval.*, weave.parent_call.*,
//     wandb.thread_id, ...) on ingest, over JSON and OTLP;
//   - answers responses to the weave client (detected by its user agent) with
//     weave:/// refs, legacy feedback names and its field names (wb_user_id,
//     wb_username, wb_run_id, wb_run_step, wb_run_step_end);
//   - redirects /{entity}/{project}/weave/... and /{entity}/{project}/r/call/{id}
//     links to the matching UI pages;
//   - reports the client's version fields in /server_info (served by
//     routes.go, like GET /version, /geolocate and /inference/*);
//   - reads WF_* environment variables as aliases of Weft's settings.
//
// With it off, none of this is registered: the routes return 404 and
// nothing else changes. Nothing outside this package depends on it. See
// docs/weft/COMPATIBILITY.md.
package compat

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/compat/legacy"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
)

// FlagName and EnvName turn the adapter on.
const (
	FlagName = "weave-compat"
	EnvName  = "WEFT_WEAVE_COMPAT"
)

// Adapter is the compatibility adapter for one server.
type Adapter struct {
	host Host
}

// New builds the adapter and installs its ingest hooks on the host's store.
func New(h Host) *Adapter {
	a := &Adapter{host: h}
	h.Store().SetIngestHooks(IngestHooks())
	return a
}

// IngestHooks rename legacy attribute keys of incoming calls and recognise
// ops serialised by the weave client.
func IngestHooks() store.IngestHooks {
	return store.IngestHooks{
		Attributes: func(m map[string]any) { legacy.RewriteAttributes(m) },
		IsOp: func(m map[string]any) bool {
			wt, ok := m["weave_type"].(map[string]any)
			return ok && m["_type"] == "CustomWeaveType" && wt["type"] == "Op"
		},
	}
}

// ServerInfo adds the fields the weave client reads from /server_info.
func (a *Adapter) ServerInfo(m map[string]any) {
	m["min_required_weave_python_version"] = "0.0.0"
	m["weave_compat"] = true
}

// MountPublic registers the unauthenticated routes: UI link redirects.
func (a *Adapter) MountPublic(r chi.Router) {
	r.Get("/{entity}/{project}/weave", a.linkRedirect)
	r.Get("/{entity}/{project}/weave/*", a.linkRedirect)
	r.Get("/{entity}/{project}/r/call/{callID}", a.callRedirect)
}

// MountAuthed registers the authenticated routes: the client's login and
// call routes, and the older route shapes (Routes).
func (a *Adapter) MountAuthed(r chi.Router, routes *Routes) {
	r.Post("/graphql", a.graphql)
	r.Group(func(r chi.Router) {
		r.Use(legacyResponses)
		r.Post("/v2/{entity}/{project}/calls/complete", a.callsComplete)
		r.Post("/v2/{entity}/{project}/call/start", handle(routes.callStart))
		r.Post("/v2/{entity}/{project}/call/end", handle(routes.callEnd))
	})
	r.Post("/service/projects_info", a.projectsInfo)
}

// ---------------------------------------------------------------- middleware

// maxRewrite caps the request bodies the adapter rewrites; larger bodies
// pass through untouched (the server rejects JSON bodies above 64 MiB).
const maxRewrite = 64 << 20

// Middleware translates requests from legacy names to Weft's, and responses
// to the weave client back to the names it understands.
func (a *Adapter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if k := r.Header.Get("Wandb-Api-Key"); k != "" && r.Header.Get("X-Weft-Api-Key") == "" {
			r.Header.Set("X-Weft-Api-Key", k)
		}
		if NativePath(r.URL.Path) {
			// Weft's own API keeps its contract whatever the client: only
			// the adapter's routes are translated.
			next.ServeHTTP(w, r)
			return
		}
		rewriteQuery(r)
		if err := rewriteRequestBody(r); err != nil {
			http.Error(w, "reading body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if !IsWeaveClient(r) {
			next.ServeHTTP(w, r)
			return
		}
		// The client's call summaries already include their descendants.
		r.Header.Set("X-Weft-Summary", "cumulative")
		rw := &bufferedWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		body := rw.buf
		if strings.Contains(w.Header().Get("Content-Type"), "json") {
			body = rewriteResponse(r.URL.Path, body)
		}
		w.Header().Del("Content-Length")
		w.WriteHeader(rw.status)
		_, _ = w.Write(body)
	})
}

// NativePath reports paths of Weft's own API (/api/...), which the adapter
// never translates.
func NativePath(p string) bool { return p == "/api" || strings.HasPrefix(p, "/api/") }

// IsWeaveClient reports whether the caller is the official weave client,
// which only understands weave:/// refs and its own feedback names.
func IsWeaveClient(r *http.Request) bool {
	if strings.EqualFold(r.Header.Get("X-Weft-Ref-Scheme"), "weave") {
		return true
	}
	if r.Header.Get("X-Stainless-Lang") != "" {
		return true
	}
	ua := r.Header.Get("User-Agent")
	return strings.HasPrefix(ua, "WeaveTrace/") || strings.HasPrefix(ua, "weave/")
}

// bufferedWriter buffers a response so it can be rewritten.
type bufferedWriter struct {
	http.ResponseWriter
	buf    []byte
	status int
}

func (w *bufferedWriter) WriteHeader(code int) { w.status = code }
func (w *bufferedWriter) Write(b []byte) (int, error) {
	w.buf = append(w.buf, b...)
	return len(b), nil
}
func (w *bufferedWriter) Flush() {}

// Unwrap returns the wrapped writer (the request log finds its record there).
func (w *bufferedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func rewriteQuery(r *http.Request) {
	if !strings.Contains(r.URL.RawQuery, "weave") {
		return
	}
	q := r.URL.Query()
	for k, vs := range q {
		for i, v := range vs {
			vs[i] = inScheme(v)
		}
		q[k] = vs
	}
	r.URL.RawQuery = q.Encode()
}

func inScheme(s string) string {
	if rest, ok := strings.CutPrefix(s, legacy.RefScheme); ok {
		return "weft:///" + rest
	}
	if rest, ok := strings.CutPrefix(s, legacy.FileScheme); ok {
		return "weft-file://" + rest
	}
	return s
}

// feedbackPath reports request paths whose bodies carry feedback fields.
func feedbackPath(p string) bool {
	return strings.HasPrefix(p, "/feedback/") || strings.HasPrefix(p, "/agents/feedback/")
}

// agentsPath reports the /agents/* API, whose queries name attribute keys.
func agentsPath(p string) bool { return strings.HasPrefix(p, "/agents/") }

func rewriteRequestBody(r *http.Request) error {
	if r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	if ce := r.Header.Get("Content-Encoding"); ce != "" && !strings.EqualFold(ce, "identity") {
		return nil
	}
	ct := r.Header.Get("Content-Type")
	if ct != "" && !strings.Contains(ct, "json") {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRewrite+1))
	if err != nil {
		return err
	}
	if len(body) > maxRewrite {
		r.Body = readCloser{io.MultiReader(bytes.NewReader(body), r.Body), r.Body}
		return nil
	}
	_ = r.Body.Close()
	body = RewriteRequest(r.URL.Path, body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Del("Content-Length")
	return nil
}

type readCloser struct {
	io.Reader
	io.Closer
}

// RewriteRequest translates a JSON request body from legacy names to Weft's.
// It works on the bytes so that the publisher's key order is kept (stored
// objects keep it).
func RewriteRequest(path string, body []byte) []byte {
	if !bytes.Contains(body, []byte("weave")) && !bytes.Contains(body, []byte("wandb.")) && !bytes.Contains(body, []byte("wb_")) {
		return body
	}
	if bytes.Contains(body, []byte("wb_")) || bytes.Contains(body, []byte(".weave.")) || bytes.Contains(body, []byte("feedback.wandb.")) {
		// Field names and query fields, outside user data. An object being
		// published may be a saved view, whose query is in its value.
		objCreate := path == "/obj/create"
		body = legacy.JSONRewrite{Key: legacy.QueryFieldName, Value: legacy.QueryFieldName, Skip: func(k string) bool {
			return dataKeys[k] && !(objCreate && k == "val")
		}}.Apply(body)
	}
	body = bytes.ReplaceAll(body, []byte(`"`+legacy.RefScheme), []byte(`"weft:///`))
	body = bytes.ReplaceAll(body, []byte(`"`+legacy.FileScheme), []byte(`"weft-file://`))
	if feedbackPath(path) {
		body = bytes.ReplaceAll(body, []byte(`"`+legacy.FeedbackRefColumn+`"`), []byte(`"ref"`))
		body = rewriteFeedbackTypes(body, legacy.FeedbackType)
	}
	if agentsPath(path) {
		body = bytes.ReplaceAll(body, []byte(`"weave.parent_call.`), []byte(`"weft.parent_span.`))
	}
	return body
}

// rewriteFeedbackTypes maps every JSON string value that is a feedback type.
func rewriteFeedbackTypes(body []byte, mapType func(string) string) []byte {
	if !bytes.Contains(body, []byte(`"feedback_type"`)) && !bytes.Contains(body, []byte(`"feedback_types"`)) &&
		!bytes.Contains(body, []byte("wandb.")) && !bytes.Contains(body, []byte("weft.")) {
		return body
	}
	var out bytes.Buffer
	out.Grow(len(body))
	i := 0
	for i < len(body) {
		if body[i] != '"' {
			out.WriteByte(body[i])
			i++
			continue
		}
		j := i + 1
		for j < len(body) && body[j] != '"' {
			if body[j] == '\\' {
				j++
			}
			j++
		}
		if j >= len(body) {
			out.Write(body[i:])
			break
		}
		lit := body[i : j+1]
		var s string
		if bytes.IndexByte(lit, '\\') < 0 {
			s = string(lit[1 : len(lit)-1])
			if n := mapType(s); n != s {
				b, _ := json.Marshal(n)
				out.Write(b)
				i = j + 1
				continue
			}
		}
		out.Write(lit)
		i = j + 1
	}
	return out.Bytes()
}

// dataKeys hold user data, whose keys and values are never renamed.
var dataKeys = map[string]bool{"inputs": true, "output": true, "attributes": true, "summary": true, "payload": true, "val": true,
	"rows": true, "row": true, "rollup": true}

// recordPath reports responses that carry calls, feedback or objects.
func recordPath(p string) bool {
	for _, pre := range []string{"/call/", "/calls/", "/feedback/", "/obj/", "/objs/", "/threads/", "/evaluations/"} {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

// rewriteResponse translates a JSON response to the weave client.
func rewriteResponse(path string, body []byte) []byte {
	body = legacy.RefKindsOut(body) // the client knows only its own ref kinds
	body = bytes.ReplaceAll(body, []byte("weft:///"), []byte(legacy.RefScheme))
	if recordPath(path) {
		body = legacy.JSONRewrite{Key: legacy.FieldNameOut, Skip: func(k string) bool { return dataKeys[k] }}.Apply(body)
	}
	if agentsPath(path) {
		// The client's agent span fields for the weft.parent_span.* link.
		body = bytes.ReplaceAll(body, []byte(`"linked_parent_span_id":`), []byte(`"parent_call_id":`))
		body = bytes.ReplaceAll(body, []byte(`"linked_parent_trace_id":`), []byte(`"parent_call_trace_id":`))
	}
	if strings.HasPrefix(path, "/completions/") || path == "/image/create" {
		body = bytes.ReplaceAll(body, []byte(`"call_id":`), []byte(`"weave_call_id":`))
	}
	if feedbackPath(path) || strings.HasPrefix(path, "/calls/") || strings.HasPrefix(path, "/call/") {
		body = bytes.ReplaceAll(body, []byte(`"ref":"`+legacy.RefScheme), []byte(`"`+legacy.FeedbackRefColumn+`":"`+legacy.RefScheme))
		body = rewriteFeedbackTypes(body, func(s string) string {
			if strings.HasPrefix(s, "weft.") {
				return legacy.FeedbackTypeOut(s)
			}
			return s
		})
	}
	return body
}

// ---------------------------------------------------------------- links

// sections are the UI sections a /weave/<section>/... link maps onto.
var sections = map[string]bool{"calls": true, "objects": true, "evaluations": true, "leaderboards": true, "threads": true,
	"agents": true, "datasets": true, "models": true, "prompts": true, "scorers": true, "ops": true, "monitors": true,
	"playground": true, "traces": true}

// LinkTarget maps the path after /{entity}/{project}/weave/ to a UI path.
func LinkTarget(entity, project, rest string) string {
	base := "/" + url.PathEscape(entity) + "/" + url.PathEscape(project)
	p := strings.Trim(rest, "/")
	if c, ok := strings.CutPrefix(p, "agents/conversations/"); ok && c != "" {
		return base + "/conversations/" + c
	}
	head, _, _ := strings.Cut(p, "/")
	if sections[head] {
		return base + "/" + p
	}
	return base + "/traces"
}

func (a *Adapter) linkRedirect(w http.ResponseWriter, r *http.Request) {
	to := LinkTarget(chi.URLParam(r, "entity"), chi.URLParam(r, "project"), chi.URLParam(r, "*"))
	if r.URL.RawQuery != "" {
		to += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, to, http.StatusFound)
}

func (a *Adapter) callRedirect(w http.ResponseWriter, r *http.Request) {
	to := "/" + url.PathEscape(chi.URLParam(r, "entity")) + "/" + url.PathEscape(chi.URLParam(r, "project")) +
		"/calls/" + url.PathEscape(chi.URLParam(r, "callID"))
	http.Redirect(w, r, to, http.StatusFound)
}
