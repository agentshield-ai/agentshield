package server

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/otlp"
	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Audit log: every mutating request (any outcome), every denied request,
// and sign-in/sign-out events are recorded with who, when, method, path,
// project, object and span ids and outcome. Entries are written in the
// background in batches and purged after the retention period. Request
// bodies, headers and secrets are never recorded.

func init() {
	routeExtensions = append(routeExtensions, func(s *Server, r chi.Router) {
		r.Post("/audit/query", handleIAM(s.auditQuery))
		r.Post("/audit/export", s.auditExport)
	})
}

// auditNote collects what the auth layer and handlers learn about a request.
type auditNote struct {
	actor      string
	authMethod string
	keyID      *string
	projects   []string
	detail     string
}

type auditNoteKey struct{}

func auditNoteFrom(r *http.Request) *auditNote {
	n, _ := r.Context().Value(auditNoteKey{}).(*auditNote)
	return n
}

// auditDetail adds a human-readable description of an admin action.
func auditDetail(r *http.Request, format string, args ...any) {
	if n := auditNoteFrom(r); n != nil {
		d := fmt.Sprintf(format, args...)
		if len(d) > 500 {
			d = d[:500]
		}
		n.detail = d
	}
}

// maxAuditBody bounds the body the audit layer parses for ids.
const maxAuditBody = 8 << 20

// auditLog wraps authenticated routes; it must run before auth.
func (s *Server) auditLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		note := &auditNote{}
		r = r.WithContext(context.WithValue(r.Context(), auditNoteKey{}, note))
		mutating := !isReadRequest(r)
		var body []byte
		if mutating {
			body = peekJSONBody(r)
		}
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}
		if !mutating && status != http.StatusForbidden && note.detail == "" {
			return
		}
		e := store.AuditEntry{Actor: note.actor, AuthMethod: note.authMethod, KeyID: note.keyID, Method: r.Method,
			Path: r.URL.Path, Status: status, Outcome: auditOutcome(status), IP: clientIP(r), Detail: note.detail}
		if e.AuthMethod == "" {
			e.AuthMethod = "unauthenticated"
		}
		if e.Actor == "" {
			e.Actor = "anonymous"
		}
		pids := note.projects
		if len(pids) == 0 {
			pids = auditProjects(r, body)
		}
		if len(pids) > 0 {
			e.Project = &pids[0]
		}
		e.ObjectIDs, e.SpanIDs = auditIDs(r.URL.Path, body)
		s.recordAudit(e)
	})
}

func auditOutcome(status int) string {
	switch {
	case status < 400:
		return "success"
	case status == http.StatusUnauthorized:
		return "unauthenticated"
	case status == http.StatusForbidden:
		return "denied"
	}
	return "failed"
}

func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// peekJSONBody reads (and restores) a JSON request body of bounded size.
// OTLP, multipart and oversized bodies are left alone.
func peekJSONBody(r *http.Request) []byte {
	if r.Body == nil || r.URL.Path == "/otel/v1/traces" || r.ContentLength > maxAuditBody {
		return nil
	}
	ct := r.Header.Get("Content-Type")
	if ct != "" && !strings.Contains(ct, "json") {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxAuditBody+1))
	if err != nil {
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(raw), errReader{err}))
		return nil
	}
	if len(raw) > maxAuditBody {
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(raw), r.Body))
		return nil
	}
	body, err := otlp.ReadBody(bytes.NewReader(raw), r.Header.Get("Content-Encoding"))
	if err != nil {
		r.Body = io.NopCloser(bytes.NewReader(raw))
		return nil
	}
	r.Header.Del("Content-Encoding")
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func auditProjects(r *http.Request, body []byte) []string {
	var raw []string
	add := func(v string) {
		if v != "" {
			raw = append(raw, v)
		}
	}
	add(r.URL.Query().Get("project_id"))
	if r.URL.Path == "/otel/v1/traces" {
		add(r.Header.Get("project_id"))
		add(r.Header.Get("X-Weft-Project"))
	}
	if parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/"); len(parts) >= 3 && parts[0] == "v2" {
		add(parts[1] + "/" + parts[2])
	}
	if body != nil {
		probeBody(body, add)
	}
	out := []string{}
	seen := map[string]bool{}
	for _, v := range raw {
		if pid, err := refs.NormalizeProject(v); err == nil && !seen[pid] {
			seen[pid] = true
			out = append(out, pid)
		}
	}
	return out
}

const maxAuditIDs = 50

// auditIDs extracts object and span ids from a JSON body: Weft's own
// fields, and the older route shapes' (call_id, call_ids, start/end, batch),
// which the compatibility adapter serves.
func auditIDs(path string, body []byte) (objs, spans []string) {
	if len(body) == 0 {
		return nil, nil
	}
	var top map[string]any
	if json.Unmarshal(body, &top) != nil {
		return nil, nil
	}
	seenO, seenC := map[string]bool{}, map[string]bool{}
	addO := func(v string) {
		if v != "" && !seenO[v] && len(objs) < maxAuditIDs && len(v) <= 256 {
			seenO[v] = true
			objs = append(objs, v)
		}
	}
	addC := func(v string) {
		if v != "" && !seenC[v] && len(spans) < maxAuditIDs && len(v) <= 256 {
			seenC[v] = true
			spans = append(spans, v)
		}
	}
	str := func(m map[string]any, k string) string {
		v, _ := m[k].(string)
		return v
	}
	addRef := func(v string) {
		if !refs.IsRef(v) {
			return
		}
		rr, err := refs.Parse(v)
		if err != nil {
			return
		}
		switch rr.Kind {
		case refs.KindSpan:
			addC(rr.Name)
		case refs.KindObject, refs.KindFunction:
			addO(rr.Name)
		}
	}
	callish := strings.HasPrefix(path, "/call") || strings.Contains(path, "/calls/")
	walkCall := func(m map[string]any) {
		if m == nil {
			return
		}
		for _, k := range []string{"start", "end"} {
			if x, ok := m[k].(map[string]any); ok {
				addC(str(x, "id"))
			}
		}
	}
	walkCall(top)
	if callish {
		addC(str(top, "id"))
	}
	for _, k := range []string{"span_id", "call_id"} {
		addC(str(top, k))
	}
	for _, k := range []string{"span_ids", "call_ids"} {
		if ids, ok := top[k].([]any); ok {
			for _, x := range ids {
				if v, ok := x.(string); ok {
					addC(v)
				}
			}
		}
	}
	if b, ok := top["batch"].([]any); ok {
		for _, it := range b {
			m, _ := it.(map[string]any)
			if req, ok := m["req"].(map[string]any); ok {
				walkCall(req)
			}
			if callish {
				addC(str(m, "id"))
			}
			addRef(str(m, "ref"))
		}
	}
	addO(str(top, "object_id"))
	if o, ok := top["obj"].(map[string]any); ok {
		addO(str(o, "object_id"))
	}
	// Weft's own API: spans by id, ids and batch items; objects by name.
	if strings.HasPrefix(path, "/api/v1/spans/") {
		addC(str(top, "id"))
		if ids, ok := top["ids"].([]any); ok {
			for _, x := range ids {
				if v, ok := x.(string); ok {
					addC(v)
				}
			}
		}
		if items, ok := top["items"].([]any); ok {
			for _, it := range items {
				m, _ := it.(map[string]any)
				for _, k := range []string{"start", "end", "span"} {
					if x, ok := m[k].(map[string]any); ok {
						addC(str(x, "id"))
					}
				}
			}
		}
	}
	if rest, ok := strings.CutPrefix(path, "/api/v1/objects/"); ok {
		addO(str(top, "name"))
		if name, _, _ := strings.Cut(rest, "/"); name != "" && name != "save" && name != "query" && name != "delete" &&
			name != "tags" && name != "aliases" {
			addO(name)
		}
	}
	for k, v := range top {
		switch x := v.(type) {
		case string:
			if k == "ref" || strings.HasSuffix(k, "_ref") || k == "ref" {
				addRef(x)
			}
		case []any:
			if k == "refs" || strings.HasSuffix(k, "_refs") {
				for _, y := range x {
					if v, ok := y.(string); ok {
						addRef(v)
					}
				}
			}
		}
	}
	return objs, spans
}

// ---------------------------------------------------------------- writer

type auditWriter struct {
	s       *Server
	ch      chan store.AuditEntry
	flushCh chan chan struct{}
	done    chan struct{}
	once    sync.Once
}

func newAuditWriter(s *Server) *auditWriter {
	a := &auditWriter{s: s, ch: make(chan store.AuditEntry, 4096), flushCh: make(chan chan struct{}), done: make(chan struct{})}
	go a.run()
	return a
}

func (a *auditWriter) run() {
	defer close(a.done)
	var buf []store.AuditEntry
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	write := func() {
		if len(buf) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := a.s.store.InsertAudit(ctx, buf); err != nil {
			a.s.log.Error("writing audit log", "err", err, "entries", len(buf))
		}
		cancel()
		buf = buf[:0]
	}
	for {
		select {
		case e, ok := <-a.ch:
			if !ok {
				write()
				return
			}
			buf = append(buf, e)
			if len(buf) >= 256 {
				write()
			}
		case <-tick.C:
			write()
		case reply := <-a.flushCh:
			for drained := false; !drained; {
				select {
				case e := <-a.ch:
					buf = append(buf, e)
				default:
					drained = true
				}
			}
			write()
			close(reply)
		}
	}
}

func (a *auditWriter) flush() {
	reply := make(chan struct{})
	select {
	case a.flushCh <- reply:
		<-reply
	case <-a.done:
	}
}

func (a *auditWriter) close() {
	a.once.Do(func() {
		close(a.ch)
		<-a.done
	})
}

// recordAudit queues an entry (blocking when the queue is full, so entries
// are not dropped).
func (s *Server) recordAudit(e store.AuditEntry) {
	e.TimeMicros = time.Now().UnixMicro()
	st := s.iam()
	if st.audit == nil {
		return
	}
	defer func() { _ = recover() }() // the writer closed during shutdown
	st.audit.ch <- e
}

// FlushAudit writes queued audit entries (tests, shutdown).
func (s *Server) FlushAudit() {
	if a := s.iam().audit; a != nil {
		a.flush()
	}
}

// ---------------------------------------------------------------- retention

func (s *Server) auditRetention() time.Duration {
	d := s.cfg.IAM.AuditRetentionDays
	if d == 0 {
		d = 90
	}
	if d < 0 {
		return 0
	}
	return time.Duration(d) * 24 * time.Hour
}

// PurgeAudit deletes audit entries past retention and dead sessions.
func (s *Server) PurgeAudit(ctx context.Context) (int64, error) {
	if err := s.store.PurgeSessions(ctx, time.Now().Add(-24*time.Hour).UnixMicro()); err != nil {
		return 0, err
	}
	keep := s.auditRetention()
	if keep == 0 {
		return 0, nil
	}
	return s.store.PurgeAudit(ctx, time.Now().Add(-keep).UnixMicro())
}

func startAuditRetention(s *Server) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		first := time.NewTimer(45 * time.Second)
		defer first.Stop()
		tick := time.NewTicker(retentionInterval())
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-first.C:
			case <-tick.C:
			}
			if n, err := s.PurgeAudit(ctx); err != nil && ctx.Err() == nil {
				s.log.Error("audit log purge failed", "err", err)
			} else if n > 0 {
				s.log.Info("audit log purge", "entries", n)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// ---------------------------------------------------------------- query

type auditQueryReq struct {
	ProjectID  string `json:"project_id"`
	Actor      string `json:"actor"`
	Method     string `json:"method"`
	PathPrefix string `json:"path_prefix"`
	Outcome    string `json:"outcome"`
	Since      string `json:"since"`
	Until      string `json:"until"`
	Limit      int    `json:"limit"`
	Offset     int    `json:"offset"`
}

// auditFilter checks access and builds the store filter. Server admins see
// everything; project admins see entries of a project they administer.
func (s *Server) auditFilter(r *http.Request, req *auditQueryReq) (store.AuditFilter, error) {
	f := store.AuditFilter{Actor: req.Actor, Method: req.Method, PathPrefix: req.PathPrefix, Outcome: req.Outcome, Limit: req.Limit, Offset: req.Offset}
	if req.ProjectID != "" {
		pid, err := project(req.ProjectID)
		if err != nil {
			return f, err
		}
		f.Projects = []string{pid}
		if callerRole(r, pid) != store.RoleAdmin {
			return f, forbidden("reading the audit log of %s needs the admin role on it", pid)
		}
	} else if !callerIsAdmin(r) {
		return f, forbidden("reading the whole audit log needs a server admin; pass project_id for one project")
	}
	for _, x := range []struct {
		in  string
		out *int64
	}{{req.Since, &f.Since}, {req.Until, &f.Until}} {
		if x.in != "" {
			v, err := store.ParseTime(x.in)
			if err != nil {
				return f, err
			}
			*x.out = v
		}
	}
	return f, nil
}

func (s *Server) auditQuery(ctx context.Context, r *http.Request, req *auditQueryReq) (any, error) {
	f, err := s.auditFilter(r, req)
	if err != nil {
		return nil, err
	}
	s.FlushAudit()
	entries, total, err := s.store.QueryAudit(ctx, f)
	if err != nil {
		return nil, err
	}
	return map[string]any{"entries": entries, "total_count": total, "retention_days": int(s.auditRetention().Hours() / 24)}, nil
}

// auditExport returns matching entries (up to 10000) as CSV.
func (s *Server) auditExport(w http.ResponseWriter, r *http.Request) {
	var req auditQueryReq
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	f, err := s.auditFilter(r, &req)
	if fe, ok := err.(*forbiddenError); ok {
		writeError(w, http.StatusForbidden, fe.msg)
		return
	} else if err != nil {
		writeErr(w, err)
		return
	}
	if f.Limit <= 0 || f.Limit > 10000 {
		f.Limit = 10000
	}
	s.FlushAudit()
	entries, _, err := s.store.QueryAudit(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="weft-audit.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"id", "ts", "actor", "auth_method", "key_id", "method", "path", "project_id", "object_ids", "span_ids", "status", "outcome", "ip", "detail"})
	for _, e := range entries {
		key, proj := "", ""
		if e.KeyID != nil {
			key = *e.KeyID
		}
		if e.Project != nil {
			proj = *e.Project
		}
		_ = cw.Write([]string{strconv.FormatInt(e.ID, 10), e.Time, csvSafe(e.Actor), e.AuthMethod, key, e.Method, csvSafe(e.Path), csvSafe(proj),
			csvSafe(strings.Join(e.ObjectIDs, " ")), csvSafe(strings.Join(e.SpanIDs, " ")), strconv.Itoa(e.Status), e.Outcome, e.IP, csvSafe(e.Detail)})
	}
	cw.Flush()
}

// csvSafe defuses spreadsheet formula injection in user-controlled cells.
func csvSafe(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}
