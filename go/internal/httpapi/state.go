// Linux only: it names types that are openat2 handles underneath.
//go:build linux

// Package httpapi is the native REST surface: the twelve-step chain, the
// route table, and the handlers that answer it. It composes net/http and
// nothing else; the router is Go 1.22's ServeMux with method and wildcard
// patterns, which this surface's route list confirmed it covers.
package httpapi

import (
	"log/slog"

	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/mw"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/route"
)

// State is everything the chain and the handlers read. It is the typed
// configuration the server's validating constructor produces: the config
// file is parsed into raw values, and this is the shape every other package
// accepts after validation.
type State struct {
	Log   *slog.Logger
	Clock clock.Clock
	Auth  *auth.Service
	Core  *core.Core

	// Trusted is the proxy boundary, and Hosts is the declared origin list.
	// Both are live holders: configuration at boot, and the settings surface
	// moves them within their bounds at runtime.
	Trusted *mw.TrustedSet
	Hosts   *mw.HostSet

	// Limits holds the admin-mutable bounds; a D5 constant is the compiled-in
	// default and the outer bound, and this is what an administrator moves
	// within it.
	Limits Runtime

	// Protocol is what another protocol's mount owns, empty in a build that
	// speaks only this server's own surfaces.
	Protocol mw.ProtocolPaths

	// CSRFKey is the key sessions' CSRF tokens derive from.
	CSRFKey []byte

	// AppCSP is the application origin's Content-Security-Policy, built by the
	// wiring because it carries a hash for each inline script the frontend
	// bundle ships and this package sits below the one that embeds it. Empty
	// takes the policy for a build with no frontend.
	AppCSP string

	// Limiter is the shared per-client bucket. One instance for the process.
	Limiter *mw.RateLimiter

	// lookup is the route table's requirement resolver, installed by the
	// wiring before the server starts.
	lookup route.Lookup
}

// Runtime is the set of bounds a request path may read and an administrator
// may move within its D5 outer bound. Nothing on a request path writes here.
type Runtime struct {
	// RatePerSec and RateBurst bound the per-client token bucket.
	RatePerSec float64
	RateBurst  int
}
