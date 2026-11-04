package refs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Weft's digests (docs/weft/DESIGN.md §10.3):
//
//   - values (objects and table rows): the lowercase hex SHA-256 of the
//     value's canonical JSON (WeftJSON);
//   - tables: the lowercase hex SHA-256 of the canonical JSON of the
//     ordered list of row digests;
//   - files: the lowercase hex SHA-256 of the bytes.
//
// Digests written by earlier versions (another encoding) stay valid: they
// are looked up as stored and never recomputed. The compatibility adapter
// computes that older form for its client's requests, through a Digester
// it puts in the request context.

// Digester computes digests. Weft is the default; WithDigester selects
// another one for a request.
type Digester interface {
	// Value returns the digest of a JSON value.
	Value(v any) (string, error)
	// Bytes returns the digest of file content.
	Bytes(b []byte) string
	// Table returns the digest of a table from its ordered row digests.
	Table(rowDigests []string) string
}

// Weft is Weft's own digest scheme.
var Weft Digester = weftDigester{}

type weftDigester struct{}

func (weftDigester) Value(v any) (string, error) {
	b, err := WeftJSON(v)
	if err != nil {
		return "", err
	}
	return hexSHA256(b), nil
}

func (weftDigester) Bytes(b []byte) string { return hexSHA256(b) }

func (weftDigester) Table(rowDigests []string) string {
	b, _ := WeftJSON(rowDigests)
	return hexSHA256(b)
}

func hexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type digesterKey struct{}

// WithDigester returns a context whose writes use d for new digests.
func WithDigester(ctx context.Context, d Digester) context.Context {
	return context.WithValue(ctx, digesterKey{}, d)
}

// DigesterFrom returns the context's digester, or Weft.
func DigesterFrom(ctx context.Context) Digester {
	if ctx != nil {
		if d, ok := ctx.Value(digesterKey{}).(Digester); ok && d != nil {
			return d
		}
	}
	return Weft
}

// Digest returns v's Weft digest and v's compact canonical JSON (the text
// stored for it).
func Digest(v any) (string, []byte, error) {
	return DigestWith(Weft, v)
}

// DigestWith is Digest with another digester.
func DigestWith(d Digester, v any) (string, []byte, error) {
	b, err := CanonicalJSON(v)
	if err != nil {
		return "", nil, err
	}
	dg, err := d.Value(v)
	if err != nil {
		return "", nil, err
	}
	return dg, b, nil
}

// DigestBytes is the Weft digest of file content.
func DigestBytes(b []byte) string { return Weft.Bytes(b) }

// TableDigest is the Weft digest of a table from its ordered row digests.
func TableDigest(rowDigests []string) string { return Weft.Table(rowDigests) }

// WeftJSON encodes v as Weft's canonical JSON, the text its digests hash:
//
//   - object keys sorted by Unicode code point (byte order of their UTF-8);
//   - no insignificant whitespace (separators "," and ":");
//   - strings as UTF-8, escaping only '"', '\' and U+0000..U+001F (as \b,
//     \f, \n, \r, \t, or \u00xx with lowercase hex); invalid UTF-8 becomes
//     U+FFFD;
//   - numbers keep the text they were sent with (values decoded with
//     UseNumber); Go floats use the shortest representation that round-trips
//     (strconv 'g' style, as encoding/json writes them), integers are
//     decimal;
//   - true, false and null.
//
// This is what Python's json.dumps(v, sort_keys=True, separators=(",", ":"),
// ensure_ascii=False) writes for the same text, and what JavaScript's
// JSON.stringify writes for a value whose object keys are sorted (except
// for keys outside the Basic Multilingual Plane, which JavaScript orders by
// UTF-16 code unit).
func WeftJSON(v any) ([]byte, error) {
	var b strings.Builder
	if err := weftEncode(&b, v, 0); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

const maxCanonicalDepth = 10000

func weftEncode(b *strings.Builder, v any, depth int) error {
	if depth > maxCanonicalDepth {
		return fmt.Errorf("value nested too deeply")
	}
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		weftString(b, t)
	case json.Number:
		b.WriteString(t.String())
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return fmt.Errorf("unsupported number %v", t)
		}
		b.WriteString(goFloat(t))
	case float32:
		f := float64(t)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return fmt.Errorf("unsupported number %v", t)
		}
		b.WriteString(goFloat(f))
	case int:
		b.WriteString(strconv.Itoa(t))
	case int64:
		b.WriteString(strconv.FormatInt(t, 10))
	case []any:
		b.WriteByte('[')
		for i, x := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := weftEncode(b, x, depth+1); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case []string:
		b.WriteByte('[')
		for i, x := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			weftString(b, x)
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys) // byte order of UTF-8 = code point order
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			weftString(b, k)
			b.WriteByte(':')
			if err := weftEncode(b, t[k], depth+1); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		raw, err := json.Marshal(t)
		if err != nil {
			return err
		}
		x, err := Decode(raw)
		if err != nil {
			return err
		}
		return weftEncode(b, x, depth+1)
	}
	return nil
}

func weftString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20:
			fmt.Fprintf(b, `\u%04x`, r)
		default:
			b.WriteRune(r) // utf8.RuneError for invalid bytes: U+FFFD
		}
	}
	b.WriteByte('"')
}

// goFloat formats f like encoding/json does.
func goFloat(f float64) string {
	abs := math.Abs(f)
	fmtByte := byte('f')
	if abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		fmtByte = 'e'
	}
	s := strconv.FormatFloat(f, fmtByte, -1, 64)
	if fmtByte == 'e' {
		// clean up e-09 to e-9
		n := len(s)
		if n >= 4 && s[n-4] == 'e' && s[n-3] == '-' && s[n-2] == '0' {
			s = s[:n-2] + s[n-1:]
		}
	}
	return s
}
