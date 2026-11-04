package store

import (
	"context"
	"fmt"
)

// Ref scope. A ref names a stored thing in some project, and a value can
// hold refs to any project. Every place that dereferences a ref (refs/read,
// nested refs met while walking a ref's path, expand_columns, evaluation
// datasets, models and scorers, monitors' scorers, provider refs) checks
// the target project against the scope carried by the context:
//
//   - the server attaches the caller's read access to every request
//     (WithRefAccess); a ref into a project the caller may not read is
//     refused;
//   - background jobs (monitors, signals) run with the project they work in
//     as their home (WithRefHome), and evaluations keep the scope of the
//     request that started them (WithRefScopeOf);
//   - without any scope (internal callers, tests) a top-level ref resolves
//     anywhere, but a ref found inside a value still resolves only in the
//     project that value belongs to.
//
// So a value can never be used as a pivot into a project its reader cannot
// read.

type refScopeKey struct{}

type refScope struct {
	home  string                      // project of a background job; "" for requests
	allow func(projectID string) bool // projects the caller may read; nil: only home
}

func (sc *refScope) permits(projectID string) bool {
	if sc.home != "" && projectID == sc.home {
		return true
	}
	return sc.allow != nil && sc.allow(projectID)
}

func refScopeFrom(ctx context.Context) *refScope {
	sc, _ := ctx.Value(refScopeKey{}).(*refScope)
	return sc
}

// WithRefAccess makes refs under ctx resolve only in projects allow
// accepts (the caller's read access).
func WithRefAccess(ctx context.Context, allow func(projectID string) bool) context.Context {
	sc := &refScope{allow: allow}
	if old := refScopeFrom(ctx); old != nil {
		sc.home = old.home
	}
	return context.WithValue(ctx, refScopeKey{}, sc)
}

// WithRefHome makes refs under ctx resolve in projectID, plus any project
// the scope already in ctx allows.
func WithRefHome(ctx context.Context, projectID string) context.Context {
	sc := &refScope{home: projectID}
	if old := refScopeFrom(ctx); old != nil {
		sc.allow = old.allow
	}
	return context.WithValue(ctx, refScopeKey{}, sc)
}

// WithRefScopeOf copies the ref scope of src onto dst, for work that
// outlives the request that started it.
func WithRefScopeOf(dst, src context.Context) context.Context {
	if sc := refScopeFrom(src); sc != nil {
		return context.WithValue(dst, refScopeKey{}, sc)
	}
	return dst
}

// CanReadProject reports whether a top-level ref into projectID may be
// resolved under ctx. Without a scope it may.
func CanReadProject(ctx context.Context, projectID string) bool {
	sc := refScopeFrom(ctx)
	return sc == nil || sc.permits(projectID)
}

// CheckRefProject returns a ForbiddenError unless a ref into projectID may
// be resolved under ctx.
func CheckRefProject(ctx context.Context, ref, projectID string) error {
	if CanReadProject(ctx, projectID) {
		return nil
	}
	return Forbiddenf("%s is in project %s, which this request may not read", ref, projectID)
}

// canFollow reports whether a ref into target found inside a value of
// project origin may be followed.
func canFollow(ctx context.Context, origin, target string) bool {
	if target == origin {
		return true
	}
	sc := refScopeFrom(ctx)
	return sc != nil && sc.permits(target)
}

// ForbiddenError is a request for data in a project the caller may not
// read (HTTP 403).
type ForbiddenError struct{ Msg string }

func (e *ForbiddenError) Error() string { return e.Msg }

// Forbiddenf builds a ForbiddenError.
func Forbiddenf(format string, args ...any) error {
	return &ForbiddenError{Msg: fmt.Sprintf(format, args...)}
}

// span encoding of span refs

type spanEncoderKey struct{}

// WithSpanEncoder sets how a span reached through a ref (refs/read, a
// ref's path, expand_columns) is represented: Weft's own API returns its
// Span schema; without an encoder the stored call row is used (the
// adapter's routes).
func WithSpanEncoder(ctx context.Context, enc func(*Call) any) context.Context {
	return context.WithValue(ctx, spanEncoderKey{}, enc)
}

func spanValue(ctx context.Context, c *Call) any {
	if enc, ok := ctx.Value(spanEncoderKey{}).(func(*Call) any); ok && enc != nil {
		return enc(c)
	}
	return c
}
