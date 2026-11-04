package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// Ref scopes (refscope.go): a ref found inside a value resolves only in
// that value's project unless the context's scope allows the target; a
// top-level ref is checked against the scope when there is one.
func TestRefScope(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "weft.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	mustSave := func(p, name string, v any) {
		if _, _, err := s.CreateObject(ctx, p, name, v, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	mustSave("b/b", "secret", map[string]any{"s": "B"})
	mustSave("a/a", "own", map[string]any{"s": "A"})
	mustSave("a/a", "pivot", map[string]any{"b": "weft:///b/b/object/secret:latest", "a": "weft:///a/a/object/own:latest"})
	// A value that refers to itself through its own path.
	mustSave("a/a", "loop", map[string]any{"x": "weft:///a/a/object/loop:latest/key/x"})

	onlyA := func(p string) bool { return p == "a/a" }
	all := func(string) bool { return true }
	const nestedB, nestedA, directB = "weft:///a/a/object/pivot:latest/key/b/key/s", "weft:///a/a/object/pivot:latest/key/a/key/s",
		"weft:///b/b/object/secret:latest/key/s"
	for _, tc := range []struct {
		name string
		ctx  context.Context
		ref  string
		want any   // value, or nil
		err  error // nil, errRefDenied, or a *ForbiddenError (forbidden)
		forb bool
	}{
		{"no scope: top-level anywhere", ctx, directB, "B", nil, false},
		{"no scope: nested same project", ctx, nestedA, "A", nil, false},
		{"no scope: nested other project", ctx, nestedB, nil, errRefDenied, false},
		{"home a: nested other project", WithRefHome(ctx, "a/a"), nestedB, nil, errRefDenied, false},
		{"home a: top-level other project", WithRefHome(ctx, "a/a"), directB, nil, nil, true},
		{"home a: nested same project", WithRefHome(ctx, "a/a"), nestedA, "A", nil, false},
		{"reader of a: nested b", WithRefAccess(ctx, onlyA), nestedB, nil, errRefDenied, false},
		{"reader of a: direct b", WithRefAccess(ctx, onlyA), directB, nil, nil, true},
		{"reader of all: nested b", WithRefAccess(ctx, all), nestedB, "B", nil, false},
		{"copied scope", WithRefScopeOf(context.Background(), WithRefAccess(ctx, onlyA)), nestedB, nil, errRefDenied, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := s.ReadRef(tc.ctx, tc.ref)
			var fe *ForbiddenError
			switch {
			case tc.forb:
				if !errors.As(err, &fe) {
					t.Fatalf("err = %v, want ForbiddenError", err)
				}
			case tc.err != nil:
				if !errors.Is(err, tc.err) {
					t.Fatalf("err = %v, want %v", err, tc.err)
				}
			case err != nil || v != tc.want:
				t.Fatalf("= %v, %v; want %v", v, err, tc.want)
			}
			// ReadRefs reads every unreadable ref as null.
			vals, err := s.ReadRefs(tc.ctx, []string{tc.ref})
			if err != nil || vals[0] != tc.want {
				t.Fatalf("ReadRefs = %v, %v; want [%v]", vals, err, tc.want)
			}
		})
	}
	if _, err := s.ReadRef(ctx, "weft:///a/a/object/loop:latest/key/x"); err == nil {
		t.Fatal("a self-referencing ref must not resolve")
	}
	if vals, err := s.ReadRefs(ctx, []string{"weft:///a/a/object/loop:latest/key/x"}); err != nil || vals[0] != nil {
		t.Fatalf("self-referencing ref = %v %v", vals, err)
	}
}
