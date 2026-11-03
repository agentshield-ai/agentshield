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

func TestParseErrors(t *testing.T) {
	for _, c := range []string{"", "http://x", "weft:///e/p", "weft:///e/p/bogus/x", "weft:////p/object/x:d", "other:///e/p/call/1"} {
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

// Expected values computed independently with Python: json.dumps(v,
// sort_keys=True), SHA-256, URL-safe base64 with '-'→'X', '_'→'Y' and no
// padding; tables: hex SHA-256 of the concatenated row digests.
func TestDigestsMatchPythonJSON(t *testing.T) {
	v, _ := Decode([]byte(`{"b": [1, 2.5, null, true], "a": "héllo ☃ 😀 \"q\"\n", "c": {"z": 1e-07, "y": 1.0}}`))
	got, err := DigestJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a": "h\u00e9llo \u2603 \ud83d\ude00 \"q\"\n", "b": [1, 2.5, null, true], "c": {"y": 1.0, "z": 1e-07}}`
	if string(got) != want {
		t.Fatalf("DigestJSON:\n got %s\nwant %s", got, want)
	}
	if pyFloat(1e-7) != "1e-07" || pyFloat(1) != "1.0" || pyFloat(1e16) != "1e+16" || pyFloat(0.5) != "0.5" {
		t.Fatalf("pyFloat: %s %s %s %s", pyFloat(1e-7), pyFloat(1), pyFloat(1e16), pyFloat(0.5))
	}
}

func TestDigestJSONEscapesDEL(t *testing.T) {
	got, _ := DigestJSON("a\x7fb")
	if string(got) != `"a`+"\\"+`u007fb"` {
		t.Fatalf("got %s", got)
	}
}
