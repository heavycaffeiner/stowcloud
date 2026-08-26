// Linux only: it depends on packages that are Linux only.
//go:build linux

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
	"github.com/heavycaffeiner/stowcloud/go/internal/preview"
	"github.com/heavycaffeiner/stowcloud/go/internal/runtimecfg"
	"github.com/heavycaffeiner/stowcloud/go/internal/search/service"
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

	// Runtime holds the settings an administrator moves from the interface.
	// A nil one leaves every field reported and none of them editable, which
	// is what a build with no settings store has.
	Runtime *runtimecfg.Holder

	// SMBConfigDir is where the rendered files go, reported by the settings
	// surface and not editable there: the sidecar mounts the same directory,
	// so changing one side of that pair moves only this one.
	SMBConfigDir string

	// DataDir is where the databases live. Used to resolve the default homes
	// root when a settings check has to probe a directory the request did not
	// name, and reported by the settings surface.
	DataDir string

	// Listen reports the address the listener is on right now, which is not
	// always the stored one: a save that could not bind leaves the old socket
	// serving, and the screen has to be able to say so.
	Listen func() string

	// SwapListener moves the listener to a new address. It binds the new
	// socket before it touches the old one, so a failure leaves the deployment
	// reachable and the save is refused. A nil one leaves the address stored
	// and applied on the next start.
	SwapListener func(ctx context.Context, addr string) error

	// Preview generates thumbnails. A nil one is a build with no preview
	// subsystem: the listing then reports every entry as having none and the
	// route answers 404, rather than the interface asking for something that
	// cannot arrive.
	Preview *preview.Service

	// Events upgrades the change-channel socket for an authenticated user.
	Events EventsHandler

	// State is the durable store, for the surfaces that read a table the core
	// does not own: the grant rows the admin screen edits.
	State *state.DB

	// ReloadACL rebuilds the permission evaluator from the stored grants. A
	// grant live in the database and stale in the process serving requests is
	// a permission decision that depends on which half was asked.
	ReloadACL func(ctx context.Context) error

	// StoreSecret seals a settings value that is a credential and stores it
	// apart from the settings document. Nil leaves the field refused rather
	// than stored in the clear.
	StoreSecret func(ctx context.Context, plain string) error

	// HasOIDCSecret reports whether a client secret is stored, which is all
	// the settings screen is ever told about it.
	HasOIDCSecret bool

	// ActiveWork reports what a restart would interrupt. A nil one reports
	// nothing in flight, which is what a build with no job machinery has.
	ActiveWork func() ActiveWork

	// RequestRestart asks the process to exit so a supervisor starts it again.
	// A nil one leaves a change that needs it stored and reported as not in
	// effect, rather than as a success that changed nothing.
	RequestRestart func()

	// PathInJail reports whether a host path is already inside the sandbox's
	// domain, which is what decides whether a new share is reachable now or
	// after a restart. A nil one answers that every path is, which is what a
	// build with no sandbox has.
	PathInJail func(host string) bool

	// OIDCDisplayName is what the sign-in button says. It is configuration
	// rather than something the provider tells us, because the login screen
	// has to draw the button before any provider has been contacted.
	OIDCDisplayName string

	// Search answers queries and builds the name index. A nil one leaves the
	// build surface refusing and every query taking the walk, which is the
	// correct behaviour for a build with no index rather than a degradation.
	Search *service.Service

	// ApplyIndexEnabled attaches or detaches the index in the running process,
	// so the administrator's switch takes effect without a restart. A nil one
	// leaves the switch stored and not applied, and the settings surface says
	// so rather than reporting a success that changes nothing.
	ApplyIndexEnabled func(enabled bool) error

	// PublishSMB re-renders the SMB configuration and asks the sidecar to
	// apply it, returning what the sidecar says happened. A nil one leaves the
	// surface answering that SMB is not configured, which is what a deployment
	// without the sidecar has.
	PublishSMB func(ctx context.Context) (smbagent.Report, error)

	// SMBChanged republishes and reports what happened, without ever failing
	// the caller. Every write that changes what SMB should serve calls it.
	//
	// It exists separately from PublishSMB because the two have different
	// callers with different needs. An administrator pressing apply wants the
	// sidecar's report as the answer to their request; a grant being revoked
	// wants the change to reach SMB and must not fail because the sidecar is
	// down, since the grant is already committed and the web surface already
	// refuses.
	//
	// The outcome comes back so the write's own response can carry it. Not
	// carrying it was the gap: a share saved here with the sidecar stopped
	// answered a clean success, and "saved here, not applied over there" only
	// showed up on the health page whenever somebody next looked at it.
	SMBChanged func(ctx context.Context) SMBOutcome

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

// record writes one audit row for an administrator's action.
//
// After the write, never instead of it: the change has already happened, so a
// failure to record it is a hole in the log rather than a reason to tell the
// caller their change did not land. The account, the address and the agent all
// come from the request, which is the only place they exist.
func record(r *http.Request, d Deps, actor int64, event, target string, ok bool) {
	if d.Auth == nil {
		return
	}
	d.Auth.Record(r.Context(), actor, event, target,
		mw.ClientFrom(r.Context()).String(), r.UserAgent(), ok)
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
