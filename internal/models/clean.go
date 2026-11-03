package models

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
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
	// MaxRuleTagBytes bounds the total bytes of one rule's tags (after
	// cleaning): tags are kept in order while they fit.
	MaxRuleTagBytes = 2048
	// MaxRules is the most rules one verdict carries to Weft, the most
	// severe first (SelectRules). The engine applies it before both the push
	// and the evaluate span (whose event limit leaves room for it), and Weft
	// applies it to whatever it receives, so both copies of a verdict hold
	// the same rules. A verdict's severity still counts every matched rule.
	MaxRules = 64
	// MaxEventSourceLength bounds the event source (the request's `source`)
	// Weft stores.
	MaxEventSourceLength = 128
)

// GeneratedRuleIDPrefix starts the id given to a rule whose id is empty (or
// only whitespace): GeneratedRuleIDPrefix + the hex SHA-256 of its cleaned
// title (CleanRuleID).
const GeneratedRuleIDPrefix = "rule~sha256:"

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

// CleanRuleID returns the id a matched rule is stored under in Weft, by
// push, over OTLP and by replay alike: the id without surrounding space,
// shortened when it is too long or holds control characters (ShortID). A
// rule loaded with an empty or whitespace-only id gets a stable generated
// id, GeneratedRuleIDPrefix + the hex SHA-256 of its title as CleanText
// leaves it, so its detection is stored rather than rejected and all paths
// name it the same way. CleanRuleID(CleanRuleID(id, t), t) is
// CleanRuleID(id, t), and so is CleanRuleID(CleanRuleID(id, t), anything).
func CleanRuleID(id, title string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		sum := sha256.Sum256([]byte(CleanText(title, MaxRuleTitleLength)))
		return GeneratedRuleIDPrefix + hex.EncodeToString(sum[:])
	}
	return ShortID(id, MaxRuleIDLength)
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
// empty ones and keeps the first MaxRuleTags, stopping at the first tag that
// would take them past MaxRuleTagBytes in all. It never returns nil.
func CleanTags(tags []string) []string {
	out := make([]string, 0, min(len(tags), MaxRuleTags))
	size := 0
	for _, t := range tags {
		if len(out) == MaxRuleTags {
			break
		}
		if t = CleanText(t, MaxRuleTagLength); t != "" {
			if size+len(t) > MaxRuleTagBytes {
				break
			}
			size += len(t)
			out = append(out, t)
		}
	}
	return out
}

// CleanRule returns a matched rule's id, title and tags as the engine sends
// them to Weft (push and evaluate span alike): the id cleaned (CleanRuleID),
// the title cut with a marker (Truncate) and the tags cleaned (CleanTags).
func CleanRule(id, title string, tags []string) (string, string, []string) {
	return CleanRuleID(id, title), Truncate(title, MaxRuleTitleLength), CleanTags(tags)
}

// Rule is a matched rule as it reaches Weft.
type Rule struct {
	ID    string
	Title string
	Level string
	Tags  []string
}

// LevelRank orders rule levels (low 1 ... critical 4); anything else is 0.
func LevelRank(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "low", "informational", "info":
		return 1
	case "medium", "med":
		return 2
	case "high":
		return 3
	case "critical", "crit":
		return 4
	}
	return 0
}

// SelectRules is the one selection of a verdict's rules that the engine
// applies before it pushes the verdict and before it records it on the
// evaluate span, and that Weft applies to every detection it stores, so the
// pushed and the OTLP copy of a verdict (and a replay of it) hold the same
// rules in the same order. Rules must already be cleaned (CleanRule). A rule
// id that occurs more than once keeps its most severe entry (the first of
// equally severe ones); the rules are then ordered most severe first, in
// their order otherwise, and the first MaxRules are kept. It never returns
// nil, and SelectRules(SelectRules(r)) equals SelectRules(r).
func SelectRules(rules []Rule) []Rule {
	ids := make([]string, len(rules))
	levels := make([]string, len(rules))
	for i, r := range rules {
		ids[i], levels[i] = r.ID, r.Level
	}
	keep := SelectRuleIndexes(ids, levels)
	out := make([]Rule, 0, len(keep))
	for _, i := range keep {
		out = append(out, rules[i])
	}
	return out
}

// SelectRuleIndexes is SelectRules on cleaned ids and their levels: it
// returns the indexes of the rules SelectRules keeps, in its order, so a
// caller can clean only those rules' titles and tags.
func SelectRuleIndexes(ids, levels []string) []int {
	at := make(map[string]int, len(ids))
	all := make([]int, 0, len(ids))
	for i, id := range ids {
		if j, ok := at[id]; ok {
			if LevelRank(levels[i]) > LevelRank(levels[all[j]]) {
				all[j] = i
			}
			continue
		}
		at[id] = len(all)
		all = append(all, i)
	}
	sort.SliceStable(all, func(a, b int) bool { return LevelRank(levels[all[a]]) > LevelRank(levels[all[b]]) })
	return all[:min(len(all), MaxRules)]
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
