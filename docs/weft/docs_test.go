package weftdocs

import (
	"regexp"
	"strings"
	"testing"
	"unicode"
)

func TestUserGuidesComeFirst(t *testing.T) {
	list := List()
	if len(list) == 0 || list[0].Name != "setup.md" || list[0].Kind != KindGuide {
		t.Fatalf("first guide = %+v", list)
	}
	seen := map[string]bool{}
	reference := false
	for _, g := range list {
		if seen[g.Name] {
			t.Errorf("%s listed twice: a guide and a reference document share a name", g.Name)
		}
		seen[g.Name] = true
		if g.Kind == KindReference {
			reference = true
		} else if reference {
			t.Errorf("guide %s listed after the reference documents", g.Name)
		}
		if g.Title == "" || strings.HasSuffix(g.Title, ".md") {
			t.Errorf("%s has no title heading", g.Name)
		}
	}
	for _, want := range []string{"setup.md", "sessions.md", "agents.md", "teams.md", "security.md", "evaluations.md", "README.md", "API.md", "DESIGN.md"} {
		if !seen[want] {
			t.Errorf("%s is not listed", want)
		}
	}
	if _, ok := Read("sessions.md"); !ok {
		t.Error("a guide is not readable by its base name")
	}
	for _, bad := range []string{"guides/sessions.md", "../go.mod", "PROVENANCE.md", "docs.go"} {
		if _, ok := Read(bad); ok {
			t.Errorf("%s is readable", bad)
		}
	}
}

var linkRe = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// slug spells a heading's anchor the way the UI does (web/weft-ui/src/lib/helpLinks.ts).
func slug(h string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(h)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' || r == '-':
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteRune('-')
		}
	}
	return b.String()
}

func anchors(md string) map[string]bool {
	out := map[string]bool{}
	seen := map[string]int{}
	inCode := false
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(line, "```") {
			inCode = !inCode
		}
		if inCode || !strings.HasPrefix(line, "#") {
			continue
		}
		s := slug(strings.TrimLeft(line, "# "))
		if n, ok := seen[s]; ok {
			seen[s] = n + 1
			s = s + "-" + string(rune('0'+n+1))
		} else {
			seen[s] = 0
		}
		out[s] = true
	}
	return out
}

// Every link in a user guide opens another guide or reference document in the
// UI, at an anchor that exists.
func TestGuideLinksResolve(t *testing.T) {
	for _, g := range List() {
		if g.Kind != KindGuide {
			continue
		}
		md, _ := Read(g.Name)
		for _, m := range linkRe.FindAllStringSubmatch(md, -1) {
			href := m[1]
			if strings.HasPrefix(href, "http") {
				continue
			}
			target, anchor, _ := strings.Cut(href, "#")
			target = strings.TrimPrefix(strings.TrimPrefix(target, "../"), "./")
			doc := md
			if target != "" {
				var ok bool
				if doc, ok = Read(target); !ok {
					t.Errorf("%s links to %s, which is not embedded", g.Name, href)
					continue
				}
			}
			if anchor != "" && !anchors(doc)[anchor] {
				t.Errorf("%s links to %s: no such heading", g.Name, href)
			}
		}
	}
}
