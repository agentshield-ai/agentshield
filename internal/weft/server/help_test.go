package server

import (
	"strings"
	"testing"
)

func TestHelpGuides(t *testing.T) {
	e := newEnv(t)
	list := e.doJSON("GET", "/api/v1/help", nil, "")["items"].([]any)
	if len(list) < 3 || list[0].(map[string]any)["name"] != "README.md" {
		t.Fatalf("guides = %v", list)
	}
	g := e.doJSON("GET", "/api/v1/help/API.md", nil, "")
	if !strings.Contains(g["markdown"].(string), "/call/start") || g["title"] == "" {
		t.Fatalf("API.md = %.200v", g)
	}
	for _, bad := range []string{"PROVENANCE.md", "..%2Fgo.mod", "nope.md"} {
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
