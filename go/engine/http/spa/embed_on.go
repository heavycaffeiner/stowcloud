//go:build embed_ui

package spa

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// The compiled interface, embedded from this directory.
//
// //go:embed only sees files inside its own package, so the frontend's build
// writes its output here instead of somewhere the directive points at. That
// placement doubles as the dependency edge: rebuilding the frontend changes
// these files, and the next go build picks them up without a cache clean.
//
//go:embed all:build
var bundle embed.FS

func handler() (http.Handler, bool) {
	sub, err := fs.Sub(bundle, "build")
	if err != nil {
		// Compile-checked: the directive and this path cannot disagree, so
		// this branch never runs.
		return nil, false
	}
	if _, serr := fs.Stat(sub, "index.html"); serr != nil {
		return nil, false
	}

	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(r.URL.Path, "/")
		if clean == "" {
			// The document revalidates on every load rather than living in a
			// cache, so a redeployed bundle takes effect on refresh.
			w.Header().Set("Cache-Control", "no-cache")
			files.ServeHTTP(w, r)
			return
		}
		if _, serr := fs.Stat(sub, clean); serr != nil {
			// Nothing on disk at this path, so the client-side router takes
			// over: handing it the document is what makes a deep link survive
			// a reload.
			w.Header().Set("Cache-Control", "no-cache")
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			files.ServeHTTP(w, r2)
			return
		}
		// The build names its assets by content hash, so they may be cached
		// for a year; the document above is the one that revalidates.
		if strings.HasPrefix(clean, appDir+"/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	}), true
}

// appDir mirrors the frontend config's app directory: the build lays its
// hash-named assets there, and the long cache header below applies to them.
const appDir = "app"

// inlineScriptHashes scans the embedded document for its inline scripts.
//
// The bundle is baked into the binary, so the answer cannot change between
// calls, and the document is small enough that rescanning costs nothing.
func inlineScriptHashes() []string {
	body, err := bundle.ReadFile("build/index.html")
	if err != nil {
		// Nothing embedded means nothing to admit; an empty list keeps the
		// policy at its strictest rather than loosening it over a gap.
		return nil
	}
	return hashesFrom(string(body))
}
