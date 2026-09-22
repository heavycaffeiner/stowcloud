//go:build linux

package dav

import (
	"encoding/xml"
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
	// Props are optional properties contributed by the caller.
	Props []Prop
}

// RootPropfind answers PROPFIND on the virtual root.
//
// Nothing below a child is walked, whatever depth was asked for. Each child is
// a separate share root, and a client that wants one asks for it by name.
func (h *Handler) RootPropfind(w http.ResponseWriter, r *http.Request, baseProps []Prop, roots []RootChild) {
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

	m := NewMultistatus(w, rootNamespaces(req, baseProps, roots))

	// The root carries the one property that makes it a collection. No size,
	// no change token, no birth time: it is a projection rather than a
	// directory, and inventing values a client would cache and compare beats
	// omitting them only until the comparison matters.
	//
	// The hrefs grow from the path the client addressed, so the members it
	// reads back are the members it can request: a client mounting through
	// the compatibility prefix reads its shares under that prefix, not under
	// this server's own.
	segs, serr := SplitPath(r.URL.EscapedPath())
	if serr != nil {
		h.fail(w, r, serr)
		return
	}
	base := EncodeHref(segs, true)
	found, missing := assembleRoot(req, baseProps)
	stats := []PropStat{{Status: http.StatusOK, Props: found}}
	if len(missing) > 0 {
		absent := make([]Prop, 0, len(missing))
		for _, n := range missing {
			absent = append(absent, Prop{Name: n, NamesOnly: true})
		}
		stats = append(stats, PropStat{Status: http.StatusNotFound, Props: absent})
	}
	m.Response(base, stats)

	if depth != DepthZero {
		for _, c := range roots {
			cFound, cMissing := assembleRoot(req, c.Props)
			cStats := []PropStat{{Status: http.StatusOK, Props: cFound}}
			if len(cMissing) > 0 {
				absent := make([]Prop, 0, len(cMissing))
				for _, n := range cMissing {
					absent = append(absent, Prop{Name: n, NamesOnly: true})
				}
				cStats = append(cStats, PropStat{Status: http.StatusNotFound, Props: absent})
			}
			href := EncodeHref(append(segs[:len(segs):len(segs)], c.Label), true)
			m.Response(href, cStats)
			if m.Err() != nil {
				h.log(r).Warn("the root listing stopped early", "error", m.Err())
				break
			}
		}
	}

	h.closeMultistatus(r, m)
}

func assembleRoot(req PropFind, props []Prop) (found []Prop, missing []xml.Name) {
	hasCollection := false
	for _, p := range props {
		if p.Name == davName("resourcetype") {
			hasCollection = true
			break
		}
	}
	all := props
	if !hasCollection {
		all = append([]Prop{collectionType()}, props...)
	}

	switch req.Mode {
	case ModePropName:
		for _, p := range all {
			found = append(found, Prop{Name: p.Name, NamesOnly: true})
		}
	case ModeAllProp:
		found = append(found, all...)
	case ModeNamed:
		for _, n := range req.Names {
			matched := false
			for _, p := range all {
				if p.Name == n {
					found = append(found, p)
					matched = true
					break
				}
			}
			if !matched {
				missing = append(missing, n)
			}
		}
	}
	return found, missing
}

func rootNamespaces(req PropFind, baseProps []Prop, roots []RootChild) []string {
	seen := map[string]bool{}
	var out []string
	add := func(ns string) {
		if ns == "" || ns == davNS || seen[ns] {
			return
		}
		seen[ns] = true
		out = append(out, ns)
	}
	for _, ns := range requestedNamespaces(req) {
		add(ns)
	}
	for _, p := range baseProps {
		add(p.Name.Space)
	}
	for _, c := range roots {
		for _, p := range c.Props {
			add(p.Name.Space)
		}
	}
	return out
}

// collectionType is the resourcetype every entry in the root carries.
func collectionType() Prop {
	return Prop{
		Name:     davName("resourcetype"),
		Children: []Element{{Name: davName("collection")}},
	}
}
