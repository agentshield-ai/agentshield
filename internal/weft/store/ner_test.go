package store

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type wordAnalyzer struct {
	words map[string]string
	reqs  int
}

func (w *wordAnalyzer) Analyze(_ context.Context, req AnalyzeRequest) ([]EntitySpan, error) {
	w.reqs++
	var out []EntitySpan
	for word, typ := range w.words {
		from := 0
		for {
			i := strings.Index(req.Text[from:], word)
			if i < 0 {
				break
			}
			b := from + i
			st := utf8.RuneCountInString(req.Text[:b])
			out = append(out, EntitySpan{EntityType: typ, Start: st, End: st + utf8.RuneCountInString(word), Score: 0.9})
			from = b + len(word)
		}
	}
	return out, nil
}

func TestNERChunksAndOffsets(t *testing.T) {
	an := &wordAnalyzer{words: map[string]string{"Émile Zola": "PERSON", "Zürich": "LOCATION"}}
	p := &piiState{analyzer: an, timeout: time.Second, backoff: time.Second}
	rd, err := NewRedactor(SensitiveDataSettings{Policy: "pii-v1"})
	if err != nil {
		t.Fatal(err)
	}
	// Enough strings to need several analyzer requests.
	val := map[string]any{}
	items := []any{}
	for i := 0; i < 3000; i++ {
		items = append(items, strings.Repeat("ü", 30)+" Émile Zola visited Zürich")
	}
	val["items"] = items
	val["Zürich"] = "Key stays, value Zürich goes"
	var sl []*slot
	collectSlots(val, &sl)
	if err := p.redactSlots(context.Background(), rd, sl); err != nil {
		t.Fatal(err)
	}
	if an.reqs < 2 {
		t.Fatalf("expected chunking, got %d requests", an.reqs)
	}
	for i, x := range items {
		if x != strings.Repeat("ü", 30)+" <PERSON> visited <LOCATION>" {
			t.Fatalf("item %d = %q", i, x)
		}
	}
	if val["Zürich"] != "Key stays, value <LOCATION> goes" {
		t.Fatalf("map value = %v", val["Zürich"])
	}
	if got := replaceEntities("ab Émile cd", []EntitySpan{{EntityType: "person", Start: 3, End: 8}, {EntityType: "X", Start: 4, End: 6}}); got != "ab <PERSON> cd" {
		t.Fatalf("overlap = %q", got)
	}
}
