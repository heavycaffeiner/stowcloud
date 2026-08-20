//go:build compat_nc

package nc

import "net/http"

// Mount is one route this layer answers.
//
// The layer describes its routes rather than registering them, because the
// route table lives in the server and a layer that registered its own would be
// a second place routes come from.
type Mount struct {
	Method  string
	Pattern string
	Handler http.Handler
}

// Mounts is every route the layer owns.
//
// Empty until the handlers land. The seam and its gates come first
// deliberately: a boundary added after the code it governs is a refactor, and
// this one has been argued for in this codebase once already.
func (l *Layer) Mounts() []Mount { return nil }
