// Linux only: it depends on packages that are Linux only.
//go:build linux

package server

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/dav"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/mw"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The WebDAV mount.
//
// The protocol package is handed resolved paths and never parses a virtual
// one, so this is where a URL becomes a resolution and a method becomes a
// dispatch. Keeping the parse here rather than in the package is what keeps a
// second path parser from existing.

// davPrefix is the mount point. Everything under it is WebDAV.
const davPrefix = "/dav"

// davAliases are extra mount points that address the same tree, supplied by
// the compatibility layer when it is built in. They are data rather than
// constants here because the names belong to another product's protocol, and
// this package is core: the gate keeps that vocabulary behind the seam.
//
// Each entry is a prefix and how many leading segments to drop after it. A
// sync client addresses a tree by account name, and that segment is dropped
// rather than checked: this server resolves against the caller's own roots, so
// a name in the URL cannot widen what the credential already allows.
// DavAlias is one alternative mount point.
type DavAlias struct {
	Prefix string
	// DropSegments is how many path segments after the prefix name something
	// other than a file, such as the account the client thinks it is browsing.
	DropSegments int
}

// davAlias rewrites an alternative mount point onto this server's own.
//
// It returns the path as the resolver below expects it, and false for a URL
// that is not one of the aliases. Nothing else about the request changes: the
// method, the body and the credential are the protocol's, and only the prefix
// was ever different.
func davAlias(urlPath string, aliases []DavAlias) (string, bool) {
	for _, a := range aliases {
		rest, ok := strings.CutPrefix(urlPath, a.Prefix)
		if !ok {
			continue
		}
		for range a.DropSegments {
			i := strings.IndexByte(rest, '/')
			if i < 0 {
				return davPrefix, true
			}
			rest = rest[i+1:]
		}
		if rest == "" {
			return davPrefix, true
		}
		return davPrefix + "/" + rest, true
	}
	return "", false
}

// davMount turns a request URL into a resolved path and dispatches it.
func davMount(h *dav.Handler, c *core.Core, aliases []DavAlias) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Discovery answers without a credential, because it is how a client
		// learns this server speaks the protocol at all, and a client that
		// cannot discover that never goes on to authenticate. It reveals the
		// method list and nothing about what is stored.
		if r.Method == http.MethodOptions {
			davOptions(w)
			return
		}

		p, ok := mw.PrincipalFrom(r.Context())
		if !ok {
			// A WebDAV client does not know to send a credential until it is
			// asked, and the challenge is what asks. Without it the client
			// reports a failure rather than prompting, which looks like a
			// broken server to whoever is holding it.
			w.Header().Set("WWW-Authenticate", `Basic realm="WebDAV", charset="UTF-8"`)
			apierr.Write(w, http.StatusUnauthorized,
				apierr.NewError(apierr.CodeAuthRequired, "authentication required", ""))
			return
		}

		user := core.UserID(p.UserID)
		path := r.URL.Path
		if rewritten, ok := davAlias(path, aliases); ok {
			path = rewritten
		}
		res, err := resolveDavPath(c, user, path, davPermFor(r.Method))
		if err != nil {
			writeDavError(w, err)
			return
		}

		switch r.Method {
		case "MOVE", "COPY":
			// The destination arrives as a URL in a header, which is why these
			// two are separate entry points: turning that into a resolution is
			// this layer's job.
			target, terr := davDestination(c, user, r)
			if terr != nil {
				writeDavError(w, terr)
				return
			}
			if r.Method == "MOVE" {
				h.ServeMove(w, r, res, target)
				return
			}
			h.ServeCopy(w, r, res, target)
		default:
			h.ServeMethod(w, r, res)
		}
	})
}

// resolveDavPath turns a mount-relative URL into a resolution.
//
// The path is percent-decoded per segment, after splitting, so an encoded
// separator cannot introduce a segment boundary. Decoding first and splitting
// after is the classic way a path-mapping layer is walked out of its root.
func resolveDavPath(c *core.Core, user core.UserID, urlPath string, want acl.Perms) (core.Resolved, error) {
	rest := strings.TrimPrefix(urlPath, davPrefix)
	rest = strings.TrimPrefix(rest, "/")

	var parts []string
	for _, seg := range strings.Split(rest, "/") {
		if seg == "" {
			continue
		}
		decoded, err := url.PathUnescape(seg)
		if err != nil || strings.ContainsRune(decoded, '/') {
			return core.Resolved{}, apierr.BadRequest("dav.bad_path", "path")
		}
		parts = append(parts, decoded)
	}

	vp, err := vfs.ParseVpath(strings.Join(parts, "/"))
	if err != nil {
		return core.Resolved{}, err
	}
	return c.Resolve(user, vp, want)
}

// davDestination resolves the header a move or a copy names its target with.
func davDestination(c *core.Core, user core.UserID, r *http.Request) (dav.MoveTarget, error) {
	raw := r.Header.Get("Destination")
	if raw == "" {
		return dav.MoveTarget{}, apierr.BadRequest("dav.no_destination", "Destination")
	}
	// The header carries an absolute URL as often as a path, and only its path
	// component names anything here.
	u, err := url.Parse(raw)
	if err != nil {
		return dav.MoveTarget{}, apierr.BadRequest("dav.bad_destination", "Destination")
	}
	// A destination on another origin is refused rather than having its host
	// quietly dropped. The path alone was taken, so COPY to
	// https://elsewhere.example/dav/docs/x copied to /dav/docs/x here: a
	// request that names somewhere else was answered as though it named this
	// server. RFC 4918 9.8.3 makes a destination this server cannot write a
	// 502.
	if u.Host != "" && !sameOrigin(u, r) {
		return dav.MoveTarget{}, apierr.BadGateway("dav.foreign_destination", "Destination")
	}
	res, rerr := resolveDavPath(c, user, u.Path, acl.Write|acl.Create)
	if rerr != nil {
		return dav.MoveTarget{}, rerr
	}
	return dav.MoveTarget{
		Resolved:  res,
		Overwrite: dav.ParseOverwrite(r.Header.Get("Overwrite")),
	}, nil
}

// sameOrigin reports whether an absolute Destination names this server.
//
// Host only, and case-insensitively: the scheme is not compared because a
// reverse proxy terminates TLS and forwards http, so requiring the schemes to
// match would refuse every move behind one.
func sameOrigin(u *url.URL, r *http.Request) bool {
	return strings.EqualFold(u.Host, r.Host)
}

// davPermFor is what a method needs before the path is even opened.
//
// A method that writes is checked as a write here, so a caller with read
// access is refused at resolution rather than part way through an operation.
func davPermFor(method string) acl.Perms {
	switch method {
	case "PUT", "MKCOL", "PROPPATCH", "LOCK", "UNLOCK":
		return acl.Write
	case "DELETE":
		return acl.Delete
	case "MOVE":
		return acl.Write | acl.Delete
	case "COPY":
		return acl.Read
	}
	return acl.Read
}

// writeDavError maps a resolution failure onto a status.
//
// It goes through the same mapper the native surface uses, because the failures
// at this point are the domain's rather than the protocol's: a path that does
// not exist and one the caller may not know about answer identically here too.
func writeDavError(w http.ResponseWriter, err error) {
	status, body := apierr.Map(err)
	apierr.Write(w, status, body)
}

// davOptions answers discovery.
//
// The class says locking is offered, which is what makes a client use it
// rather than assuming a server that only pretends to.
func davOptions(w http.ResponseWriter) {
	h := w.Header()
	h.Set("DAV", "1, 2")
	h.Set("Allow", strings.Join(davMethods(), ", "))
	h.Set("MS-Author-Via", "DAV")
	w.WriteHeader(http.StatusOK)
}

// davMethods is what this mount answers.
func davMethods() []string {
	return []string{
		"OPTIONS", "GET", "HEAD", "PUT", "DELETE",
		"PROPFIND", "PROPPATCH", "MKCOL", "COPY", "MOVE", "LOCK", "UNLOCK",
	}
}
