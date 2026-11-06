package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/compat"
	"github.com/agentshield-ai/agentshield/internal/weft/refs"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// Project scoping of request bodies.
//
// A non-admin caller is authorised against every project its request names
// (requestProjects, admin.go). That only holds if the projects found there
// are the projects the handler then uses, so:
//
//   - the probe (probeBody) fails closed: a body that is not exactly one
//     JSON value, or that has a project-bearing field of a type the probe
//     cannot read, is refused (400) instead of being skipped;
//   - field names are matched the way encoding/json matches them (case
//     insensitively), and every spelling present counts;
//   - decodeBody (and the adapter's) refuses trailing data after the JSON
//     value, and ignores bodies on GET and HEAD;
//   - authorise at use: after authorisation the request carries a guard
//     (projectGuard) and decodeBody checks every project_id / project_ids
//     field the handler actually decoded, at any depth of its request type,
//     against the caller's role. So a project the probe missed is still
//     refused before the handler sees it.

// requestError is a refusal with its status (400 malformed, 413 too large).
type requestError struct {
	code int
	msg  string
}

func (e *requestError) Error() string { return e.msg }

func badRequest(format string, args ...any) error {
	return &requestError{code: http.StatusBadRequest, msg: fmt.Sprintf(format, args...)}
}

// ---------------------------------------------------------------- body limits

// Request body caps (decompressed). Reads are small; admin and IAM routes
// are tiny; ingest, batches and object saves can be large.
const (
	maxReadBody  = 4 << 20
	maxAdminBody = 1 << 20
)

// bodyLimit is the largest JSON body a request may carry.
func bodyLimit(r *http.Request) int64 {
	path := strings.TrimSuffix(r.URL.Path, "/")
	if serverAdminPaths[path] || projectAdminPaths[path] || strings.HasPrefix(path, "/secrets/") {
		return maxAdminBody
	}
	for _, p := range selfPrefixes {
		if strings.HasPrefix(path, p) && p != "/audit/" {
			return maxAdminBody
		}
	}
	if isReadRequest(r) {
		return maxReadBody
	}
	return maxJSONBody
}

// ---------------------------------------------------------------- probe

// probedKeys are the body fields that name projects. Any other top-level
// string (or list of strings) that is a ref names its project too.
var probedKeys = []string{"project_id", "project_ids", "start", "end", "obj", "table", "span", "batch", "items", "refs", "variables"}

// probedKey is the probed field a JSON key stands for, matched like
// encoding/json matches struct fields (bytes.EqualFold), or "".
func probedKey(k string) string {
	for _, n := range probedKeys {
		if strings.EqualFold(k, n) {
			return n
		}
	}
	return ""
}

// jsonKind is the kind of a raw JSON value: '{', '[', '"', 'n' (null) or
// 's' (number or boolean).
func jsonKind(v json.RawMessage) byte {
	v = bytes.TrimSpace(v)
	if len(v) == 0 {
		return 'n'
	}
	switch v[0] {
	case '{', '[', '"':
		return v[0]
	case 'n':
		return 'n'
	}
	return 's'
}

// probeBody adds every project a JSON request body names: project_id and
// project_ids at the top level, project_id inside the start/end/obj/table/
// span wrappers, the items of batch and items (and batch items' req
// wrappers), refs, ref-valued top-level fields, and the GraphQL
// variables' entity/name. It fails when the body is not exactly one JSON
// value or a project-bearing field has an unexpected type; add has then
// still seen what was found before (for the audit log).
func probeBody(body []byte, add func(string)) error {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if !json.Valid(body) {
		return badRequest("invalid JSON body: the body must be exactly one JSON value")
	}
	switch jsonKind(body) {
	case '{':
		return probeObject(body, add, true)
	case '[':
		var arr []json.RawMessage
		if err := json.Unmarshal(body, &arr); err != nil {
			return badRequest("invalid JSON body: %v", err)
		}
		for _, el := range arr {
			if jsonKind(el) == '{' {
				if err := probeObject(el, add, true); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// probeLenient is probeBody for the audit log: on a body it refuses, it
// still reports what the first JSON value names.
func probeLenient(body []byte, add func(string)) {
	if probeBody(body, add) == nil {
		return
	}
	var first json.RawMessage
	if json.NewDecoder(bytes.NewReader(body)).Decode(&first) == nil {
		_ = probeBody(first, add)
	}
}

func addRefTo(add func(string), v string) {
	if refs.IsRef(v) {
		if rr, err := refs.Parse(v); err == nil {
			add(rr.ProjectID())
		}
	}
}

// probeProjectID reads a project_id value: a string, or null.
func probeProjectID(v json.RawMessage, add func(string), where string) error {
	switch jsonKind(v) {
	case 'n':
		return nil
	case '"':
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return badRequest("invalid %s", where)
		}
		add(s)
		return nil
	}
	return badRequest("%s must be a string", where)
}

// probeHolder reads a wrapper object (start, end, obj, table, span): its
// project_id. Scalars carry no project (start and end are also times in
// some requests); a list is refused.
func probeHolder(v json.RawMessage, add func(string), where string) error {
	switch jsonKind(v) {
	case '{':
		var m map[string]json.RawMessage
		if err := json.Unmarshal(v, &m); err != nil {
			return badRequest("invalid %s: %v", where, err)
		}
		for k, x := range m {
			if strings.EqualFold(k, "project_id") {
				if err := probeProjectID(x, add, where+".project_id"); err != nil {
					return err
				}
			}
		}
		return nil
	case '[':
		return badRequest("%s must be an object", where)
	case '"':
		var s string
		if json.Unmarshal(v, &s) == nil {
			addRefTo(add, s)
		}
	}
	return nil
}

// probeList reads batch / items: a list of objects (or null).
func probeList(v json.RawMessage, add func(string), where string) error {
	switch jsonKind(v) {
	case 'n':
		return nil
	case '[':
	default:
		return badRequest("%s must be a list", where)
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(v, &arr); err != nil {
		return badRequest("invalid %s: %v", where, err)
	}
	var firstErr error
	for i, el := range arr {
		if jsonKind(el) != '{' {
			if firstErr == nil {
				firstErr = badRequest("%s[%d] must be an object", where, i)
			}
			continue
		}
		if err := probeObject(el, add, false); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// probeObject probes one object: the request body (top) or a batch item.
func probeObject(v json.RawMessage, add func(string), top bool) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(v, &m); err != nil {
		return badRequest("invalid JSON body: %v", err)
	}
	var firstErr error
	for k, x := range m {
		var err error
		switch probedKey(k) {
		case "project_id":
			err = probeProjectID(x, add, "project_id")
		case "project_ids":
			switch jsonKind(x) {
			case 'n':
			case '[':
				var ids []string
				if json.Unmarshal(x, &ids) != nil {
					err = badRequest("project_ids must be a list of strings")
				}
				for _, id := range ids {
					add(id)
				}
			default:
				err = badRequest("project_ids must be a list of strings")
			}
		case "start", "end", "obj", "table", "span":
			err = probeHolder(x, add, strings.ToLower(k))
		case "batch", "items":
			if !top {
				// Nested lists are values (rows, messages), not requests.
				continue
			}
			err = probeList(x, add, strings.ToLower(k))
		case "refs":
			switch jsonKind(x) {
			case 'n':
			case '"':
				var s string
				_ = json.Unmarshal(x, &s)
				addRefTo(add, s)
			case '[':
				var list []string
				if json.Unmarshal(x, &list) != nil {
					err = badRequest("refs must be a list of strings")
				}
				for _, s := range list {
					addRefTo(add, s)
				}
			default:
				err = badRequest("refs must be a list of strings")
			}
		case "variables":
			if !top {
				continue
			}
			switch jsonKind(x) {
			case 'n':
			case '{':
				var vars map[string]any
				if json.Unmarshal(x, &vars) != nil {
					err = badRequest("invalid variables")
				}
				ent, _ := vars["entity"].(string)
				name, _ := vars["name"].(string)
				if ent != "" && name != "" {
					add(ent + "/" + name)
				}
			default:
				err = badRequest("variables must be an object")
			}
		default:
			if !top {
				// A batch item's req wrapper holds start/end.
				if strings.EqualFold(k, "req") && jsonKind(x) == '{' {
					err = probeObject(x, add, false)
				}
				continue
			}
			// Top-level ref-valued fields (evaluation_ref, model, scorer_refs, ...).
			switch jsonKind(x) {
			case '"':
				var s string
				if json.Unmarshal(x, &s) == nil {
					addRefTo(add, s)
				}
			case '[':
				var arr []any
				if json.Unmarshal(x, &arr) == nil {
					for _, el := range arr {
						if s, ok := el.(string); ok {
							addRefTo(add, s)
						}
					}
				}
			}
		}
		if err != nil && firstErr == nil {
			firstErr = err // keep going: the audit log wants every project named
		}
	}
	return firstErr
}

// ---------------------------------------------------------------- guard

// projectGuard authorises, at use, the projects a handler decoded.
type projectGuard struct {
	p    *principal
	need store.Role
}

type projectGuardKey struct{}

// withProjectGuard returns ctx carrying the caller's project check.
func withProjectGuard(ctx context.Context, p *principal, need store.Role) context.Context {
	g := &projectGuard{p: p, need: need}
	ctx = context.WithValue(ctx, projectGuardKey{}, g)
	return compat.WithDecodeCheck(ctx, g.checkValue)
}

func guardFrom(ctx context.Context) *projectGuard {
	g, _ := ctx.Value(projectGuardKey{}).(*projectGuard)
	return g
}

// check refuses a project the caller may not use with this request.
func (g *projectGuard) check(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	pid, err := refs.NormalizeProject(raw)
	if err != nil {
		return &store.InvalidError{Msg: err.Error()}
	}
	if g.p.projects != nil && !g.p.projects[pid] {
		return store.Forbiddenf("this API key has no access to project %s", pid)
	}
	if have := g.p.roleFor(pid); have.Rank() < g.need.Rank() {
		if have == store.RoleNone {
			return store.Forbiddenf("no access to project %s (needs the %s role)", pid, g.need)
		}
		return store.Forbiddenf("the %s role on %s is needed; you have %s", g.need, pid, have)
	}
	return nil
}

// checkProject applies the request's guard to one project (no guard: open
// servers, admins and static keys).
func checkProject(r *http.Request, pid string) error {
	if g := guardFrom(r.Context()); g != nil {
		return g.check(pid)
	}
	return nil
}

// checkDecoded applies the request's guard to a decoded request value.
func checkDecoded(r *http.Request, v any) error {
	if g := guardFrom(r.Context()); g != nil {
		return g.checkValue(v)
	}
	return nil
}

// checkValue checks every project_id (string or *string) and project_ids
// ([]string) field of a decoded request, at any depth of its type. Fields
// of interface or map[string]any type are user data and are not walked.
func (g *projectGuard) checkValue(v any) error {
	return g.walk(reflect.ValueOf(v), 0)
}

func (g *projectGuard) walk(v reflect.Value, depth int) error {
	if depth > 64 || !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return nil
		}
		return g.walk(v.Elem(), depth+1)
	case reflect.Slice, reflect.Array:
		if !walkable(v.Type().Elem()) {
			return nil
		}
		for i := 0; i < v.Len(); i++ {
			if err := g.walk(v.Index(i), depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		if !walkable(v.Type().Elem()) {
			return nil
		}
		it := v.MapRange()
		for it.Next() {
			if err := g.walk(it.Value(), depth+1); err != nil {
				return err
			}
		}
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			fv := v.Field(i)
			switch name {
			case "project_id":
				if s, ok := stringOf(fv); ok {
					if err := g.check(s); err != nil {
						return err
					}
					continue
				}
			case "project_ids":
				if fv.Kind() == reflect.Slice && fv.Type().Elem().Kind() == reflect.String {
					for j := 0; j < fv.Len(); j++ {
						if err := g.check(fv.Index(j).String()); err != nil {
							return err
						}
					}
					continue
				}
			}
			if err := g.walk(fv, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func stringOf(v reflect.Value) (string, bool) {
	switch {
	case v.Kind() == reflect.String:
		return v.String(), true
	case v.Kind() == reflect.Pointer && v.Type().Elem().Kind() == reflect.String:
		if v.IsNil() {
			return "", true
		}
		return v.Elem().String(), true
	}
	return "", false
}

// walkable reports element types that can hold request structs.
func walkable(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	return t.Kind() == reflect.Struct
}
