package legacy

import (
	"bytes"
	"regexp"
)

// RefKinds maps Weft's ref kinds to the names they had before Weft's own
// API (docs/weft/DESIGN.md §10.2): "function" was "op" and "span" was
// "call". Weft reads the old names everywhere (package refs); only the
// adapter writes them, for clients that know no other.
var RefKinds = map[string]string{"function": "op", "span": "call"}

var refKindRe = regexp.MustCompile(`(weft:///[^/"\\\s]+/[^/"\\\s]+/)(function|span)/`)

// RefKindsOut rewrites refs in a JSON (or NDJSON) body to the old kind
// names: weft:///e/p/function/... becomes weft:///e/p/op/... and
// weft:///e/p/span/... becomes weft:///e/p/call/....
func RefKindsOut(body []byte) []byte {
	if !bytes.Contains(body, []byte("/function/")) && !bytes.Contains(body, []byte("/span/")) {
		return body
	}
	return refKindRe.ReplaceAllFunc(body, func(m []byte) []byte {
		sub := refKindRe.FindSubmatch(m)
		out := append([]byte{}, sub[1]...)
		out = append(out, RefKinds[string(sub[2])]...)
		return append(out, '/')
	})
}
