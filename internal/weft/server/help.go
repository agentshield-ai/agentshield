package server

import (
	"net/http"

	weftdocs "github.com/agentshield-ai/agentshield/docs/weft"
	"github.com/go-chi/chi/v5"
)

// The guides in docs/weft are embedded in the binary and served to the UI's
// help pages: GET /api/v1/help lists them, GET /api/v1/help/{name} returns
// one as Markdown. The compatibility guide is listed and served only when
// the compatibility adapter is on.

const compatGuide = "COMPATIBILITY.md"

func init() {
	routeExtensions = append(routeExtensions, func(s *Server, r chi.Router) {
		r.Get("/api/v1/help", func(w http.ResponseWriter, _ *http.Request) {
			items := []weftdocs.Guide{}
			for _, g := range weftdocs.List() {
				if g.Name != compatGuide || s.cfg.Compat {
					items = append(items, g)
				}
			}
			writeJSON(w, map[string]any{"items": items})
		})
		r.Get("/api/v1/help/{name}", func(w http.ResponseWriter, r *http.Request) {
			name := chi.URLParam(r, "name")
			md, ok := weftdocs.Read(name)
			if name == compatGuide && !s.cfg.Compat {
				ok = false
			}
			if !ok {
				writeError(w, http.StatusNotFound, "no guide named "+name)
				return
			}
			title := name
			for _, g := range weftdocs.List() {
				if g.Name == name {
					title = g.Title
				}
			}
			writeJSON(w, map[string]any{"name": name, "title": title, "markdown": md})
		})
	})
}
