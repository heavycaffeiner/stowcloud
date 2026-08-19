// The file is tagged embed_ui because //go:embed reads web/build at compile
// time: a build without a built frontend must not require one, and the gate's
// embed_ui step builds only when web/build/index.html exists.
//go:build embed_ui

// Package static serves the embedded SPA under the embed_ui build tag.
//
// The embed is a real dependency edge: after npm run build, the next go build
// picks up the new files, so the stale-frontend hazard that needed a cargo
// clean has no counterpart here. Uploaded content is never served from this
// mount; it lives on the separate content origin with a capability URL, so a
// stored HTML or SVG file executing in a browser has no session to steal.
package static

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:dist
var dist embed.FS

// Handler serves the built SPA with a single-file fallback: any path that is
// not a file hands index.html back to the client router. The hash-named
// bundle files are immutable, so they are served with a long cache; the
// document itself is revalidated.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic("embedded SPA is missing: " + err.Error()) //nolint:forbidigo // a build without web/build is a wiring failure.
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/" {
			// The document is revalidated, not cached long.
			w.Header().Set("Cache-Control", "no-cache")
			fileServer.ServeHTTP(w, r)
			return
		}
		if _, err := fs.Stat(sub, p[1:]); err != nil {
			// Not a file: the client router owns the path.
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
