package server

import (
	"strings"
	"testing"
)

func TestHelpGuides(t *testing.T) {
	e := newEnv(t)
	list := e.doJSON("GET", "/api/v1/help", nil, "")["items"].([]any)
	// User guides first, then the reference documents.
	if len(list) < 3 || list[0].(map[string]any)["name"] != "setup.md" || list[0].(map[string]any)["kind"] != "guide" {
		t.Fatalf("guides = %v", list)
	}
	kinds := ""
	for _, x := range list {
		if k := x.(map[string]any)["kind"].(string); !strings.HasSuffix(kinds, k) {
			kinds += " " + k
		}
	}
	if kinds != " guide reference" {
		t.Fatalf("guide kinds in order = %q", kinds)
	}
	if g := e.doJSON("GET", "/api/v1/help/sessions.md", nil, ""); g["title"] != "Sessions" || !strings.Contains(g["markdown"].(string), "Failed steps") {
		t.Fatalf("sessions.md = %.200v", g)
	}
	g := e.doJSON("GET", "/api/v1/help/API.md", nil, "")
	if !strings.Contains(g["markdown"].(string), "/api/v1/spans/start") || g["title"] == "" {
		t.Fatalf("API.md = %.200v", g)
	}
	for _, bad := range []string{"PROVENANCE.md", "..%2Fgo.mod", "nope.md", "guides%2Fsessions.md"} {
		if code, _ := e.do("GET", "/api/v1/help/"+bad, nil, ""); code != 404 {
			t.Fatalf("%s -> %d", bad, code)
		}
	}
}

// The compatibility guide names another product, so it is listed and served
// only when the compatibility adapter is on.
func TestHelpCompatibilityGuideOnlyWithAdapter(t *testing.T) {
	e, s := newEnvWithServer(t)
	has := func() bool {
		for _, g := range e.doJSON("GET", "/api/v1/help", nil, "")["items"].([]any) {
			if g.(map[string]any)["name"] == compatGuide {
				return true
			}
		}
		return false
	}
	if has() {
		t.Fatal("compatibility guide listed with the adapter off")
	}
	if code, _ := e.do("GET", "/api/v1/help/"+compatGuide, nil, ""); code != 404 {
		t.Fatalf("compatibility guide served with the adapter off: %d", code)
	}
	s.cfg.Compat = true
	if !has() {
		t.Fatal("compatibility guide missing with the adapter on")
	}
}
