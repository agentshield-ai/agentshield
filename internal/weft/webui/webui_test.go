package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// directives parses a CSP header into directive -> sources.
func directives(csp string) map[string][]string {
	out := map[string][]string{}
	for _, d := range strings.Split(csp, ";") {
		f := strings.Fields(d)
		if len(f) > 0 {
			out[f[0]] = f[1:]
		}
	}
	return out
}

func TestHTMLSecurityHeaders(t *testing.T) {
	for _, path := range []string{"/", "/index.html", "/acme/proj/spans/abc", "/help/API.md"} {
		rec := get(t, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s -> %d", path, rec.Code)
		}
		h := rec.Header()
		if got := h.Get("Content-Security-Policy"); got != ContentSecurityPolicy {
			t.Fatalf("GET %s: CSP = %q", path, got)
		}
		if got := h.Get("Referrer-Policy"); got != "no-referrer" {
			t.Fatalf("GET %s: Referrer-Policy = %q", path, got)
		}
		if h.Get("X-Frame-Options") != "DENY" || h.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("GET %s: frame/nosniff headers = %v", path, h)
		}
	}
}

func TestContentSecurityPolicy(t *testing.T) {
	d := directives(ContentSecurityPolicy)
	want := map[string]string{
		"default-src":     "'self'",
		"script-src":      "'self'",
		"style-src":       "'self' 'unsafe-inline'",
		"img-src":         "'self' data: blob: http: https:",
		"media-src":       "'self' data: blob: http: https:",
		"font-src":        "'self' data:",
		"connect-src":     "'self'",
		"object-src":      "blob:",
		"frame-src":       "'self' blob:",
		"frame-ancestors": "'none'",
		"base-uri":        "'none'",
		"form-action":     "'self'",
	}
	for k, v := range want {
		if got := strings.Join(d[k], " "); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	// Recorded data must never be able to run script in Weft's origin.
	for _, k := range []string{"script-src", "default-src", "object-src", "frame-src", "connect-src"} {
		for _, src := range d[k] {
			switch src {
			case "'unsafe-inline'", "'unsafe-eval'", "data:", "*", "http:", "https:":
				t.Errorf("%s allows %s", k, src)
			}
		}
	}
}

// The built page must work under script-src 'self': no inline scripts or
// inline event handlers in index.html.
func TestIndexHasNoInlineScript(t *testing.T) {
	root, err := fs.Sub(assets, "static")
	if err != nil {
		t.Fatal(err)
	}
	b, err := fs.ReadFile(root, "index.html")
	if err != nil {
		t.Skip("UI not built (make weft-ui)")
	}
	html := string(b)
	for _, m := range regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`).FindAllStringSubmatch(html, -1) {
		if !strings.Contains(m[1], "src=") || strings.TrimSpace(m[2]) != "" {
			t.Errorf("inline script in index.html: %.120s", m[0])
		}
	}
	if regexp.MustCompile(`(?i)\son[a-z]+\s*=`).MatchString(html) {
		t.Error("inline event handler in index.html")
	}
}

func TestAssetsServedImmutable(t *testing.T) {
	root, err := fs.Sub(assets, "static")
	if err != nil {
		t.Fatal(err)
	}
	var asset string
	_ = fs.WalkDir(root, "assets", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && asset == "" && strings.HasSuffix(p, ".js") {
			asset = p
		}
		return nil
	})
	if asset == "" {
		t.Skip("UI not built (make weft-ui)")
	}
	rec := get(t, "/"+asset)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("GET /%s -> %d %v", asset, rec.Code, rec.Header())
	}
	if rec := get(t, "/assets/missing-file.js"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing asset -> %d", rec.Code)
	}
}
