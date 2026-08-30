//go:build embed_ui

package spa

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
	"sync"
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
		// Unreachable: the embed is checked at compile time, so a failure here
		// would mean the directive and this path disagree.
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
			// Not a file, so the client router owns the path. The document is
			// handed back for it to route, which is what makes a deep link
			// work on a reload.
			w.Header().Set("Cache-Control", "no-cache")
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			files.ServeHTTP(w, r2)
			return
		}
		// Hash-named bundle files are immutable, so they cache for a year.
		// The document above is the one that revalidates.
		if strings.HasPrefix(clean, appDir+"/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	}), true
}

// appDir is where the frontend build puts its hash-named assets, and it
// matches the app directory the frontend config names.
const appDir = "app"

// inlineScriptHashes scans the embedded document a single time and keeps the
// hashes: the bundle is baked into the binary, so its content cannot change
// out from under a computed answer.
var inlineScriptHashes = sync.OnceValue(func() []string {
	body, err := bundle.ReadFile("build/index.html")
	if err != nil {
		// No document, so no inline script to admit. The policy stays as
		// strict as it was rather than being loosened over a missing file.
		return nil
	}
	return hashesFrom(string(body))
})
