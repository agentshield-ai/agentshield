package server

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// newIAMEnv builds a server with a static "root" key and the given IAM config.
func newIAMEnv(t *testing.T, iam IAMConfig) (*testEnv, *Server) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "weft.db"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{APIKeys: []string{"root"}, IAM: iam}, st)
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		hs.Close()
		s.Close()
		_ = st.Close()
	})
	return &testEnv{t: t, srv: hs, key: "root"}, s
}

func (e *testEnv) mustCode(want int, method, path, key string, body any) []byte {
	e.t.Helper()
	code, out := e.admDo(method, path, key, body)
	if code != want {
		e.t.Fatalf("%s %s -> %d (%s), want %d", method, path, code, bytes.TrimSpace(out), want)
	}
	return out
}

func callStart(project, id string) map[string]any {
	return map[string]any{"start": map[string]any{"project_id": project, "id": id, "trace_id": id, "op_name": "op",
		"started_at": time.Now().UTC().Format(time.RFC3339Nano), "inputs": map[string]any{}}}
}

// rbacFixture: users alice (admin of team acme), bob (member of acme),
// carol (viewer of acme, member of acme/special), dave (member of project
// beta/app only), and a key for each.
type rbacFixture struct {
	e                                *testEnv
	s                                *Server
	alice, bob, carol, dave, daveApp string
}

func newRBAC(t *testing.T) *rbacFixture {
	e, s := newIAMEnv(t, IAMConfig{})
	for _, u := range []string{"alice", "bob", "carol", "dave"} {
		e.admJSON("POST", "/users/create", "root", map[string]any{"id": u, "email": u + "@example.com"})
	}
	e.admJSON("POST", "/teams/create", "root", map[string]any{"name": "acme"})
	e.admJSON("POST", "/teams/members/set", "root", map[string]any{"team": "acme", "user_id": "alice", "role": "admin"})
	e.admJSON("POST", "/teams/members/set", "root", map[string]any{"team": "acme", "user_id": "bob", "role": "member"})
	e.admJSON("POST", "/teams/members/set", "root", map[string]any{"team": "acme", "user_id": "carol", "role": "viewer"})
	e.admJSON("POST", "/project/members/set", "root", map[string]any{"project_id": "acme/special", "user_id": "carol", "role": "member"})
	e.admJSON("POST", "/project/members/set", "root", map[string]any{"project_id": "beta/app", "user_id": "dave", "role": "member"})
	f := &rbacFixture{e: e, s: s}
	key := func(user string, extra map[string]any) string {
		body := map[string]any{"name": user + "-key", "user_id": user}
		for k, v := range extra {
			body[k] = v
		}
		sec, k := createKey(e, "root", body)
		if k["user_bound"] != true {
			t.Fatalf("key for %s not bound to the user: %v", user, k)
		}
		return sec
	}
	f.alice, f.bob, f.carol, f.dave = key("alice", nil), key("bob", nil), key("carol", nil), key("dave", nil)
	f.daveApp = key("dave", map[string]any{"projects": []any{"beta/app"}, "name": "dave-app"})
	return f
}

func TestRBACRoles(t *testing.T) {
	f := newRBAC(t)
	e := f.e
	// Members write, viewers read.
	e.mustCode(200, "POST", "/call/start", f.bob, callStart("acme/app", "b1"))
	e.mustCode(200, "POST", "/calls/query", f.carol, map[string]any{"project_id": "acme/app"})
	e.mustCode(403, "POST", "/call/start", f.carol, callStart("acme/app", "c1"))
	e.mustCode(403, "POST", "/obj/create", f.carol, map[string]any{"obj": map[string]any{"project_id": "acme/app", "object_id": "o", "val": 1}})
	e.mustCode(403, "POST", "/feedback/create", f.carol, map[string]any{"project_id": "acme/app", "ref": "weft:///acme/app/call/b1",
		"feedback_type": "weft.reaction", "payload": map[string]any{"emoji": "👍"}})
	e.mustCode(403, "POST", "/calls/delete", f.carol, map[string]any{"project_id": "acme/app", "call_ids": []any{"b1"}})
	// A direct project role is stronger than the team role.
	e.mustCode(200, "POST", "/call/start", f.carol, callStart("acme/special", "c2"))
	// No role: no access at all.
	e.mustCode(403, "POST", "/calls/query", f.dave, map[string]any{"project_id": "acme/app"})
	e.mustCode(403, "GET", "/v2/acme/app/datasets", f.dave, nil)
	e.mustCode(403, "POST", "/refs/read_batch", f.dave, map[string]any{"refs": []any{"weft:///acme/app/object/o:abc"}})
	e.mustCode(200, "POST", "/call/start", f.dave, callStart("beta/app", "d1"))

	// Members cannot use admin endpoints.
	for _, x := range []struct {
		path string
		body map[string]any
	}{
		{"/project/ttl_settings/update", map[string]any{"project_id": "acme/app", "retention_days": 1}},
		{"/project/ingest_sampling_settings/update", map[string]any{"project_id": "acme/app", "sample_rate": 0}},
		{"/project/sensitive_data_settings/update", map[string]any{"project_id": "acme/app", "policy": "pii-v1"}},
		{"/project/delete", map[string]any{"project_id": "acme/app"}},
		{"/project/members/set", map[string]any{"project_id": "acme/app", "user_id": "bob", "role": "admin"}},
		{"/teams/members/set", map[string]any{"team": "acme", "user_id": "bob", "role": "admin"}},
		{"/users/create", map[string]any{"id": "mallory"}},
		{"/users/update", map[string]any{"id": "bob", "admin": true}},
		{"/teams/create", map[string]any{"name": "evil"}},
		{"/secrets/set", map[string]any{"name": "X", "value": "y"}},
		{"/audit/query", map[string]any{"project_id": "acme/app"}},
		{"/audit/query", map[string]any{}},
	} {
		e.mustCode(403, "POST", x.path, f.bob, x.body)
	}
	e.mustCode(403, "GET", "/users/list", f.bob, nil)

	// The team admin manages its projects and team, but nothing else.
	e.mustCode(200, "POST", "/project/ttl_settings/update", f.alice, map[string]any{"project_id": "acme/app", "retention_days": 30})
	e.mustCode(200, "POST", "/project/members/set", f.alice, map[string]any{"project_id": "acme/app", "user_id": "dave", "role": "viewer"})
	e.mustCode(200, "POST", "/calls/query", f.dave, map[string]any{"project_id": "acme/app"})
	e.mustCode(403, "POST", "/call/start", f.dave, callStart("acme/app", "d2"))
	e.mustCode(200, "POST", "/teams/members/set", f.alice, map[string]any{"team": "acme", "user_id": "dave", "role": "member"})
	e.mustCode(200, "POST", "/call/start", f.dave, callStart("acme/app", "d3"))
	e.mustCode(200, "POST", "/teams/members/remove", f.alice, map[string]any{"team": "acme", "user_id": "dave"})
	e.mustCode(403, "POST", "/call/start", f.dave, callStart("acme/app", "d4"))
	e.mustCode(403, "POST", "/project/ttl_settings/update", f.alice, map[string]any{"project_id": "beta/app", "retention_days": 30})
	e.mustCode(403, "POST", "/teams/members/set", f.alice, map[string]any{"team": "beta", "user_id": "alice", "role": "admin"})
	e.mustCode(403, "POST", "/users/update", f.alice, map[string]any{"id": "alice", "admin": true})
	e.mustCode(403, "POST", "/secrets/set", f.alice, map[string]any{"name": "X", "value": "y"})
	e.mustCode(200, "GET", "/users/list", f.alice, nil)
	// A request smuggling a second project is checked on every project.
	e.mustCode(403, "POST", "/call/upsert_batch", f.bob, map[string]any{"batch": append(
		startEnd("acme/app", "s1", "s1", nil, time.Now(), map[string]any{}, 1),
		startEnd("beta/app", "s2", "s2", nil, time.Now(), map[string]any{}, 1)...)})
	// A non-admin request that names no project is refused.
	e.mustCode(403, "POST", "/calls/query", f.bob, map[string]any{})

	// /projects lists only projects the caller can see.
	ps := e.admJSON("GET", "/projects", f.carol, nil)["projects"].([]any)
	for _, p := range ps {
		if id := p.(map[string]any)["project_id"].(string); !strings.HasPrefix(id, "acme/") {
			t.Fatalf("carol sees %s", id)
		}
	}
	if len(ps) == 0 {
		t.Fatal("carol sees no projects")
	}
	// Team list: own teams only.
	if ts := e.admJSON("GET", "/teams/list", f.dave, nil)["teams"].([]any); len(ts) != 0 {
		t.Fatalf("dave sees teams %v", ts)
	}
	// Project members list.
	ms := e.admJSON("POST", "/project/members/list", f.carol, map[string]any{"project_id": "acme/special"})
	roles := map[string]string{}
	for _, m := range ms["members"].([]any) {
		mm := m.(map[string]any)
		roles[mm["user_id"].(string)] = mm["role"].(string)
	}
	if roles["alice"] != "admin" || roles["bob"] != "member" || roles["carol"] != "member" || ms["can_manage"] != false {
		t.Fatalf("members = %v", ms)
	}
}

func TestRBACKeys(t *testing.T) {
	f := newRBAC(t)
	e := f.e
	// Cross-project key: dave is a member of beta/app, but this key is limited to it.
	e.admJSON("POST", "/project/members/set", "root", map[string]any{"project_id": "beta/other", "user_id": "dave", "role": "member"})
	e.mustCode(200, "POST", "/call/start", f.dave, callStart("beta/other", "x1"))
	e.mustCode(403, "POST", "/call/start", f.daveApp, callStart("beta/other", "x2"))
	e.mustCode(200, "POST", "/call/start", f.daveApp, callStart("beta/app", "x3"))

	// Read-only key of a member cannot write.
	ro, _ := createKey(e, "root", map[string]any{"name": "bob-ro", "user_id": "bob", "read_only": true})
	e.mustCode(200, "POST", "/calls/query", ro, map[string]any{"project_id": "acme/app"})
	e.mustCode(403, "POST", "/call/start", ro, callStart("acme/app", "r1"))

	// Self-service keys: for yourself only, never admin, never broader.
	sec, k := createKey(e, f.bob, map[string]any{"name": "bob-ci"})
	if k["user_id"] != "bob" || k["user_bound"] != true {
		t.Fatalf("self key = %v", k)
	}
	e.mustCode(200, "POST", "/call/start", sec, callStart("acme/app", "b2"))
	e.mustCode(403, "POST", "/api_keys/create", f.bob, map[string]any{"name": "x", "user_id": "alice"})
	e.mustCode(403, "POST", "/api_keys/create", f.bob, map[string]any{"name": "x", "admin": true})
	e.mustCode(403, "POST", "/api_keys/create", f.daveApp, map[string]any{"name": "x"})
	e.mustCode(403, "POST", "/api_keys/create", f.daveApp, map[string]any{"name": "x", "projects": []any{"beta/other"}})
	e.mustCode(200, "POST", "/api_keys/create", f.daveApp, map[string]any{"name": "x", "projects": []any{"beta/app"}})
	e.mustCode(403, "POST", "/api_keys/create", ro, map[string]any{"name": "x"})
	// Listing shows own keys; revoking someone else's key is not possible.
	var aliceKeyID string
	for _, x := range e.admJSON("GET", "/api_keys/list", "root", nil)["keys"].([]any) {
		m := x.(map[string]any)
		if m["user_id"] == "alice" {
			aliceKeyID = m["id"].(string)
		}
	}
	for _, x := range e.admJSON("GET", "/api_keys/list", f.bob, nil)["keys"].([]any) {
		if u := x.(map[string]any)["user_id"]; u != "bob" {
			t.Fatalf("bob lists %v's key", u)
		}
	}
	e.mustCode(404, "POST", "/api_keys/revoke", f.bob, map[string]any{"id": aliceKeyID})
	e.mustCode(200, "GET", "/auth/check", f.alice, nil)

	// Disabling a user turns their keys off at once; re-enabling restores them.
	e.mustCode(200, "POST", "/users/update", "root", map[string]any{"id": "bob", "disabled": true})
	e.mustCode(401, "POST", "/calls/query", f.bob, map[string]any{"project_id": "acme/app"})
	e.mustCode(200, "POST", "/users/update", "root", map[string]any{"id": "bob", "disabled": false})
	e.mustCode(200, "POST", "/calls/query", f.bob, map[string]any{"project_id": "acme/app"})

	// A server-admin user's key is a server admin; its read-only key is not.
	e.mustCode(200, "POST", "/users/update", "root", map[string]any{"id": "carol", "admin": true})
	e.mustCode(200, "POST", "/secrets/set", f.carol, map[string]any{"name": "X", "value": "y"})
	e.mustCode(200, "POST", "/call/start", f.carol, callStart("zeta/x", "z1"))
	cro, _ := createKey(e, "root", map[string]any{"name": "carol-ro", "user_id": "carol", "read_only": true})
	e.mustCode(403, "POST", "/secrets/set", cro, map[string]any{"name": "X", "value": "y"})
	// Admins cannot demote or disable themselves.
	e.mustCode(400, "POST", "/users/update", f.carol, map[string]any{"id": "carol", "admin": false})
	// Bound keys cannot carry the admin flag.
	e.mustCode(400, "POST", "/api_keys/create", "root", map[string]any{"name": "x", "user_id": "bob", "admin": true})

	me := e.admJSON("GET", "/auth/me", f.alice, nil)
	if me["user"].(map[string]any)["id"] != "alice" || me["roles"].(map[string]any)["entities"].(map[string]any)["acme"] != "admin" || me["auth_method"] != "api_key" {
		t.Fatalf("me = %v", me)
	}
	pr := e.admJSON("GET", "/auth/principal", f.bob, nil)
	if pr["rbac"] != true || pr["admin"] != false {
		t.Fatalf("principal = %v", pr)
	}
}

func TestAuditLog(t *testing.T) {
	f := newRBAC(t)
	e, s := f.e, f.s
	e.mustCode(200, "POST", "/call/start", f.bob, callStart("acme/app", "call-1"))
	e.mustCode(200, "POST", "/obj/create", f.bob, map[string]any{"obj": map[string]any{"project_id": "acme/app", "object_id": "my-ds", "val": 1}})
	e.mustCode(403, "POST", "/call/start", f.carol, callStart("acme/app", "call-2"))
	e.mustCode(403, "POST", "/project/delete", f.bob, map[string]any{"project_id": "acme/app"})
	e.mustCode(401, "POST", "/call/start", "bogus", callStart("acme/app", "call-3"))
	e.mustCode(200, "POST", "/calls/query", f.bob, map[string]any{"project_id": "acme/app"}) // reads are not recorded
	e.mustCode(200, "POST", "/secrets/set", "root", map[string]any{"name": "AUDIT_SECRET", "value": "hunter2-value"})

	all := e.admJSON("POST", "/audit/query", "root", map[string]any{"limit": 100})
	entries := all["entries"].([]any)
	find := func(pred func(m map[string]any) bool) map[string]any {
		for _, x := range entries {
			if m := x.(map[string]any); pred(m) {
				return m
			}
		}
		return nil
	}
	start := find(func(m map[string]any) bool { return m["path"] == "/call/start" && m["outcome"] == "success" })
	if start == nil || start["actor"] != "bob" || start["project_id"] != "acme/app" || start["auth_method"] != "api_key" ||
		len(start["call_ids"].([]any)) != 1 || start["call_ids"].([]any)[0] != "call-1" || start["key_id"] == nil {
		t.Fatalf("call start entry = %v", start)
	}
	if o := find(func(m map[string]any) bool { return m["path"] == "/obj/create" }); o == nil || o["object_ids"].([]any)[0] != "my-ds" {
		t.Fatalf("obj entry = %v", o)
	}
	if d := find(func(m map[string]any) bool { return m["actor"] == "carol" && m["outcome"] == "denied" }); d == nil || d["status"].(float64) != 403 {
		t.Fatal("denied write not recorded")
	}
	if d := find(func(m map[string]any) bool { return m["path"] == "/project/delete" && m["actor"] == "bob" }); d == nil || d["outcome"] != "denied" {
		t.Fatal("denied admin call not recorded")
	}
	if d := find(func(m map[string]any) bool { return m["outcome"] == "unauthenticated" }); d == nil || d["actor"] != "anonymous" {
		t.Fatal("unauthenticated write not recorded")
	}
	if find(func(m map[string]any) bool { return m["path"] == "/calls/query" && m["outcome"] == "success" }) != nil {
		t.Fatal("reads should not be recorded")
	}
	if d := find(func(m map[string]any) bool {
		return m["path"] == "/teams/members/set" && strings.Contains(m["detail"].(string), "alice as admin of team acme")
	}); d == nil || d["actor"] != "static-key" {
		t.Fatalf("admin action detail missing: %v", d)
	}
	raw, _ := json.Marshal(all)
	if bytes.Contains(raw, []byte("hunter2")) {
		t.Fatal("audit log contains a secret value")
	}

	// Project admins read their project's entries only; filters apply.
	byAlice := e.admJSON("POST", "/audit/query", f.alice, map[string]any{"project_id": "acme/app", "actor": "bob"})
	for _, x := range byAlice["entries"].([]any) {
		m := x.(map[string]any)
		if m["actor"] != "bob" || m["project_id"] != "acme/app" {
			t.Fatalf("filter leak: %v", m)
		}
	}
	if byAlice["total_count"].(float64) < 3 {
		t.Fatalf("alice sees %v", byAlice["total_count"])
	}
	e.mustCode(403, "POST", "/audit/query", f.alice, map[string]any{})
	e.mustCode(403, "POST", "/audit/query", f.alice, map[string]any{"project_id": "beta/app"})
	denied := e.admJSON("POST", "/audit/query", "root", map[string]any{"outcome": "denied", "method": "post", "path_prefix": "/call/"})
	if denied["total_count"].(float64) != 1 {
		t.Fatalf("denied filter = %v", denied["total_count"])
	}

	// CSV export.
	csvBody := e.mustCode(200, "POST", "/audit/export", f.alice, map[string]any{"project_id": "acme/app"})
	recs, err := csv.NewReader(bytes.NewReader(csvBody)).ReadAll()
	if err != nil || len(recs) < 4 || recs[0][0] != "id" || recs[0][2] != "actor" {
		t.Fatalf("csv = %v %v", err, recs)
	}

	// Project delete keeps the audit trail.
	e.mustCode(200, "POST", "/project/delete", f.alice, map[string]any{"project_id": "acme/app"})
	if n := e.admJSON("POST", "/audit/query", "root", map[string]any{"project_id": "acme/app", "path_prefix": "/project/delete", "outcome": "success"})["total_count"]; n.(float64) != 1 {
		t.Fatalf("project delete not audited: %v", n)
	}

	// Retention.
	s.FlushAudit()
	if err := s.store.InsertAudit(t.Context(), []store.AuditEntry{{TimeMicros: time.Now().AddDate(0, 0, -200).UnixMicro(), Actor: "old",
		AuthMethod: "api_key", Method: "POST", Path: "/x", Status: 200, Outcome: "success"}}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PurgeAudit(t.Context()); err != nil || n != 1 {
		t.Fatalf("purge = %d, %v", n, err)
	}
}

// sessionClient signs a user in directly and returns their cookies.
func sessionCookies(t *testing.T, s *Server, user string) (session, csrf *http.Cookie) {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := s.StartSession(rec, httptest.NewRequest("GET", "/", nil), user); err != nil {
		t.Fatal(err)
	}
	for _, c := range rec.Result().Cookies() {
		switch c.Name {
		case sessionCookie:
			session = c
		case csrfCookie:
			csrf = c
		}
	}
	if session == nil || csrf == nil || !session.HttpOnly || csrf.HttpOnly || session.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookies: %+v %+v", session, csrf)
	}
	return session, csrf
}

func cookieDo(t *testing.T, e *testEnv, method, path string, body any, cookies []*http.Cookie, headers map[string]string) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func TestSessionsAndCSRF(t *testing.T) {
	f := newRBAC(t)
	e, s := f.e, f.s
	sess, csrf := sessionCookies(t, s, "bob")
	cs := []*http.Cookie{sess, csrf}
	ok := map[string]string{csrfHeader: csrf.Value}
	if code, out := cookieDo(t, e, "GET", "/auth/me", nil, cs, nil); code != 200 || !bytes.Contains(out, []byte(`"auth_method":"session"`)) {
		t.Fatalf("me -> %d %s", code, out)
	}
	if code, _ := cookieDo(t, e, "POST", "/calls/query", map[string]any{"project_id": "acme/app"}, cs, nil); code != 403 {
		t.Fatalf("POST without CSRF header -> %d", code)
	}
	if code, _ := cookieDo(t, e, "POST", "/call/start", callStart("acme/app", "s1"), cs, map[string]string{csrfHeader: "wrong"}); code != 403 {
		t.Fatalf("wrong CSRF -> %d", code)
	}
	s.FlushAudit()
	if ents, _, _ := s.store.QueryAudit(t.Context(), store.AuditFilter{Outcome: "denied", PathPrefix: "/call/start"}); len(ents) != 1 ||
		ents[0].Actor != "bob" || ents[0].AuthMethod != "session" {
		t.Fatalf("CSRF denial audit = %+v", ents)
	}
	if code, out := cookieDo(t, e, "POST", "/call/start", callStart("acme/app", "s1"), cs, ok); code != 200 {
		t.Fatalf("session write -> %d %s", code, out)
	}
	if code, _ := cookieDo(t, e, "POST", "/call/start", callStart("acme/app", "s2"), cs,
		map[string]string{csrfHeader: csrf.Value, "Origin": "https://evil.example"}); code != 403 {
		t.Fatalf("foreign origin -> %d", code)
	}
	// Roles apply to sessions as to keys.
	if code, _ := cookieDo(t, e, "POST", "/project/delete", map[string]any{"project_id": "acme/app"}, cs, ok); code != 403 {
		t.Fatalf("member session deleting project -> %d", code)
	}
	// The call is attributed to the session's user.
	c := e.admJSON("POST", "/call/read", "root", map[string]any{"project_id": "acme/app", "id": "s1"})["call"].(map[string]any)
	if c["user_id"] != "bob" {
		t.Fatalf("user_id = %v", c["user_id"])
	}
	// Tampered cookie.
	bad := *sess
	bad.Value = strings.Replace(sess.Value, ".", "x.", 1)
	if code, _ := cookieDo(t, e, "GET", "/auth/check", nil, []*http.Cookie{&bad}, nil); code != 401 {
		t.Fatalf("tampered cookie -> %d", code)
	}
	// An API key in the request wins and needs no CSRF token.
	if code, _ := cookieDo(t, e, "POST", "/call/start", callStart("acme/app", "s3"), cs, map[string]string{"Authorization": "Bearer " + f.bob}); code != 200 {
		t.Fatalf("key with cookie -> %d", code)
	}
	// Logout needs the CSRF token, then revokes the session.
	if code, _ := cookieDo(t, e, "POST", "/auth/logout", nil, cs, nil); code != 403 {
		t.Fatalf("logout without CSRF -> %d", code)
	}
	if code, _ := cookieDo(t, e, "POST", "/auth/logout", nil, cs, ok); code != 200 {
		t.Fatalf("logout -> %d", code)
	}
	if code, _ := cookieDo(t, e, "GET", "/auth/check", nil, cs, nil); code != 401 {
		t.Fatalf("revoked session -> %d", code)
	}
	// Disabling a user ends their sessions.
	sess2, csrf2 := sessionCookies(t, s, "carol")
	cs2 := []*http.Cookie{sess2, csrf2}
	if code, _ := cookieDo(t, e, "GET", "/auth/check", nil, cs2, nil); code != 200 {
		t.Fatalf("carol session -> %d", code)
	}
	e.mustCode(200, "POST", "/users/update", "root", map[string]any{"id": "carol", "disabled": true})
	e.mustCode(200, "POST", "/users/update", "root", map[string]any{"id": "carol", "disabled": false})
	if code, _ := cookieDo(t, e, "GET", "/auth/check", nil, cs2, nil); code != 401 {
		t.Fatalf("session after disable -> %d", code)
	}
	// Admin-forced sign-out.
	sess3, csrf3 := sessionCookies(t, s, "alice")
	e.mustCode(200, "POST", "/users/revoke_sessions", "root", map[string]any{"id": "alice"})
	if code, _ := cookieDo(t, e, "GET", "/auth/check", nil, []*http.Cookie{sess3, csrf3}, nil); code != 401 {
		t.Fatalf("session after revoke -> %d", code)
	}
}

func TestDBPermissionWarnings(t *testing.T) {
	p := filepath.Join(t.TempDir(), "w.db")
	if err := store.PrepareDBFile(p); err != nil {
		t.Fatal(err)
	}
	if w := store.DBPermissionWarnings(p); len(w) != 0 {
		t.Fatalf("new file warnings: %v", w)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if w := store.DBPermissionWarnings(p); len(w) == 0 || !strings.Contains(w[0], "world-readable") {
		t.Fatalf("warnings = %v", w)
	}
}
