// Package weftdocs embeds Weft's guides so the server can show them in the
// web UI (/help/...), offline and for the version that is running.
//
// Two kinds: user guides (guides/*.md, task-oriented, one per area of the
// UI) come first; the reference documents (README, API, DESIGN, …) follow.
// Guides are served by their base name (guides/sessions.md is "sessions.md"),
// so names must not repeat across the two kinds.
package weftdocs

import (
	"embed"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
)

//go:embed README.md API.md DESIGN.md COMPATIBILITY.md guides/*.md
var files embed.FS

// Kinds of guide.
const (
	KindGuide     = "guide"
	KindReference = "reference"
)

// Guide is one embedded guide.
type Guide struct {
	Name  string `json:"name"`  // file name, e.g. "sessions.md" or "API.md"
	Title string `json:"title"` // its first heading
	Kind  string `json:"kind"`  // KindGuide or KindReference
}

var titleRe = regexp.MustCompile(`(?m)^#\s+(.+)$`)

// order lists the guides as the help index shows them: user guides in the
// order a new user needs them, then the reference.
var order = map[string]int{
	"setup.md": 0, "sessions.md": 1, "agents.md": 2, "teams.md": 3, "security.md": 4, "traces.md": 5,
	"evaluations.md": 6, "filters.md": 7, "project.md": 8,
	"README.md": 100, "API.md": 101, "DESIGN.md": 102, "COMPATIBILITY.md": 103,
}

func rank(g Guide) int {
	if r, ok := order[g.Name]; ok {
		return r
	}
	if g.Kind == KindGuide {
		return 50
	}
	return 200
}

// List returns the embedded guides, user guides first.
func List() []Guide {
	var out []Guide
	add := func(dir, kind string) {
		entries, _ := fs.ReadDir(files, dir)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			b, _ := files.ReadFile(path.Join(dir, e.Name()))
			g := Guide{Name: e.Name(), Title: strings.TrimSuffix(e.Name(), ".md"), Kind: kind}
			if m := titleRe.FindSubmatch(b); m != nil {
				g.Title = strings.TrimSpace(string(m[1]))
			}
			out = append(out, g)
		}
	}
	add("guides", KindGuide)
	add(".", KindReference)
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := rank(out[i]), rank(out[j]); ri != rj {
			return ri < rj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Read returns a guide's Markdown; ok is false for unknown names. A user
// guide is found by its base name.
func Read(name string) (string, bool) {
	if strings.ContainsAny(name, "/\\") || !strings.HasSuffix(name, ".md") {
		return "", false
	}
	for _, p := range []string{path.Join("guides", name), name} {
		if b, err := files.ReadFile(p); err == nil {
			return string(b), true
		}
	}
	return "", false
}
