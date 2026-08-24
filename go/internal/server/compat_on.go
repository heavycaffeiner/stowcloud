//go:build linux && compat_nc

package server

import (
	"log/slog"
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/internal/compat/ncwire"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
)

// compatRoutes returns the compatibility mounts.
//
// This is the server's whole reference to the compat layer, and it lives in
// one tagged file with a no-op sibling. A build without the tag does not
// compile the layer at all, which is stronger than a flag that still
// typechecks: an error in there cannot hide behind the flag being off.
func compatRoutes(c *core.Core, st *state.DB, log *slog.Logger) []compatMount {
	layer := ncwire.Build(c, st, log)
	out := make([]compatMount, 0)
	for _, m := range layer.Mounts() {
		out = append(out, compatMount{Method: m.Method, Pattern: m.Pattern, Handler: m.Handler})
	}
	return out
}

// compatMount is one route the layer owns.
type compatMount struct {
	Method  string
	Pattern string
	Handler http.Handler
}
