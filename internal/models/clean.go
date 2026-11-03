package models

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode/utf8"
)

// Limits of the descriptive values a verdict carries to Weft, by push
// (/api/v1/shield/ingest) and on the evaluate span. Rules are operator
// configuration that the engine loads whatever their ids and tags hold, so
// these values are cleaned (CleanText, CleanTags, ShortID), never rejected:
// a block on such a rule must still reach Weft. Weft applies the same
// functions, and they leave a value they already produced unchanged, so a
// verdict that arrives both ways is stored once with the same rule ids.
const (
	MaxRuleIDLength    = 256
	MaxRuleTitleLength = 1024
	MaxRuleTags        = 64
	MaxRuleTagLength   = 256
	// MaxEventSourceLength bounds the event source (the request's `source`)
	// Weft stores.
	MaxEventSourceLength = 128
)

// shortIDHashMarker joins the kept prefix of a shortened id to its hash.
const shortIDHashMarker = "~sha256:"

// isCtl reports the control characters values sent to Weft may not hold
// (Weft's own definition: C0 controls and DEL).
func isCtl(r rune) bool { return r < 0x20 || r == 0x7f }

// ShortID returns id unchanged when it is valid UTF-8 of at most max bytes
// without control characters. Otherwise it returns a stand-in of at most
// max bytes that is stable (the same id always gives the same stand-in) and
// unique in practice: the id's leading part (control characters replaced by
// '_'), "~sha256:" and the hex SHA-256 of the whole id. A stand-in is itself
// a valid id, so ShortID(ShortID(id)) == ShortID(id).
func ShortID(id string, max int) string {
	if len(id) <= max && utf8.ValidString(id) && !strings.ContainsFunc(id, isCtl) {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	suffix := shortIDHashMarker + hex.EncodeToString(sum[:])
	keep := max - len(suffix)
	if keep < 0 {
		// Too small a max for a prefix and a full hash: the hash alone, cut.
		return suffix[:max]
	}
	prefix := strings.Map(func(r rune) rune {
		if isCtl(r) {
			return '_'
		}
		return r
	}, strings.ToValidUTF8(id, "�"))
	return cutUTF8(prefix, keep) + suffix
}

// CleanText makes a descriptive value (a name, title, label or tag, never
// an id) safe to send and store: control characters become spaces, runs of
// whitespace around them collapse, surrounding space is trimmed and the
// value is cut to max bytes on a rune boundary. Invalid UTF-8 is replaced.
func CleanText(v string, max int) string {
	if !utf8.ValidString(v) {
		v = strings.ToValidUTF8(v, "�")
	}
	if strings.ContainsFunc(v, isCtl) {
		v = strings.Join(strings.FieldsFunc(v, func(r rune) bool { return isCtl(r) || r == ' ' }), " ")
	}
	return cutUTF8(strings.TrimSpace(v), max)
}

// CleanTags cleans each rule tag (CleanText, MaxRuleTagLength), drops the
// empty ones and keeps the first MaxRuleTags. It never returns nil.
func CleanTags(tags []string) []string {
	out := make([]string, 0, min(len(tags), MaxRuleTags))
	for _, t := range tags {
		if len(out) == MaxRuleTags {
			break
		}
		if t = CleanText(t, MaxRuleTagLength); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// CleanRule returns a matched rule's id, title and tags as the engine sends
// them to Weft (push and evaluate span alike): the id shortened (ShortID),
// the title cut with a marker (Truncate) and the tags cleaned (CleanTags).
func CleanRule(id, title string, tags []string) (string, string, []string) {
	return ShortID(id, MaxRuleIDLength), Truncate(title, MaxRuleTitleLength), CleanTags(tags)
}

// TruncateMarker ends a value that was cut to fit a size limit.
const TruncateMarker = "…[truncated]"

// Truncate cuts v to at most max bytes on a rune boundary, ending it with
// TruncateMarker when it was cut (and max leaves room for the marker).
func Truncate(v string, max int) string {
	if len(v) <= max {
		return v
	}
	if max <= len(TruncateMarker) {
		return cutUTF8(v, max)
	}
	return cutUTF8(v, max-len(TruncateMarker)) + TruncateMarker
}

// cutUTF8 cuts v to at most max bytes without splitting a rune.
func cutUTF8(v string, max int) string {
	if len(v) <= max {
		return v
	}
	if max <= 0 {
		return ""
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(v[cut]) {
		cut--
	}
	return v[:cut]
}
