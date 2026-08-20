//go:build !compat_nc

package server

import (
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
)

// compatRoutes returns nothing in a build without the compatibility layer.
//
// The no-op sibling exists so the assembly calls this unconditionally: a build
// tag that changes whether a function exists pushes the tag into every caller,
// which is how a tag stops being one file's concern.
func compatRoutes(*core.Core, *state.DB) []compatMount { return nil }

// compatMount is one route the layer owns. It is declared in both files so the
// assembly's type does not depend on the tag either.
type compatMount struct {
	Method  string
	Pattern string
	Handler http.Handler
}
