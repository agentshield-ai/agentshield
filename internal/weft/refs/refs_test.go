package refs

import "testing"

func TestParseRoundTrip(t *testing.T) {
	cases := []string{
		"weft:///e/p/object/my_ds:abc123",
		"weft:///e/p/op/MyModel.predict:xyz",
		"weft:///e/p/table/deadbeef",
		"weft:///e/p/call/0190-abc",
		"weft:///e/p/object/ds:abc/attr/rows/id/r1",
		"weft:///e/p/object/with%20space:abc",
	}
	for _, c := range cases {
		r, err := Parse(c)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c, err)
		}
		if got := r.String(); got != c {
			t.Errorf("round trip %q -> %q", c, got)
		}
	}
}

func TestParseWeaveScheme(t *testing.T) {
	r, err := Parse("weave:///ent/proj/object/x:d1/attr/rows")
	if err != nil {
		t.Fatal(err)
	}
	if r.ProjectID() != "ent/proj" || r.Name != "x" || r.Digest != "d1" || len(r.Extra) != 2 {
		t.Fatalf("unexpected parse: %+v", r)
	}
	if Normalize("weave:///a/b/call/1") != "weft:///a/b/call/1" {
		t.Fatal("normalize failed")
	}
}

func TestParseErrors(t *testing.T) {
	for _, c := range []string{"", "http://x", "weft:///e/p", "weft:///e/p/bogus/x", "weft:////p/object/x:d"} {
		if _, err := Parse(c); err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}

func TestDigestStable(t *testing.T) {
	a, _, _ := Digest(map[string]any{"b": 1, "a": []any{"x", map[string]any{"z": true, "y": nil}}})
	b, _, _ := Digest(map[string]any{"a": []any{"x", map[string]any{"y": nil, "z": true}}, "b": 1})
	if a != b {
		t.Fatalf("digest depends on key order: %s vs %s", a, b)
	}
	if len(a) != 43 {
		t.Fatalf("unexpected digest length %d", len(a))
	}
}

func TestNormalizeProject(t *testing.T) {
	if p, _ := NormalizeProject("proj"); p != "default/proj" {
		t.Fatalf("got %q", p)
	}
	if _, err := NormalizeProject("a/b/c"); err == nil {
		t.Fatal("expected error for nested project")
	}
	if _, err := NormalizeProject(""); err == nil {
		t.Fatal("expected error for empty project")
	}
}
