package legacy

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/agentshield-ai/agentshield/internal/weft/refs"
)

// The digest form Weft used before it had its own (docs/weft/DESIGN.md
// §10.3), which the weave client computes itself:
//
//   - bytes: SHA-256, URL-safe base64 with '-'→'X', '_'→'Y', no padding;
//   - values (objects and table rows): that digest of Python's
//     json.dumps(v, sort_keys=True);
//   - tables: hex SHA-256 of the concatenated row digests.
//
// Digests stored in this form stay valid and are looked up as stored; the
// compatibility adapter's routes compute new ones in this form (Digester).

// Digester computes the legacy digest form.
var Digester refs.Digester = digester{}

type digester struct{}

func (digester) Value(v any) (string, error) {
	pj, err := DigestJSON(v)
	if err != nil {
		return "", err
	}
	return DigestBytes(pj), nil
}

func (digester) Bytes(b []byte) string            { return DigestBytes(b) }
func (digester) Table(rowDigests []string) string { return TableDigest(rowDigests) }

// DigestBytes is the legacy digest of b.
func DigestBytes(b []byte) string {
	sum := sha256.Sum256(b)
	enc := base64.RawURLEncoding.EncodeToString(sum[:])
	return strings.NewReplacer("-", "X", "_", "Y").Replace(enc)
}

// TableDigest is the legacy digest of a table from its ordered row digests.
func TableDigest(rowDigests []string) string {
	h := sha256.New()
	for _, d := range rowDigests {
		h.Write([]byte(d))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// DigestJSON encodes v exactly like Python's json.dumps(v, sort_keys=True):
// ", " and ": " separators, keys sorted by code point, non-ASCII escaped as
// \uXXXX (with surrogate pairs). json.Number values keep their text.
func DigestJSON(v any) ([]byte, error) {
	var b strings.Builder
	if err := pyEncode(&b, v); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

func pyEncode(b *strings.Builder, v any) error {
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
		pyString(b, t)
	case json.Number:
		b.WriteString(t.String())
	case float64:
		b.WriteString(pyFloat(t))
	case float32:
		b.WriteString(pyFloat(float64(t)))
	case int:
		b.WriteString(strconv.Itoa(t))
	case int64:
		b.WriteString(strconv.FormatInt(t, 10))
	case []any:
		b.WriteByte('[')
		for i, x := range t {
			if i > 0 {
				b.WriteString(", ")
			}
			if err := pyEncode(b, x); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case []string:
		b.WriteByte('[')
		for i, x := range t {
			if i > 0 {
				b.WriteString(", ")
			}
			pyString(b, x)
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return pyLess(keys[i], keys[j]) })
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			pyString(b, k)
			b.WriteString(": ")
			if err := pyEncode(b, t[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		// Fall back to a JSON round trip for other Go types.
		raw, err := json.Marshal(t)
		if err != nil {
			return err
		}
		x, err := refs.Decode(raw)
		if err != nil {
			return err
		}
		return pyEncode(b, x)
	}
	return nil
}

// pyLess orders strings by Unicode code point, as Python's sorted() does.
func pyLess(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	for i := 0; i < len(ra) && i < len(rb); i++ {
		if ra[i] != rb[i] {
			return ra[i] < rb[i]
		}
	}
	return len(ra) < len(rb)
}

func pyString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r < 0x20 || (r >= 0x7f && r <= 0xffff): // Python escapes everything outside ' '..'~'
			fmt.Fprintf(b, `\u%04x`, r)
		case r > 0xffff:
			r -= 0x10000
			fmt.Fprintf(b, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}

// pyFloat formats a float like Python's repr().
func pyFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	abs := math.Abs(f)
	if abs != 0 && (abs < 1e-4 || abs >= 1e16) {
		s := strconv.FormatFloat(f, 'e', -1, 64)
		mant, exp, _ := strings.Cut(s, "e")
		sign := exp[0]
		digits := strings.TrimLeft(exp[1:], "0")
		if len(digits) < 2 {
			digits = strings.Repeat("0", 2-len(digits)) + digits
		}
		return mant + "e" + string(sign) + digits
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.ContainsAny(s, ".") {
		s += ".0"
	}
	return s
}
