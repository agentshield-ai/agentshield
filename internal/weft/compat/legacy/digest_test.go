package legacy

import (
	"testing"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// Expected values computed independently with Python: json.dumps(v,
// sort_keys=True), SHA-256, URL-safe base64 with '-'→'X', '_'→'Y' and no
// padding; tables: hex SHA-256 of the concatenated row digests.
func TestDigestsMatchPythonJSON(t *testing.T) {
	v, _ := refs.Decode([]byte(`{"b": [1, 2.5, null, true], "a": "héllo ☃ 😀 \"q\"\n", "c": {"z": 1e-07, "y": 1.0}}`))
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

// The legacy digester gives the weave client's digests: base64url SHA-256
// of json.dumps(v, sort_keys=True), and the concatenated-row table digest.
func TestLegacyDigester(t *testing.T) {
	v, _ := refs.Decode([]byte(`{"b": 1, "a": "x"}`))
	d, err := Digester.Value(v)
	if err != nil {
		t.Fatal(err)
	}
	if d != DigestBytes([]byte(`{"a": "x", "b": 1}`)) || len(d) != 43 {
		t.Fatalf("legacy value digest %q", d)
	}
	if Digester.Bytes([]byte("hi")) != DigestBytes([]byte("hi")) {
		t.Fatal("legacy bytes digest")
	}
	if Digester.Table([]string{"a", "b"}) != TableDigest([]string{"a", "b"}) || TableDigest([]string{"a", "b"}) == refs.TableDigest([]string{"a", "b"}) {
		t.Fatal("legacy table digest")
	}
	if w, _, _ := refs.Digest(v); w == d {
		t.Fatal("legacy and Weft digests should differ")
	}
}

func TestRefKindsOut(t *testing.T) {
	in := `{"a":"weft:///e/p/function/f:d","b":"weft:///e/p/span/s1/attr/x","c":"weft:///e/p/object/o:d","d":"/span/ plain"}`
	want := `{"a":"weft:///e/p/op/f:d","b":"weft:///e/p/call/s1/attr/x","c":"weft:///e/p/object/o:d","d":"/span/ plain"}`
	if got := string(RefKindsOut([]byte(in))); got != want {
		t.Fatalf("RefKindsOut:\n got %s\nwant %s", got, want)
	}
}
