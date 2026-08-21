// Package handler is the REST surface's handlers: one file per resource, and
// every handler returns an error the ErrorMapper renders rather than choosing
// a status itself. The only status a handler names is on the success path.
package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/mw"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/oidc"
	"github.com/heavycaffeiner/stowcloud/go/internal/smbagent"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
	"github.com/heavycaffeiner/stowcloud/go/internal/upload"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// Deps is what the handlers read. It is the slice of the httpapi.State a
// handler may touch; the server wires one instance and the route table closes
// over it.
type Deps struct {
	Core  *core.Core
	Auth  *auth.Service
	Clock clock.Clock
	Log   *slog.Logger

	// Limiter is here so the settings handler can move the rate bounds; the
	// chain reads the same instance.
	Limiter *mw.RateLimiter

	// CSRFKey derives the token the session response hands the client, which
	// is the same key the CSRF middleware checks against.
	CSRFKey []byte

	// Trusted and Hosts are here for the network-settings surface, which
	// moves them within their bounds. The chain reads the same instances.
	Trusted *mw.TrustedSet
	Hosts   *mw.HostSet

	// WatchCap reports the watcher's hot-set bound; the watcher itself is
	// Phase 1's package and Phase 5's settings surface only reports the bound.
	WatchCap func() int

	// Events upgrades the change-channel socket for an authenticated user.
	Events EventsHandler

	// State is the durable store, for the surfaces that read a table the core
	// does not own: the grant rows the admin screen edits.
	State *state.DB

	// ReloadACL rebuilds the permission evaluator from the stored grants. A
	// grant live in the database and stale in the process serving requests is
	// a permission decision that depends on which half was asked.
	ReloadACL func(ctx context.Context) error

	// ActiveWork reports what a restart would interrupt. A nil one reports
	// nothing in flight, which is what a build with no job machinery has.
	ActiveWork func() ActiveWork

	// RequestRestart asks the process to exit so a supervisor starts it again.
	// A nil one makes the restart surface refuse rather than pretend.
	RequestRestart func()

	// PublishSMB re-renders the SMB configuration and asks the sidecar to
	// apply it, returning what the sidecar says happened. A nil one leaves the
	// surface answering that SMB is not configured, which is what a deployment
	// without the sidecar has.
	PublishSMB func(ctx context.Context) (smbagent.Report, error)

	// OIDC is the single-sign-on client. A nil one leaves the link surfaces
	// answering that the provider is not configured, which is what a
	// deployment without one has.
	OIDC *oidc.Client

	// Uploads is the resumable-upload engine. A nil one leaves the surface
	// unmounted rather than answering with a session nothing backs.
	Uploads *upload.Engine

	// Health carries the degradations the running server has. It is a value
	// the server owns rather than package state, because a status every caller
	// can reach into is a status any of them can rewrite.
	Health *HealthState
}

// ActiveWork is what a restart would interrupt.
type ActiveWork struct {
	Uploads int
	Jobs    int
}

// Wrap converts a handler function into the route-table form. The error it
// returns is recorded for ErrorMapper, which is the only place a status is
// chosen.
func Wrap(fn func(w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			mw.RecordError(r, err)
		}
	}
}

// userOf is the acting user, or an error that renders as 401 when the route
// is reachable without one. Every handler that reads a path calls it.
func userOf(r *http.Request) (core.UserID, error) {
	p, ok := mw.PrincipalFrom(r.Context())
	if !ok {
		return 0, auth.ErrCredentials
	}
	return core.UserID(p.UserID), nil
}

// requireAdmin is the admin-role guard every /api/admin handler runs. The
// scope layer already refused app passwords on these routes; this is the
// session-side half, and a non-admin session is refused here with the same
// denial a missing credential gets.
func requireAdmin(r *http.Request, svc *auth.Service) (int64, error) {
	uid, err := userOf(r)
	if err != nil {
		return 0, err
	}
	admin, err := svc.IsAdmin(r.Context(), int64(uid))
	if err != nil {
		return 0, err
	}
	if !admin {
		return 0, auth.ErrAccountDisabled
	}
	return int64(uid), nil
}

// pathOf parses the client's path parameter. The path is the one trust
// boundary every browse handler shares, so it is parsed here in one place.
func pathOf(r *http.Request) (vfs.Vpath, error) {
	p := r.URL.Query().Get("path")
	if p == "" {
		return vfs.Vpath{}, vfs.ErrInvalidName
	}
	return vfs.ParseVpath(p)
}

// writeJSON is the success-path responder. The response is a plain object;
// the error envelope is the only other JSON shape this surface sends.
func writeJSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	return enc.Encode(v) //nolint:wrapcheck // a failed encode is a failed write.
}

// readBody reads and bounds a JSON request body. A body over the D5 bound is
// refused as too large before a byte is parsed.
func readBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limits.RequestBody {
		return nil, limits.Exceed("RequestBody", limits.RequestBody, int64(len(body)))
	}
	return body, nil
}

// decodeJSON parses a request body into v, refusing with a named field when
// the JSON does not match. The offending value is never echoed back.
func decodeJSON(r *http.Request, v any) error {
	body, err := readBody(r)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return apierr.BadRequest("fs.invalid_json", "body")
	}
	return nil
}

// Events is the change-channel route. It upgrades the socket and hands the
// connection to the hub, which owns the sub/unsub protocol and the watch
// refcounts. Nothing after this speaks HTTP on this route.
type EventsHandler func(w http.ResponseWriter, r *http.Request, user core.UserID)
