//go:build linux

package dav

import (
	"net/http"
)

// The virtual root, the one collection with nothing on disk behind it.
//
// Every other PROPFIND resolves to a directory. This one cannot: it is the
// projection of the caller's grants, one child per share, and there is nothing
// to stat. Resolve refuses it deliberately, since a path outside every grant
// and a path that is not there have to give the same answer.
//
// A client that cannot list the root cannot start. The sync clients read it
// before they will finish adding an account, so answering 404 makes a server
// look unreachable right after a sign-in that worked.

// RootChild is one share as the root lists it.
type RootChild struct {
	// Label names the share as the caller's own grant names it.
	Label string
}

// RootPropfind answers PROPFIND on the virtual root.
//
// Nothing below a child is walked, whatever depth was asked for. Each child is
// a separate share root, and a client that wants one asks for it by name.
func (h *Handler) RootPropfind(w http.ResponseWriter, r *http.Request, roots []RootChild) {
	req, err := ParsePropFind(http.MaxBytesReader(w, r.Body, h.limits.Bytes), h.limits)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	depth, derr := ParseDepth(r.Header.Get("Depth"), DepthInfinity,
		DepthZero, DepthOne, DepthInfinity)
	if derr != nil {
		h.fail(w, r, derr)
		return
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)

	m := NewMultistatus(w, requestedNamespaces(req))

	// The root carries the one property that makes it a collection. No size,
	// no change token, no birth time: it is a projection rather than a
	// directory, and inventing values a client would cache and compare beats
	// omitting them only until the comparison matters.
	m.Response("/", []PropStat{{
		Status: http.StatusOK,
		Props:  []Prop{collectionType()},
	}})

	if depth != DepthZero {
		for _, c := range roots {
			m.Response(EncodeHref([]string{c.Label}, true), []PropStat{{
				Status: http.StatusOK,
				Props:  []Prop{collectionType()},
			}})
			if m.Err() != nil {
				h.log(r).Warn("the root listing stopped early", "error", m.Err())
				break
			}
		}
	}

	h.closeMultistatus(r, m)
}

// collectionType is the resourcetype every entry in the root carries.
func collectionType() Prop {
	return Prop{
		Name:     davName("resourcetype"),
		Children: []Element{{Name: davName("collection")}},
	}
}
