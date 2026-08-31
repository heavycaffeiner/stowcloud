//go:build linux

// Package spa answers browser requests for the built interface.
//
// The interface's compiled output rides inside the binary when the embed_ui
// tag is set, and without the tag the server starts normally and simply has
// no pages to hand out. The tag decides which half of this package compiles,
// and nothing outside the package can tell the difference.
//
// Nothing a user uploaded is ever served here. Uploads are reachable only on
// the content origin through capability URLs, which is what keeps a stored
// HTML or SVG file from running inside a page that holds session cookies.
package spa

import "net/http"

// Handler answers with the interface, or reports that this build embeds
// none.
//
// The caller uses the second return to decide between mounting the fallback
// and leaving the root unmounted: answering a browser with a JSON 404 reads
// to the person holding it as a server that never had an interface at all.
func Handler() (http.Handler, bool) { return handler() }
