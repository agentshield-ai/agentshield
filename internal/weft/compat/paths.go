package compat

import (
	"net/http"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
)

// adapterRoutes matches the paths of the adapter's routes (any method),
// built from the same Mount functions the server uses, so it never drifts
// from them. The UI link redirects (/{entity}/{project}/weave/...) are left
// out: without the adapter they are ordinary UI paths.
var adapterRoutes = sync.OnceValue(func() *chi.Mux {
	all := chi.NewRouter()
	a := &Adapter{}
	rs := &Routes{adapter: a}
	a.MountPublic(all)
	rs.MountPublic(all)
	a.MountAuthed(all, rs)
	rs.Mount(all)
	m := chi.NewRouter()
	noop := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	seen := map[string]bool{}
	_ = chi.Walk(all, func(_, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, "/{entity}/") || seen[route] {
			return nil
		}
		seen[route] = true
		m.Handle(route, noop)
		return nil
	})
	return m
})

// IsAdapterRoute reports whether path is one of the adapter's routes (with
// any method). The server answers them with a JSON 404 while the adapter is
// off, rather than with the UI.
func IsAdapterRoute(path string) bool {
	m := adapterRoutes()
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
		if m.Match(chi.NewRouteContext(), method, path) {
			return true
		}
	}
	return false
}
