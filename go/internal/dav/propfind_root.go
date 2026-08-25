//go:build linux

package dav

import (
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// The virtual root, which is the one collection with no path behind it.
//
// Every other PROPFIND resolves to a directory on disk. The root does not: it
// is the projection of the caller's own grants, one child per share, and there
// is nothing to stat. Resolve refuses it by design, because a path outside
// every grant and a path that does not exist have to be the same answer.
//
// A client that cannot list it cannot begin. The sync clients check the root
// before they will create an account, so a server that answers 404 there is
// one they report as unreachable after signing in successfully.

// RootChild is one share as the virtual root lists it.
type RootChild struct {
	Label string
}

// ServeRootPropfind answers PROPFIND on the virtual root.
//
// The children are the caller's readable shares, named as their own grants
// name them. Nothing below is walked even at Depth: infinity, because each
// child is a separate share root and a client that wants one asks for it.
func (h *Handler) ServeRootPropfind(w http.ResponseWriter, r *http.Request, roots []RootChild) {
	body, err := readBody(r, limits.RequestBodyXML)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if _, perr := ParsePropFind(body, h.limits); perr != nil {
		h.fail(w, r, perr)
		return
	}
	depth, derr := ParseDepth(r.Header.Get("Depth"), DepthInfinity)
	if derr != nil {
		h.fail(w, r, derr)
		return
	}

	base := hrefOf(r.URL.Path, true)
	m := NewMultistatus(w, h.namespaces())

	// The root itself carries the one property that makes it a collection. It
	// has no size, no change token and no birth time: it is a projection
	// rather than a directory, and inventing values a client could cache and
	// compare against would be worse than omitting them.
	if werr := m.Write(Response{
		Href:  base,
		Found: []Prop{{Name: DavName("resourcetype"), Raw: "<" + davPrefix + ":collection/>"}},
	}); werr != nil {
		h.logger(r).Warn("the root collection could not be written", "error", werr)
		_ = m.Close() //nolint:errcheck // the write error above is the answer.
		return
	}

	if depth != DepthZero {
		for _, c := range roots {
			if werr := m.Write(Response{
				Href:  base + escapeSegment(c.Label) + "/",
				Found: []Prop{{Name: DavName("resourcetype"), Raw: "<" + davPrefix + ":collection/>"}},
			}); werr != nil {
				h.logger(r).Warn("a share could not be written", "error", werr)
				break
			}
		}
	}

	if cerr := m.Close(); cerr != nil {
		h.logger(r).Warn("the multistatus could not be closed", "error", cerr)
	}
}
