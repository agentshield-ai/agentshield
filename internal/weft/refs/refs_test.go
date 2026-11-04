package refs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestParseRoundTrip(t *testing.T) {
	cases := []string{
		"weft:///e/p/object/my_ds:abc123",
		"weft:///e/p/function/MyModel.predict:xyz",
		"weft:///e/p/table/deadbeef",
		"weft:///e/p/span/0190-abc",
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
	if len(a) != 64 || strings.ToLower(a) != a {
		t.Fatalf("expected a lowercase hex SHA-256, got %q", a)
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

// The old kind names are read as the current ones.
func TestParseLegacyKinds(t *testing.T) {
	for in, want := range map[string]string{
		"weft:///e/p/op/f:abc":           "weft:///e/p/function/f:abc",
		"weft:///e/p/call/0190":          "weft:///e/p/span/0190",
		"weft:///e/p/op/M.predict:d/x/y": "weft:///e/p/function/M.predict:d/x/y",
	} {
		r, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		if r.String() != want {
			t.Errorf("Parse(%q) = %q, want %q", in, r.String(), want)
		}
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
		if v := Variants(want); len(v) != 2 || v[0] != want || v[1] != in {
			t.Errorf("Variants(%q) = %v", want, v)
		}
		if v := Variants(in); len(v) != 2 || v[0] != want || v[1] != in {
			t.Errorf("Variants(%q) = %v", in, v)
		}
	}
	for _, s := range []string{"weft:///e/p/object/x:d", "not a ref", "weft:///e/p/table/t"} {
		if Normalize(s) != s || len(Variants(s)) != 1 {
			t.Errorf("Normalize/Variants changed %q", s)
		}
	}
	if FunctionRef := ObjectRef("e/p", "f", "d", true); FunctionRef != "weft:///e/p/function/f:d" {
		t.Errorf("ObjectRef(function) = %q", FunctionRef)
	}
	if got := SpanRef("e/p", "s1"); got != "weft:///e/p/span/s1" {
		t.Errorf("SpanRef = %q", got)
	}
}

// Weft's digest is the hex SHA-256 of the canonical JSON (DESIGN §10.3).
func TestWeftDigest(t *testing.T) {
	v, _ := Decode([]byte(`{"b": [1, 2.50, null, true, false], "a": "héllo ☃ 😀 \"q\"\n\u0001<&>\u2028", "c": {"z": 1e-07, "y": 1.0}, "é": {}, "Z": []}`))
	got, err := WeftJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"Z":[],"a":"héllo ☃ 😀 \"q\"\n\u0001<&>` + "\u2028" + `","b":[1,2.50,null,true,false],"c":{"y":1.0,"z":1e-07},"é":{}}`
	want = strings.Replace(want, `\u2028`, "\u2028", 1)
	want = strings.Replace(want, "\u2028", " ", 1)
	if string(got) != want {
		t.Fatalf("WeftJSON:\n got %s\nwant %s", got, want)
	}
	d, _, err := Digest(v)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(want))
	if d != hex.EncodeToString(sum[:]) {
		t.Fatalf("Digest = %s, want sha256(canonical) = %s", d, hex.EncodeToString(sum[:]))
	}
	// Go values: floats as encoding/json writes them, ints as decimal.
	g, _ := WeftJSON(map[string]any{"f": 0.5, "i": 3, "e": 1e-9, "big": 1e21, "s": []string{"x"}})
	if string(g) != `{"big":1e+21,"e":1e-9,"f":0.5,"i":3,"s":["x"]}` {
		t.Fatalf("WeftJSON(go values) = %s", g)
	}
	// Python's json.dumps(v, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
	// of {"k": "v", "n": [1, 2]}, hashed with SHA-256:
	py, _ := Decode([]byte(`{"n": [1, 2], "k": "v"}`))
	if d, _, _ := Digest(py); d != "fc8bda0be48b2ab9a7f766eef1f21af4e4e2c9e156a1b7d937686c456f9fbc33" {
		t.Fatalf("Digest({k,n}) = %s, want Python's", d)
	}
	// Tables: hex SHA-256 of the canonical JSON list of row digests.
	td := TableDigest([]string{"r1", "r2"})
	sum2 := sha256.Sum256([]byte(`["r1","r2"]`))
	if td != hex.EncodeToString(sum2[:]) {
		t.Fatalf("TableDigest = %s", td)
	}
	sum3 := sha256.Sum256([]byte(`[]`))
	if TableDigest([]string{}) != hex.EncodeToString(sum3[:]) {
		t.Fatal("empty table digest")
	}
	// Files: hex SHA-256 of the bytes.
	sum4 := sha256.Sum256([]byte("hello"))
	if DigestBytes([]byte("hello")) != hex.EncodeToString(sum4[:]) {
		t.Fatal("DigestBytes")
	}
}

type fixedDigester struct{}

func (fixedDigester) Value(any) (string, error) { return "v", nil }
func (fixedDigester) Bytes([]byte) string       { return "b" }
func (fixedDigester) Table([]string) string     { return "t" }

func TestDigesterFromContext(t *testing.T) {
	if DigesterFrom(context.Background()) != Weft {
		t.Fatal("the default digester is Weft's")
	}
	ctx := WithDigester(context.Background(), fixedDigester{})
	if d, _ := DigesterFrom(ctx).Value(1); d != "v" {
		t.Fatal("WithDigester not used")
	}
	if d, b, _ := DigestWith(fixedDigester{}, map[string]any{"a": 1}); d != "v" || string(b) != `{"a":1}` {
		t.Fatalf("DigestWith = %s %s", d, b)
	}
}
