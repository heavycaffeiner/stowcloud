// Package spa serves the built frontend.
//
// The bundle is compiled into the binary under the embed_ui tag and is absent
// without it, so a build with no frontend produces a server that runs and says
// so rather than one that fails to compile. Which of the two files in this
// package is built is the only difference.
//
// Uploaded content is never served from this mount. It lives on the separate
// content origin behind a capability URL, so a stored HTML or SVG file
// executing in a browser has no session to steal.
package spa

import "net/http"

// Handler serves the SPA, or reports that this build carries none.
//
// The second return says whether a bundle is present, so the caller mounts the
// route or leaves it unmounted rather than serving a 404 page that looks like
// a broken frontend.
func Handler() (http.Handler, bool) { return handler() }
