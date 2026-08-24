//go:build linux && compat_nc

package server

import (
	"log/slog"
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/compat/ncwire"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/mw"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
)

// compatRoutes returns the compatibility mounts.
//
// This is the server's whole reference to the compat layer, and it lives in
// one tagged file with a no-op sibling. A build without the tag does not
// compile the layer at all, which is stronger than a flag that still
// typechecks: an error in there cannot hide behind the flag being off.
func compatRoutes(c *core.Core, st *state.DB, authSvc *auth.Service, origin string, clk clock.Clock, log *slog.Logger) []compatMount {
	// The mount points the sync clients use, which address the same tree this
	// server serves at its own prefix. They live here because the names are
	// another product's protocol and this is the one file that speaks it.
	layer := ncwire.Build(c, st, authSvc, origin, clk, log)
	out := make([]compatMount, 0)
	for _, m := range layer.Mounts() {
		out = append(out, compatMount{Method: m.Method, Pattern: m.Pattern, Handler: m.Handler})
	}
	return out
}

// compatPaths describes what the layer's own protocol owns, for the chain and
// the file mount. The names are another product's protocol, which is why they
// live in this tagged file rather than in the core packages that read them.
func compatPaths() (mw.ProtocolPaths, []DavAlias) {
	return mw.ProtocolPaths{
		FilePrefixes: []string{"/remote.php/", "/index.php/remote.php/"},
		PublicReads: []string{
			"/status.php",
			"/ocs/v1.php/cloud/capabilities",
			"/ocs/v2.php/cloud/capabilities",
		},
		// The device login, which mints the credential it cannot carry.
		// The third leg, the approval, is deliberately absent: it runs
		// behind a session and a CSRF token, because a grant reachable
		// without one is an account takeover from a drive-by page.
		CredentialFlow: []string{"/index.php/login/v2", "/index.php/login/v2/poll"},
	}, []DavAlias{
		{Prefix: "/remote.php/dav/files/", DropSegments: 1},
		{Prefix: "/index.php/remote.php/dav/files/", DropSegments: 1},
		{Prefix: "/remote.php/webdav/", DropSegments: 0},
		{Prefix: "/index.php/remote.php/webdav/", DropSegments: 0},
	}
}

// compatMount is one route the layer owns.
type compatMount struct {
	Method  string
	Pattern string
	Handler http.Handler
}
