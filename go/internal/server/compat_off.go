//go:build linux && !compat_nc

package server

import (
	"log/slog"
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/mw"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
)

// compatRoutes returns nothing in a build without the compatibility layer.
//
// The no-op sibling exists so the assembly calls this unconditionally: a build
// tag that changes whether a function exists pushes the tag into every caller,
// which is how a tag stops being one file's concern.
func compatRoutes(*core.Core, *state.DB, *auth.Service, string, clock.Clock, *slog.Logger) []compatMount {
	return nil
}

// compatPaths describes nothing in a build without the layer, which is what
// makes every predicate reading it answer false.
func compatPaths() (mw.ProtocolPaths, []DavAlias) { return mw.ProtocolPaths{}, nil }

// compatMount is one route the layer owns. It is declared in both files so the
// assembly's type does not depend on the tag either.
type compatMount struct {
	Method  string
	Pattern string
	Handler http.Handler
}
