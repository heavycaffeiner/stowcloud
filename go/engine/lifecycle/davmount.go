//go:build linux

// The WebDAV mount.
//
// Nothing in the protocol package interprets a virtual path; it works from
// resolutions handed to it. Turning a URL into one of those, and a method into
// a dispatch, happens here, which is what stops a second path parser existing.
package lifecycle

import (
	"context"
	"encoding/xml"
	"errors"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
	"net/http"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/dav"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// DavPrefix is the mount point. Everything under it is WebDAV.
const DavPrefix = "/dav"

type davContextKey string

const keyDavPath davContextKey = "dav_path"

// newDavHandler builds the protocol handler over the engine's services.
func (e *Engine) newDavHandler() *dav.Handler {
	if e.davLocks == nil {
		e.davLocks = NewDavLocks(e.State, e.clock, e.logger)
	}
	locks := e.davLocks
	return dav.New(dav.Options{
		Core:            e.Core,
		Locks:           locks,
		TokensAt:        locks.Tokens,
		LocksAt:         locks.At,
		Taker:           locks,
		Store:           NewDavProps(e.State),
		KeyOf:           DavKeyOf,
		InfinityEntries: davDefaultInfinity,
		Logger:          e.logger,
	})
}

// guardDavLock checks whether an active exclusive WebDAV lock covers the target path,
// preventing cross-protocol overwrites and deletions.
func (e *Engine) guardDavLock(ctx context.Context, share uint32, path string, principal int64) error {
	if e.davLocks == nil {
		return nil
	}
	return e.davLocks.Guard(ctx, share, path, principal, nil)
}

// davDefaultInfinity is the listing ceiling, a decision named rather than a
// bare number in the constructor.
const davDefaultInfinity = 10_000

// mountDav claims the WebDAV prefix.
//
// The chain has already run by the time a request reaches the bridge, so the
// principal it resolved travels in the request context, which is what the
// mount reads. The bridge is built once: it is stateless.
func (e *Engine) mountDav(app *fiber.App) {
	bridge := adaptor.HTTPHandler(e.DavHandler(e.newDavHandler()))
	app.All(DavPrefix, bridge)
	app.All(DavPrefix+"/*", bridge)
}

// DavHandler serves the WebDAV mount.
func (e *Engine) DavHandler(h *dav.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No credential needed to discover: a client establishes that the
		// server speaks this protocol before it will authenticate, so demanding
		// one first means it never gets that far. Restricted to the root,
		// because an OPTIONS aimed at a path speaks about that resource, and
		// answering that to a stranger tells them whether a file is there.
		path := r.URL.EscapedPath()
		for strings.Contains(path, "//") {
			path = strings.ReplaceAll(path, "//", "/")
		}

		if r.Method == http.MethodOptions && davIsRoot(path) {
			h.MountOptions(w)
			return
		}

		user, ok := davUser(r)
		if !ok {
			// A WebDAV client does not send a credential until it is asked,
			// and this is what asks. Without the challenge the client reports
			// a failure instead of prompting, which reads as a broken server
			// to whoever is holding it.
			w.Header().Set("WWW-Authenticate", `Basic realm="WebDAV", charset="UTF-8"`)
			apierr.WriteClassified(w, apierr.Classified{Class: apierr.AuthRequired})
			return
		}

		r = r.WithContext(context.WithValue(r.Context(), keyDavPath, path))

		// Nothing on disk corresponds to the virtual root, so resolution has
		// no path to be handed. PROPFIND answers the caller's shares; a sync
		// client lists it to confirm the account it has just signed in as, and
		// a 404 at that moment reads as a server that cannot be reached, right
		// after a sign-in that succeeded.
		if davIsRoot(path) {
			if r.Method == "PROPFIND" {
				baseProps, children := e.davRootProps(r.Context(), user)
				h.RootPropfind(w, r, baseProps, children)
				return
			}
			if r.Method == http.MethodHead || r.Method == http.MethodGet {
				w.Header().Set("DAV", "1, 2")
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				return
			}
		}

		// Read is the gate, not the method's own need. Resolution keeps the
		// caller's whole effective set and every operation checks its own
		// requirement against that, so asking here for what the method needs
		// changes no answer: it only decides whether the refusal arrives from
		// this line or from the operation. Reaching a share at all requires
		// read, which is what makes this the honest gate.
		res, err := e.resolveDav(r, user, path, acl.Read)
		if err != nil {
			// Logged here as well as in the protocol handler: a write refused
			// by resolution never reaches one. Without this the operator
			// looking for the failed write finds nothing at all.
			e.davRefused(r, path, err)
			apierr.Write(w, err, apierr.VisibilityHidden)
			return
		}

		switch r.Method {
		case "MOVE", "COPY":
			// The destination arrives as a URL in a header. Turning that into
			// a second resolution is this layer's work, which is why these two
			// are separate entry points.
			target, terr := e.davDestination(user, r)
			if terr != nil {
				apierr.Write(w, terr, apierr.VisibilityHidden)
				return
			}

			if r.Method == "MOVE" {
				h.Move(w, r, res, target)
				return
			}
			h.Copy(w, r, res, target)
		default:
			h.ServeMethod(w, r, res)
		}
	})
}

// davRefused records a write that resolution turned away.
//
// Reads are left out for the reason the protocol handler leaves them out: a
// sync client probes for absent paths constantly, and those lines bury the
// one an operator is looking for.
func (e *Engine) davRefused(r *http.Request, path string, err error) {
	if _, served := dav.MethodRequirement(r.Method); !served {
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, "PROPFIND", "OPTIONS":
		return
	}
	e.log().Warn("the write was refused before it reached the protocol",
		"method", r.Method, "path", path, "subsystem", "dav", "error", err)
}

// davUser reads the caller the chain authenticated.
//
// The key is the plain string form, which is what the chain stores the
// principal under: the value crosses the framework boundary into the request
// context, whose lookup compares keys as interfaces, and the constant's
// defined type would not match the string the chain wrote.
func davUser(r *http.Request) (core.UserID, bool) {
	p, ok := r.Context().Value(middleware.KeyCredential).(middleware.Principal)
	if !ok || p.UserID == 0 {
		return 0, false
	}
	return core.UserID(p.UserID), true
}

// davIsRoot reports whether a path names the virtual root.
//
// Two spellings are accepted. A client addressing a collection may append the
// trailing slash or leave it off, and either way it means the same place.
func davIsRoot(path string) bool {
	rest := strings.TrimPrefix(path, DavPrefix)
	for len(rest) > 0 && rest[0] == '/' {
		rest = rest[1:]
	}
	return rest == ""
}

// resolveDav turns a mount-relative URL into a resolution.
//
// The protocol package owns the splitting, which decodes per segment after the
// split so an encoded separator cannot introduce a boundary. A second decoder
// here would be a second set of rules about what a path may contain, and the
// weaker of the two would be the one that decides.
func (e *Engine) resolveDav(
	r *http.Request, user core.UserID, urlPath string, want acl.Perms,
) (core.Resolved, error) {
	parts, err := dav.SplitPath(strings.TrimPrefix(urlPath, DavPrefix))
	if err != nil {
		return core.Resolved{}, apierr.BadRequest("dav.bad_path", "path")
	}

	vp, perr := vfs.ParseVpath(strings.Join(parts, "/"))
	if perr != nil {
		return core.Resolved{}, core.ErrNotFound
	}
	res, rerr := e.Core.Resolve(user, vp, want)
	if rerr != nil {
		return res, rerr
	}

	if p, ok := r.Context().Value(middleware.KeyCredential).(middleware.Principal); ok {
		if len(p.Shares) > 0 && len(parts) > 0 {
			allowed := false
			for _, s := range p.Shares {
				if s == parts[0] {
					allowed = true
					break
				}
			}
			if !allowed {
				return core.Resolved{}, core.ErrNotFound
			}
		}
		if !p.Mask.IsEmpty() {
			res = res.WithMask(p.Mask)
			if !res.Has(want) {
				return core.Resolved{}, core.ErrDenied
			}
		}
	}
	return res, nil
}

// davDestination resolves the header a MOVE or a COPY names its target with.
func (e *Engine) davDestination(user core.UserID, r *http.Request) (dav.Target, error) {
	// The protocol package parses the header, including refusing one that
	// names another host. Taking the path alone would make a COPY to
	// https://elsewhere.example/dav/docs/x copy to /dav/docs/x here: a request
	// naming somewhere else answered as though it named this server.
	segments, err := dav.ParseDestination(r.Header.Get("Destination"), r.Host)
	switch {
	case errors.Is(err, dav.ErrNoDestination):
		return dav.Target{}, apierr.BadRequest("dav.no_destination", "Destination")

	case errors.Is(err, dav.ErrForeignDestination):
		return dav.Target{}, apierr.BadGatewayError("dav.foreign_destination", "Destination")
	case err != nil:
		return dav.Target{}, apierr.BadRequest("dav.bad_destination", "Destination")
	}

	path := "/" + strings.Join(segments, "/")

	// Read here too, for the reason the request path uses it: the copy and the
	// move check what they need against the resolution they are handed.
	res, rerr := e.resolveDav(r, user, path, acl.Read)
	if rerr != nil {
		return dav.Target{}, rerr
	}
	return dav.Target{
		Resolved:  res,
		Overwrite: dav.Overwrite(r.Header.Get("Overwrite")),
	}, nil
}

// davRootProps lists the caller's shares as the virtual root's children.
func (e *Engine) davRootProps(ctx context.Context, user core.UserID) ([]dav.Prop, []dav.RootChild) {
	roots := e.Core.Roots(user)
	children := make([]dav.RootChild, 0, len(roots))
	for _, rt := range roots {
		children = append(children, dav.RootChild{
			Label: rt.Label,
			Props: []dav.Prop{
				{Name: xml.Name{Space: "DAV:", Local: "displayname"}, Value: rt.Label},
			},
		})
	}
	return nil, children
}
