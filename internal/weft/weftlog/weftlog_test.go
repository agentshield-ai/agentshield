package weftlog

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5/middleware"
)

func TestNewHandlerFormats(t *testing.T) {
	for format, want := range map[string]string{"": "msg=hello", "text": "msg=hello", "JSON": `"msg":"hello"`} {
		var b bytes.Buffer
		h, err := NewHandler(&b, format, slog.LevelInfo)
		if err != nil {
			t.Fatal(err)
		}
		slog.New(h).Info("hello")
		if !strings.Contains(b.String(), want) {
			t.Fatalf("%q: %s", format, b.String())
		}
	}
	if _, err := NewHandler(&bytes.Buffer{}, "xml", slog.LevelInfo); err == nil {
		t.Fatal("xml accepted")
	}
}

func TestReasonElidesQuotedValues(t *testing.T) {
	got := Reason(`proto: syntax error (line 1:17): unexpected token "SECRET-VALUE"; invalid character 'x'; ""`)
	if strings.Contains(got, "SECRET") || !strings.Contains(got, `unexpected token "…"`) || !strings.Contains(got, `""`) {
		t.Fatalf("Reason = %q", got)
	}
	if long := Reason(strings.Repeat("a", 1000)); len(long) > 310 {
		t.Fatalf("not truncated: %d", len(long))
	}
	if Counts(map[string]int{"b": 2, "a": 1, "z": 0}) != "a=1,b=2" {
		t.Fatal("Counts")
	}
}

// RecordError and RecordReason find the request through wrapping writers.
func TestRecordThroughWrappers(t *testing.T) {
	req := &Request{}
	rec := &Recorder{ResponseWriter: httptest.NewRecorder(), Req: req}
	wrapped := middleware.NewWrapResponseWriter(rec, 1)
	if !RecordError(wrapped, errors.New("disk on fire")) {
		t.Fatal("not found through the wrapper")
	}
	RecordReason(wrapped, "bad input")
	RecordReason(wrapped, "second reason")
	wrapped.WriteHeader(http.StatusTeapot)
	s := req.Snapshot()
	if s.Err == nil || s.Reason != "bad input" || rec.Status != http.StatusTeapot {
		t.Fatalf("snapshot = %+v, status %d", s, rec.Status)
	}
	if RecordError(httptest.NewRecorder(), errors.New("x")) {
		t.Fatal("found a request without the request log")
	}
	req.AddProjects("a/b", "a/b", "c/d")
	if p := req.Snapshot().Projects; len(p) != 2 {
		t.Fatalf("projects = %v", p)
	}
}
