// Package webui serves the compiled Weft web app (web/weft-ui) from the
// binary. Build it with `make weft-ui`.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:static
var assets embed.FS

const placeholder = `<!doctype html><html><head><meta charset="utf-8"><title>Weft</title></head>
<body style="font-family:system-ui;padding:40px;max-width:640px;margin:auto">
<h1>Weft</h1><p>The API is running, but this binary was built without the web UI.</p>
<p>Build it with <code>make weft-ui</code> and rebuild the server.</p></body></html>`

// ContentSecurityPolicy is sent with the UI's HTML. The UI renders data
// that anyone who can send telemetry wrote, so the page may run only its own
// scripts (no inline script, no eval), talk only to this server, embed only
// blob: objects (PDFs, always application/pdf blobs) and frames of its own,
// and cannot be framed.
//   - img-src and media-src allow http(s): presigned reference-bucket URLs
//     (S3, GCS, a local MinIO) and remote images the viewer asked to load.
//     The UI never fetches a remote image named in recorded data by itself
//     (components/media/RecordedImage.tsx): it shows "Image from <host>".
//   - style-src 'unsafe-inline': React style attributes and the charts.
//   - font-src data: the bundled Inter font may be inlined by the build.
const ContentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob: http: https:; " +
	"media-src 'self' data: blob: http: https:; " +
	"font-src 'self' data:; " +
	"connect-src 'self'; " +
	"object-src blob:; " +
	"frame-src 'self'; " +
	"worker-src 'self'; " +
	"frame-ancestors 'none'; " +
	"base-uri 'none'; " +
	"form-action 'self'"

// Handler serves static assets, falling back to index.html for client routes.
func Handler() http.Handler {
	root, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err)
	}
	index, idxErr := fs.ReadFile(root, "index.html")
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p != "" && p != "index.html" {
			if st, err := fs.Stat(root, p); err == nil && !st.IsDir() {
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
			if strings.HasPrefix(p, "assets/") {
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", ContentSecurityPolicy)
		// Recorded links and images never learn which Weft page showed them.
		w.Header().Set("Referrer-Policy", "no-referrer")
		if idxErr != nil {
			_, _ = w.Write([]byte(placeholder))
			return
		}
		_, _ = w.Write(index)
	})
}
