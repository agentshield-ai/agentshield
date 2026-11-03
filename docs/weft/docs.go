// Package weftdocs embeds Weft's guides so the server can show them in the
// web UI (/help/...), offline and for the version that is running.
package weftdocs

import (
	"embed"
	"io/fs"
	"regexp"
	"sort"
	"strings"
)

//go:embed README.md API.md DESIGN.md COMPATIBILITY.md
var files embed.FS

// Guide is one embedded guide.
type Guide struct {
	Name  string `json:"name"`  // file name, e.g. "API.md"
	Title string `json:"title"` // its first heading
}

var titleRe = regexp.MustCompile(`(?m)^#\s+(.+)$`)

// order lists the guides as the help index shows them.
var order = map[string]int{"README.md": 0, "API.md": 1, "DESIGN.md": 2, "COMPATIBILITY.md": 3}

// List returns the embedded guides.
func List() []Guide {
	entries, _ := fs.ReadDir(files, ".")
	out := make([]Guide, 0, len(entries))
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, _ := files.ReadFile(e.Name())
		g := Guide{Name: e.Name(), Title: strings.TrimSuffix(e.Name(), ".md")}
		if m := titleRe.FindSubmatch(b); m != nil {
			g.Title = strings.TrimSpace(string(m[1]))
		}
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return order[out[i].Name] < order[out[j].Name] })
	return out
}

// Read returns a guide's Markdown; ok is false for unknown names.
func Read(name string) (string, bool) {
	if strings.ContainsAny(name, "/\\") || !strings.HasSuffix(name, ".md") {
		return "", false
	}
	b, err := files.ReadFile(name)
	if err != nil {
		return "", false
	}
	return string(b), true
}
