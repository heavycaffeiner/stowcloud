//go:build linux

package dav

import (
	"context"
	"net/http"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// SEARCH (RFC 5323) and the filter-files REPORT (RFC 3253).
//
// Both are load-bearing for the mobile clients: the favourites view on both
// phone apps is a REPORT. The design constraint is that this package must not
// learn the vendor vocabulary, so a query's property comparisons are collected
// verbatim as resolved names and values and handed to whichever registered
// source claims the namespace. The source decides what a comparison means;
// this package decides only that the request was well formed and bounded.

// QuerySource answers a SEARCH or a REPORT for the namespaces it claims.
//
// It is separate from PropSource because contributing a property to a listing
// and answering a query are different jobs, and a source may do either.
type QuerySource interface {
	PropSource
	// Query returns the entries matching every leaf, which are the filter
	// terms as they appeared in the body. A source that cannot apply a term it
	// was given must return an error rather than ignore it: silently dropping
	// a filter returns more than the client asked for, which for a favourites
	// view means returning the whole tree.
	Query(ctx context.Context, res core.Resolved, leaves []Leaf, want []Name) ([]core.Entry, error)
}

func (h *Handler) search(w http.ResponseWriter, r *http.Request, res core.Resolved) {
	body, err := readBody(r, limits.RequestBodyXML)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// A SEARCH body is XML from a stranger and gets the same caps a PROPFIND
	// body does: the DTD refusal, the element count, the depth and the name
	// length are all unchanged.
	parsed, perr := ParseReport(body, h.limits)
	if perr != nil {
		h.fail(w, r, perr)
		return
	}
	h.runQuery(w, r, res, parsed)
}

func (h *Handler) report(w http.ResponseWriter, r *http.Request, res core.Resolved) {
	body, err := readBody(r, limits.RequestBodyXML)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	parsed, perr := ParseReport(body, h.limits)
	if perr != nil {
		h.fail(w, r, perr)
		return
	}
	h.runQuery(w, r, res, parsed)
}

// runQuery dispatches a parsed body to whichever source claims its root.
func (h *Handler) runQuery(w http.ResponseWriter, r *http.Request, res core.Resolved, body ReportBody) {
	src := h.queryFor(body.Root.Space)
	if src == nil {
		// Nobody claims this vocabulary, so the server genuinely cannot run
		// the report. Answering with an empty multistatus would look like
		// "nothing matched", which is a different and wrong answer.
		h.fail(w, r, ErrBadRequest)
		return
	}

	want := body.Props
	if len(want) == 0 {
		want = []Name{DavName("getetag")}
	}

	hits, err := src.Query(r.Context(), res, body.Leaves, want)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	req := PropFind{Mode: PropFindNamed, Props: want}
	m := NewMultistatus(w, h.namespaces())
	base := hrefOf(r.URL.Path, true)
	for _, e := range hits {
		// Each hit needs its own href or a client cannot tell two results
		// apart. The share-relative path is what identifies it; the name is the
		// fallback for a source that reports only that.
		rel := strings.TrimPrefix(e.Path.String(), "/")
		if rel == "" {
			rel = e.Name
		}
		href := base + rel
		if e.IsDir && !strings.HasSuffix(href, "/") {
			href += "/"
		}
		if werr := h.writeEntry(r.Context(), m, req, res, e, href); werr != nil {
			h.logger(r).Warn("the query result could not be written", "error", werr)
			break
		}
	}
	if cerr := m.Close(); cerr != nil {
		h.logger(r).Warn("the multistatus could not be closed", "error", cerr)
	}
}

// queryFor finds the source claiming a namespace.
func (h *Handler) queryFor(ns string) QuerySource {
	for _, s := range h.sources {
		q, ok := s.(QuerySource)
		if !ok {
			continue
		}
		for _, claimed := range s.Namespaces() {
			if claimed == ns {
				return q
			}
		}
	}
	return nil
}
