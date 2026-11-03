package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Named-entity (NER) PII redaction at ingest.
//
// When the server has a PII analyzer (--pii-analyzer-url, Presidio Analyzer's
// REST API) and a project's sensitive-data policy is on, the strings of every
// incoming call are sent to the analyzer before the write transaction and the
// PERSON, LOCATION, ORGANIZATION (and NRP, when selected) spans it finds are
// replaced with <ENTITY> placeholders. The regex detectors then run as usual
// inside the transaction. Analyzer requests have a short timeout; on any
// failure the call is stored with regex redaction only, and the analyzer is
// skipped for a short back-off period so a dead analyzer does not add its
// timeout to every request.

// NEREntities are the named-entity types the analyzer can redact.
var NEREntities = []*RedactionEntity{
	{Name: "PERSON", Placeholder: "<PERSON>", Description: "People's names (NER, via the PII analyzer)", Kind: "ner", defaultOn: true},
	{Name: "LOCATION", Placeholder: "<LOCATION>", Description: "Locations: countries, cities, addresses, facilities (NER, via the PII analyzer)", Kind: "ner", defaultOn: true},
	{Name: "ORGANIZATION", Placeholder: "<ORGANIZATION>", Description: "Organisation names (NER, via the PII analyzer)", Kind: "ner", defaultOn: true},
	{Name: "NRP", Placeholder: "<NRP>", Description: "Nationalities, religious or political groups (NER, via the PII analyzer; opt-in)", Kind: "ner"},
}

func init() {
	for _, e := range RedactionEntities {
		if e.Kind == "" {
			e.Kind = "regex"
		}
	}
}

// AnalyzeRequest is one analyzer request (Presidio's /analyze body).
type AnalyzeRequest struct {
	Text           string   `json:"text"`
	Language       string   `json:"language"`
	Entities       []string `json:"entities,omitempty"`
	ScoreThreshold float64  `json:"score_threshold"`
}

// EntitySpan is one entity found by the analyzer. Start and End are
// character (Unicode code point) offsets, as Presidio reports them.
type EntitySpan struct {
	EntityType string  `json:"entity_type"`
	Start      int     `json:"start"`
	End        int     `json:"end"`
	Score      float64 `json:"score"`
}

// PIIAnalyzer finds named entities in text.
type PIIAnalyzer interface {
	Analyze(ctx context.Context, req AnalyzeRequest) ([]EntitySpan, error)
}

// PresidioAnalyzer calls a Presidio Analyzer REST service (POST /analyze).
type PresidioAnalyzer struct {
	URL    string
	Client *http.Client
}

// NewPresidioAnalyzer returns an analyzer for baseURL ("http://host:5002";
// "/analyze" is appended unless present).
func NewPresidioAnalyzer(baseURL string) *PresidioAnalyzer {
	u := strings.TrimRight(baseURL, "/")
	if !strings.HasSuffix(u, "/analyze") {
		u += "/analyze"
	}
	// The analyzer URL is operator configuration (--pii-analyzer-url) and is
	// usually a local container, so it does not go through the egress guard.
	return &PresidioAnalyzer{URL: u, Client: &http.Client{}}
}

// Analyze implements PIIAnalyzer.
func (p *PresidioAnalyzer) Analyze(ctx context.Context, req AnalyzeRequest) ([]EntitySpan, error) {
	body, _ := json.Marshal(req)
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	resp, err := p.Client.Do(hr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("analyzer returned %d: %s", resp.StatusCode, strings.TrimSpace(string(data[:min(len(data), 200)])))
	}
	var out []EntitySpan
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("analyzer response: %w", err)
	}
	return out, nil
}

// piiState is the store's analyzer with its timeout and failure back-off.
type piiState struct {
	analyzer PIIAnalyzer
	timeout  time.Duration
	backoff  time.Duration

	mu        sync.Mutex
	skipUntil time.Time
	lastLog   time.Time
}

// SetPIIAnalyzer turns on NER redaction for projects whose sensitive-data
// policy is on. timeout bounds each ingest's analyzer requests (default 2s).
func (s *Store) SetPIIAnalyzer(a PIIAnalyzer, timeout time.Duration) {
	if a == nil {
		s.pii = nil
		return
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	s.pii = &piiState{analyzer: a, timeout: timeout, backoff: 30 * time.Second}
	s.invalidateIngest("")
}

// HasPIIAnalyzer reports whether NER redaction is available.
func (s *Store) HasPIIAnalyzer() bool { return s.pii != nil }

func (p *piiState) available() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return time.Now().After(p.skipUntil)
}

func (p *piiState) failed(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.skipUntil = time.Now().Add(p.backoff)
	if time.Since(p.lastLog) > time.Minute {
		p.lastLog = time.Now()
		slog.Warn("PII analyzer failed; storing calls with regex redaction only", "err", err, "retry_after", p.backoff)
	}
}

// ---------------------------------------------------------------- string slots

// slot is one string inside a decoded JSON value, replaceable in place.
type slot struct {
	m   map[string]any
	k   string
	a   []any
	i   int
	p   *string
	end *CallEnd // a string output
	val string
}

func (sl *slot) set(v string) {
	switch {
	case sl.p != nil:
		*sl.p = v
	case sl.end != nil:
		sl.end.Output = v
	case sl.m != nil:
		sl.m[sl.k] = v
	case sl.a != nil:
		sl.a[sl.i] = v
	}
}

// nerCandidate reports whether a string can contain a named entity worth
// sending to the analyzer (refs, ids, numbers and very short strings cannot).
func nerCandidate(s string) bool {
	if len(s) < 3 || strings.HasPrefix(s, "weft:///") || !utf8.ValidString(s) {
		return false
	}
	upper := 0
	for _, r := range s {
		if unicode.IsUpper(r) {
			upper++
		}
	}
	// NER models key on capitalised tokens; all-lowercase text rarely holds a name
	// they would find, but keep it for languages without case.
	if upper == 0 {
		for _, r := range s {
			if unicode.IsLetter(r) && !unicode.Is(unicode.Latin, r) {
				return true
			}
		}
		return false
	}
	return true
}

func collectSlots(v any, out *[]*slot) {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			if s, ok := x.(string); ok {
				if nerCandidate(s) {
					*out = append(*out, &slot{m: t, k: k, val: s})
				}
				continue
			}
			collectSlots(x, out)
		}
	case []any:
		for i, x := range t {
			if s, ok := x.(string); ok {
				if nerCandidate(s) {
					*out = append(*out, &slot{a: t, i: i, val: s})
				}
				continue
			}
			collectSlots(x, out)
		}
	}
}

// replaceEntities replaces the spans (code point offsets into s) with
// placeholders. Overlapping spans are merged into the earliest one.
func replaceEntities(s string, spans []EntitySpan) string {
	if len(spans) == 0 {
		return s
	}
	rs := []rune(s)
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].Start != spans[j].Start {
			return spans[i].Start < spans[j].Start
		}
		return spans[i].End > spans[j].End
	})
	var b strings.Builder
	pos := 0
	for _, sp := range spans {
		st, en := max(sp.Start, 0), min(sp.End, len(rs))
		if st < pos || en <= st {
			continue
		}
		b.WriteString(string(rs[pos:st]))
		b.WriteString("<" + strings.ToUpper(sp.EntityType) + ">")
		pos = en
	}
	b.WriteString(string(rs[pos:]))
	return b.String()
}

const (
	nerSeparator  = "\n\n\n"
	nerChunkRunes = 50_000
)

// redactSlots sends the slots' strings to the analyzer (joined into chunks of
// at most nerChunkRunes characters, one request each) and rewrites them.
func (p *piiState) redactSlots(ctx context.Context, rd *Redactor, slots []*slot) error {
	if len(slots) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	sepLen := utf8.RuneCountInString(nerSeparator)
	for start := 0; start < len(slots); {
		// Build one chunk: [start, end) slots, their code point offsets.
		var b strings.Builder
		offsets := []int{}
		n := 0
		end := start
		for end < len(slots) {
			l := utf8.RuneCountInString(slots[end].val)
			if end > start && n+sepLen+l > nerChunkRunes {
				break
			}
			if end > start {
				b.WriteString(nerSeparator)
				n += sepLen
			}
			offsets = append(offsets, n)
			b.WriteString(slots[end].val)
			n += l
			end++
		}
		found, err := p.analyzer.Analyze(ctx, AnalyzeRequest{Text: b.String(), Language: rd.nerLang, Entities: rd.ner, ScoreThreshold: rd.nerScore})
		if err != nil {
			return err
		}
		allowed := map[string]bool{}
		for _, e := range rd.ner {
			allowed[e] = true
		}
		per := make([][]EntitySpan, end-start)
		for _, f := range found {
			if !allowed[strings.ToUpper(f.EntityType)] || f.Score < rd.nerScore {
				continue
			}
			// The slot whose range holds the span start; clip the span to it.
			i := sort.Search(len(offsets), func(i int) bool { return offsets[i] > f.Start }) - 1
			if i < 0 {
				continue
			}
			l := utf8.RuneCountInString(slots[start+i].val)
			sp := EntitySpan{EntityType: f.EntityType, Start: f.Start - offsets[i], End: min(f.End-offsets[i], l), Score: f.Score}
			if sp.Start < l && sp.End > sp.Start {
				per[i] = append(per[i], sp)
			}
		}
		for i, spans := range per {
			if len(spans) > 0 {
				sl := slots[start+i]
				sl.val = replaceEntities(sl.val, spans)
				sl.set(sl.val)
			}
		}
		start = end
	}
	return nil
}

// nerRedact applies NER redaction to calls about to be stored. It never
// fails: on analyzer errors the calls keep their text (regex redaction still
// runs in the ingest hook).
func (s *Store) nerRedact(ctx context.Context, starts []*CallStart, ends []*CallEnd) {
	p := s.pii
	if p == nil || (len(starts) == 0 && len(ends) == 0) || !p.available() {
		return
	}
	byProject := map[string][]*slot{}
	policies := map[string]*Redactor{}
	policy := func(pid string) *Redactor {
		if rd, ok := policies[pid]; ok {
			return rd
		}
		var rd *Redactor
		if pol, err := s.ingestPolicyFor(ctx, s.r, pid); err == nil && pol.redactor != nil && len(pol.redactor.ner) > 0 {
			rd = pol.redactor
		}
		policies[pid] = rd
		return rd
	}
	for _, st := range starts {
		if st == nil || policy(st.ProjectID) == nil {
			continue
		}
		var sl []*slot
		collectSlots(st.Inputs, &sl)
		collectSlots(st.Attributes, &sl)
		byProject[st.ProjectID] = append(byProject[st.ProjectID], sl...)
	}
	for _, en := range ends {
		if en == nil || policy(en.ProjectID) == nil {
			continue
		}
		var sl []*slot
		switch o := en.Output.(type) {
		case string:
			if nerCandidate(o) {
				sl = append(sl, &slot{end: en, val: o})
			}
		default:
			collectSlots(o, &sl)
		}
		if en.Exception != nil && nerCandidate(*en.Exception) {
			sl = append(sl, &slot{p: en.Exception, val: *en.Exception})
		}
		byProject[en.ProjectID] = append(byProject[en.ProjectID], sl...)
	}
	for pid, sl := range byProject {
		if err := p.redactSlots(ctx, policies[pid], sl); err != nil {
			p.failed(err)
			return
		}
	}
}

// RedactPreview redacts sample input with the given (unsaved) settings,
// including NER entities when the server has an analyzer. ner reports whether
// the analyzer was used.
func (s *Store) RedactPreview(ctx context.Context, set SensitiveDataSettings, text string, value any) (string, any, bool, error) {
	rd, err := NewRedactor(set)
	if err != nil {
		return "", nil, false, err
	}
	used := false
	if s.pii != nil && len(rd.ner) > 0 {
		var sl []*slot
		ts := &slot{p: &text, val: text}
		if nerCandidate(text) {
			sl = append(sl, ts)
		}
		wrap := map[string]any{"v": value}
		collectSlots(wrap, &sl)
		if err := s.pii.redactSlots(ctx, rd, sl); err == nil {
			used = true
			value = wrap["v"]
		}
	}
	return rd.String(text), rd.Value(value), used, nil
}
