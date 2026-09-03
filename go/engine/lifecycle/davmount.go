//go:build linux

// The WebDAV mount.
//
// Nothing in the protocol package interprets a virtual path; it works from
// resolutions handed to it. Turning a URL into one of those, and a method into
// a dispatch, happens here, which is what stops a second path parser existing.
package lifecycle

import (
	"context"
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

// DavUploadPrefix is where the chunked upload collection lives.
//
// Outside the share tree on purpose: the collection is addressed by session
// name, and a session is not a directory under a share. A path inside a share
// named "uploads" would collide with it.
const DavUploadPrefix = "/dav-uploads"

// DavTrashPrefix is where the compatibility trash collection lives.
const DavTrashPrefix = "/dav-trash"

type davContextKey string

const keyDavPath davContextKey = "dav_path"

// DavAlias is an alternative mount point addressing the same tree.
type DavAlias struct {
	// Prefix is the path the other client addresses this server with.
	Prefix string
	// Mount is the local prefix the rewrite lands on. The share tree and the
	// chunked upload collection are separate mounts, and the other product
	// addresses both under one prefix of its own, so the alias has to say
	// which one it means.
	Mount string
	// DropSegments is how many segments after the prefix name something other
	// than a file, such as the account the client believes it is browsing.
	//
	// Dropped rather than checked: resolution runs against the caller's own
	// roots, so a name in the URL cannot widen what the credential allows.
	DropSegments int
}

// newDavHandler builds the protocol handler over the engine's services.
//
// The vocabulary that varies by build, the compatibility header names and the
// vendor property source, arrives through the hooks rather than here, so this
// file stays out of the tag's business.
func (e *Engine) newDavHandler() *dav.Handler {
	locks := NewDavLocks(e.State, e.clock, e.logger)
	return dav.New(dav.Options{
		Core:            e.Core,
		Locks:           locks,
		TokensAt:        locks.Tokens,
		LocksAt:         locks.At,
		Taker:           locks,
		Store:           NewDavProps(e.State),
		KeyOf:           DavKeyOf,
		Uploads:         NewDavUploads(e.Upload),
		UploadHeaders:   e.davUploadHeaders(),
		VendorProps:     e.davVendorProps(),
		InfinityEntries: davDefaultInfinity,
		Logger:          e.logger,
	})
}

// davDefaultInfinity is the listing ceiling, a decision named rather than a
// bare number in the constructor.
const davDefaultInfinity = 10_000

// mountDav claims the WebDAV prefixes.
//
// The chain has already run by the time a request reaches the bridge, so the
// principal it resolved travels in the request context, which is what the
// mount reads. The bridge is built once: it is stateless, and one conversion
// path for every prefix is what keeps the wire behaviour identical however a
// client addresses the tree.
func (e *Engine) mountDav(app *fiber.App) {
	bridge := adaptor.HTTPHandler(e.DavHandler(e.newDavHandler(), e.davAliases()))

	prefixes := []string{DavPrefix, DavUploadPrefix, DavTrashPrefix}
	for _, a := range e.davAliases() {
		prefixes = append(prefixes, strings.TrimSuffix(a.Prefix, "/"))
	}
	for _, p := range prefixes {
		prefix := p
		app.All(prefix, bridge)
		app.All(prefix+"/*", bridge)
	}
}

// DavHandler serves the WebDAV mount.
//
// aliases may be empty, which is a deployment serving only its own prefix. The
// chunked upload collection is served when the engine has an upload engine;
// without one the collection answers 405 rather than half-serving it.
func (e *Engine) DavHandler(h *dav.Handler, aliases []DavAlias) http.Handler {
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
		if rewritten, aliased := davAlias(path, aliases); aliased {
			path = rewritten
		}

		if r.Method == http.MethodOptions && (davIsRoot(r.URL.EscapedPath()) || davIsRoot(path)) {
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

		// The chunked upload collection lives under its own prefix, outside the
		// share tree: it is addressed by session name, and a session is not a
		// directory under a share. The permission lives at the destination,
		// which the collection resolves through the same header COPY and MOVE
		// use. A deployment with no upload engine matches anyway and lets the
		// handler refuse: half-serving the collection would be the worse
		// answer, and a 405 says exactly what is missing.
		if rest, ok := strings.CutPrefix(path, DavUploadPrefix); ok {
			// The assembly member first, because it is not a chunk and the
			// parser would refuse it as a name that is not a number. A client
			// publishes a transfer by moving that member rather than the
			// collection, so this is the shape every real assembly arrives in;
			// both mean the collection itself.
			session, member, found := strings.Cut(strings.TrimPrefix(rest, "/"), "/")
			if found && e.davIsAssemblyMember(member) {
				rest = "/" + session
			}
			up, uerr := dav.ParseUploadPath(rest)
			if uerr != nil {
				// The dav package's own writer, since the refusal is the
				// protocol's: the shared mapper does not know its sentinels
				// and would answer 500 for a malformed member name.
				if werr := dav.WriteError(w, uerr); werr != nil {
					e.logger.Warn("the refusal did not reach the client", "error", werr)
				}
				return
			}
			// The destination is what the session binds to, and every method
			// against the collection needs it: a chunk PUT has to reach the
			// session that holds it, and both go through the alias scoped by
			// the destination's share. Assembling is the one that publishes
			// there. Clients that name the file only at the end send the
			// header from the start; one that omits it has named nothing to
			// upload into.
			target, terr := e.davDestination(user, r, aliases)
			if terr != nil {
				apierr.Write(w, terr, apierr.VisibilityHidden)
				return
			}
			h.ServeUpload(w, r, target.Resolved, up)
			return
		}
		if rest, ok := strings.CutPrefix(path, DavTrashPrefix); ok {
			e.serveDavTrash(w, r, user, rest)
			return
		}

		// Read is the gate, not the method's own need. Resolution keeps the
		// caller's whole effective set and every operation checks its own
		// requirement against that, so asking here for what the method needs
		// changes no answer: it only decides whether the refusal arrives from
		// this line or from the operation. Reaching a share at all requires
		// read, which is what makes this the honest gate.
		res, err := e.resolveDav(user, path, acl.Read)
		if err != nil {
			apierr.Write(w, err, apierr.VisibilityHidden)
			return
		}

		switch r.Method {
		case "MOVE", "COPY":
			// The destination arrives as a URL in a header. Turning that into
			// a second resolution is this layer's work, which is why these two
			// are separate entry points.
			target, terr := e.davDestination(user, r, aliases)
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

// davAlias maps one of the alternative mount points back onto this server's.
//
// The prefix was the whole difference. Method, body and credential belong to
// the protocol and pass through untouched. A path matching no alias reports
// false.
//
// Longest prefix first, because one alias can be a prefix of another: the
// vendor addresses chunked uploads under the same root as the share tree, and
// the shorter match would send a transfer into the tree as a directory named
// "uploads".
func davAlias(urlPath string, aliases []DavAlias) (string, bool) {
	best := -1
	for i, a := range aliases {
		prefixNoSlash := strings.TrimSuffix(a.Prefix, "/")
		if urlPath != prefixNoSlash && !strings.HasPrefix(urlPath, a.Prefix) {
			continue
		}
		if best < 0 || len(a.Prefix) > len(aliases[best].Prefix) {
			best = i
		}
	}
	if best < 0 {
		return "", false
	}

	a := aliases[best]
	rest := ""
	if strings.HasPrefix(urlPath, a.Prefix) {
		rest = strings.TrimPrefix(urlPath, a.Prefix)
	}
	for range a.DropSegments {
		rest = strings.TrimPrefix(rest, "/")
		i := strings.IndexByte(rest, '/')
		if i < 0 {
			rest = ""
			break
		}
		rest = rest[i:]
	}
	mount := a.Mount
	if mount == "" {
		mount = DavPrefix
	}
	for strings.Contains(rest, "//") {
		rest = strings.ReplaceAll(rest, "//", "/")
	}
	if strings.HasSuffix(mount, "/") && strings.HasPrefix(rest, "/") {
		rest = rest[1:]
	}
	return mount + rest, true
}

// resolveDav turns a mount-relative URL into a resolution.
//
// The protocol package owns the splitting, which decodes per segment after the
// split so an encoded separator cannot introduce a boundary. A second decoder
// here would be a second set of rules about what a path may contain, and the
// weaker of the two would be the one that decides.
func (e *Engine) resolveDav(
	user core.UserID, urlPath string, want acl.Perms,
) (core.Resolved, error) {
	parts, err := dav.SplitPath(strings.TrimPrefix(urlPath, DavPrefix))
	if err != nil {
		return core.Resolved{}, apierr.BadRequest("dav.bad_path", "path")
	}

	vp, perr := vfs.ParseVpath(strings.Join(parts, "/"))
	if perr != nil {
		return core.Resolved{}, core.ErrNotFound
	}
	return e.Core.Resolve(user, vp, want)
}

// davDestination resolves the header a MOVE or a COPY names its target with.
//
// The aliases are the mount's own: a client addressing this server through an
// alias names its destination the same way, and a destination that skipped the
// rewrite resolves somewhere else or not at all.
func (e *Engine) davDestination(
	user core.UserID, r *http.Request, aliases []DavAlias,
) (dav.Target, error) {
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

	// Back to a path so the alias rewrite and the mount prefix are stripped the
	// one way, rather than this growing its own copy of both.
	path := "/" + strings.Join(segments, "/")
	if rewritten, aliased := davAlias(path, aliases); aliased {
		path = rewritten
	}

	// Read here too, for the reason the request path uses it: the copy and the
	// move check what they need against the resolution they are handed.
	res, rerr := e.resolveDav(user, path, acl.Read)
	if rerr != nil {
		return dav.Target{}, rerr
	}
	return dav.Target{
		Resolved:  res,
		Overwrite: dav.Overwrite(r.Header.Get("Overwrite")),
	}, nil
}
