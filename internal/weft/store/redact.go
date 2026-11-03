package store

import (
	"regexp"
	"strings"
)

// Server-side PII redaction applied at ingest (sensitive_data_settings).
// Each detector replaces matches with a <ENTITY> placeholder, as Presidio does.

// RedactionEntity is one built-in detector.
type RedactionEntity struct {
	Name        string `json:"name"`
	Placeholder string `json:"placeholder"`
	Description string `json:"description"`
	// Kind is "regex" for built-in pattern detectors and "ner" for named
	// entities found by the PII analyzer (see ner.go).
	Kind      string `json:"kind,omitempty"`
	re        *regexp.Regexp
	check     func(string) bool
	defaultOn bool // NER entities: redacted when the project lists no entities
}

// RedactionEntities lists the built-in detectors in the order they run.
// API keys run first so token-shaped strings are not half-matched as phones.
var RedactionEntities = []*RedactionEntity{
	{Name: "API_KEY", Placeholder: "<API_KEY>", Description: "API keys and tokens (OpenAI, Anthropic, AWS, GitHub, Slack, Google, Stripe, JWT, Bearer)",
		re: regexp.MustCompile(`(?:\b(?:sk|pk|rk)-(?:ant-|proj-|live-|test-)?[A-Za-z0-9_\-]{16,}|\b(?:sk|pk|rk)_(?:live|test)_[A-Za-z0-9]{16,}|\bAKIA[0-9A-Z]{16}\b|\bgh[pousr]_[A-Za-z0-9]{30,}\b|\bgithub_pat_[A-Za-z0-9_]{40,}\b|\bxox[abposr]-[A-Za-z0-9\-]{10,}|\bAIza[0-9A-Za-z_\-]{35}\b|\beyJ[A-Za-z0-9_\-]{8,}\.eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}|(?i:\bbearer\s+)[A-Za-z0-9._~+/\-]{20,}=*)`)},
	{Name: "EMAIL_ADDRESS", Placeholder: "<EMAIL_ADDRESS>", Description: "Email addresses",
		re: regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)},
	{Name: "IBAN_CODE", Placeholder: "<IBAN_CODE>", Description: "International bank account numbers",
		re: regexp.MustCompile(`\b[A-Z]{2}[0-9]{2}(?: ?[A-Z0-9]{4}){2,7}(?: ?[A-Z0-9]{1,4})?\b`), check: validIBAN},
	{Name: "CREDIT_CARD", Placeholder: "<CREDIT_CARD>", Description: "Payment card numbers (Luhn-checked)",
		re: regexp.MustCompile(`\b(?:\d[ \-]?){12,18}\d\b`), check: luhn},
	{Name: "US_SSN", Placeholder: "<US_SSN>", Description: "US social security numbers",
		re: regexp.MustCompile(`\b(?:00[1-9]|0[1-9]\d|[1-578]\d\d|6[0-57-9]\d|66[0-57-9])-(?:0[1-9]|[1-9]\d)-(?:000[1-9]|00[1-9]\d|0[1-9]\d\d|[1-9]\d{3})\b`)},
	{Name: "PHONE_NUMBER", Placeholder: "<PHONE_NUMBER>", Description: "Phone numbers (international and North American formats)",
		re: regexp.MustCompile(`(?:\+\d{1,3}[ .\-]?)?(?:\(\d{2,4}\)[ .\-]?|\b\d{2,4}[ .\-])\d{3,4}[ .\-]\d{3,4}\b`)},
	{Name: "IP_ADDRESS", Placeholder: "<IP_ADDRESS>", Description: "IPv4 and IPv6 addresses",
		re: regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\b|\b(?:[0-9A-Fa-f]{1,4}:){7}[0-9A-Fa-f]{1,4}\b`)},
}

func luhn(s string) bool {
	sum, n, alt := 0, 0, false
	for i := len(s) - 1; i >= 0; i-- {
		c := s[i]
		if c < '0' || c > '9' {
			continue
		}
		d := int(c - '0')
		if alt {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		alt = !alt
		n++
	}
	return n >= 13 && n <= 19 && sum%10 == 0
}

func validIBAN(s string) bool {
	s = strings.ReplaceAll(s, " ", "")
	if len(s) < 15 || len(s) > 34 {
		return false
	}
	r := s[4:] + s[:4]
	mod := 0
	for _, c := range r {
		switch {
		case c >= '0' && c <= '9':
			mod = (mod*10 + int(c-'0')) % 97
		case c >= 'A' && c <= 'Z':
			v := int(c-'A') + 10
			mod = (mod*100 + v) % 97
		default:
			return false
		}
	}
	return mod == 1
}

// Redactor applies a project's redaction policy. String and Value apply the
// regex detectors; NER entities (ner) are applied by Store.nerRedact before
// the regex pass when the server has a PII analyzer.
type Redactor struct {
	entities []*RedactionEntity
	custom   []customPattern
	ner      []string
	nerScore float64
	nerLang  string
}

// NEREntities reports the named-entity types this policy redacts (empty when
// NER is off for the project).
func (r *Redactor) NEREntities() []string { return r.ner }

type customPattern struct {
	placeholder string
	re          *regexp.Regexp
}

var customNameRe = regexp.MustCompile(`^[A-Za-z0-9_\-]{1,64}$`)

// NewRedactor compiles settings; it validates entity names and custom regexes.
func NewRedactor(set SensitiveDataSettings) (*Redactor, error) {
	r := &Redactor{}
	want := map[string]bool{}
	for _, e := range set.Entities {
		want[strings.ToUpper(strings.TrimSpace(e))] = true
	}
	for name := range want {
		found := false
		for _, e := range RedactionEntities {
			found = found || e.Name == name
		}
		for _, e := range NEREntities {
			found = found || e.Name == name
		}
		if !found {
			return nil, Invalidf("unknown entity %q", name)
		}
	}
	for _, e := range RedactionEntities {
		if len(want) == 0 || want[e.Name] {
			r.entities = append(r.entities, e)
		}
	}
	if set.NER == nil || set.NER.Enabled == nil || *set.NER.Enabled {
		for _, e := range NEREntities {
			if (len(want) == 0 && e.defaultOn) || want[e.Name] {
				r.ner = append(r.ner, e.Name)
			}
		}
	}
	r.nerScore, r.nerLang = 0.5, "en"
	if set.NER != nil {
		if t := set.NER.ScoreThreshold; t != nil {
			if *t < 0 || *t > 1 {
				return nil, Invalidf("ner.score_threshold must be between 0 and 1")
			}
			r.nerScore = *t
		}
		if l := strings.TrimSpace(set.NER.Language); l != "" {
			if len(l) > 16 || !customNameRe.MatchString(l) {
				return nil, Invalidf("invalid ner.language %q", l)
			}
			r.nerLang = l
		}
	}
	if len(set.CustomPatterns) > 50 {
		return nil, Invalidf("at most 50 custom patterns")
	}
	for _, c := range set.CustomPatterns {
		if !customNameRe.MatchString(c.Name) {
			return nil, Invalidf("invalid custom pattern name %q (letters, digits, _ and -)", c.Name)
		}
		if c.Pattern == "" || len(c.Pattern) > 1000 {
			return nil, Invalidf("custom pattern %q is empty or too long", c.Name)
		}
		re, err := regexp.Compile(c.Pattern)
		if err != nil {
			return nil, Invalidf("custom pattern %q: %v", c.Name, err)
		}
		r.custom = append(r.custom, customPattern{"<" + strings.ToUpper(c.Name) + ">", re})
	}
	return r, nil
}

// String redacts one string. Refs are left alone.
func (r *Redactor) String(s string) string {
	if s == "" || strings.HasPrefix(s, "weft:///") {
		return s
	}
	for _, c := range r.custom {
		s = c.re.ReplaceAllString(s, c.placeholder)
	}
	for _, e := range r.entities {
		if e.check == nil {
			s = e.re.ReplaceAllLiteralString(s, e.Placeholder)
			continue
		}
		s = e.re.ReplaceAllStringFunc(s, func(m string) string {
			if e.check(m) {
				return e.Placeholder
			}
			return m
		})
	}
	return s
}

// Value redacts every string in a decoded JSON value (map keys are kept).
func (r *Redactor) Value(v any) any {
	switch t := v.(type) {
	case string:
		return r.String(t)
	case map[string]any:
		for k, x := range t {
			t[k] = r.Value(x)
		}
		return t
	case []any:
		for i, x := range t {
			t[i] = r.Value(x)
		}
		return t
	}
	return v
}
