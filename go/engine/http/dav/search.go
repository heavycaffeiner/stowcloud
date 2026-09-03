//go:build linux

package dav

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// The refusal SEARCH and REPORT make.
//
// ErrNoQuerySource reports a vocabulary no source claims. Answering an empty
// multistatus would read as "nothing matched", which is a different and wrong
// answer: the client would show an empty view and never learn the server
// cannot run the report at all.
var ErrNoQuerySource = errors.New("no source claims this vocabulary")

// QuerySource runs a query for the vocabularies it claims.
//
// A source that cannot apply a term it was given returns an error rather
// than ignoring it. A dropped filter returns more than the client asked
// for, and for a favourites view that means everything, unmarked: the view
// the client meant to see and the whole tree are both "matches", and only
// one of them is right.
type QuerySource interface {
	// Namespaces claims the vocabularies this source answers for.
	Namespaces() []string
	// Query returns the entries the leaves select, each leaf being a filter
	// term exactly as the body carried it. want names the properties the
	// response should carry.
	Query(ctx context.Context, res core.Resolved, leaves []Leaf, want []xml.Name) ([]core.Entry, error)
}

// Search answers SEARCH.
func (h *Handler) Search(w http.ResponseWriter, r *http.Request, res core.Resolved) {
	h.runQuery(w, r, res)
}

// Report answers REPORT. The two share one body shape.
func (h *Handler) Report(w http.ResponseWriter, r *http.Request, res core.Resolved) {
	h.runQuery(w, r, res)
}

// runQuery parses the body and dispatches it to the claiming source.
func (h *Handler) runQuery(w http.ResponseWriter, r *http.Request, res core.Resolved) {
	parsed, perr := ParseReport(http.MaxBytesReader(w, r.Body, h.limits.Bytes), h.limits)
	if perr != nil {
		h.fail(w, r, perr)
		return
	}

	src := h.queryFor(parsed.Root.Space)
	if src == nil {
		// Nobody claims this vocabulary, so the report cannot run. An empty
		// multistatus reads as "nothing matched", which is a different claim
		// and a wrong one: the client would show an empty view and never
		// learn the server could not answer at all.
		h.fail(w, r, ErrNoQuerySource)
		return
	}

	want := parsed.Props
	if len(want) == 0 {
		want = []xml.Name{davName("getetag")}
	}

	hits, err := src.Query(r.Context(), res, parsed.Leaves, want)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	req := PropFind{Mode: ModeNamed, Names: want}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)

	m := NewMultistatus(w, requestedNamespaces(req))
	// Hrefs grow from the path the client addressed, with the hit's own path
	// relative to the queried collection appended. A hit outside that
	// collection has no href the client could request and is left out: a
	// source answering across collections must answer per collection.
	segs, serr := SplitPath(r.URL.EscapedPath())
	if serr != nil {
		h.fail(w, r, serr)
		return
	}
	scope := ""
	if !res.Path().IsRoot() {
		scope = res.Path().String() + "/"
	}
	for _, e := range hits {
		// The source vouches for the entry; its share-relative path arrives
		// through the core's own validated types. The slash-split here only
		// separates components of that path, never introduces one.
		rel := strings.TrimPrefix(e.Path.String(), scope)
		href := EncodeHref(append(append([]string{}, segs...), strings.Split(rel, "/")...), e.IsDir)
		if werr := h.writeEntry(r.Context(), m, req, res, e, href); werr != nil {
			h.log(r).Warn("the query result could not be written", "error", werr)
			break
		}
	}
	h.closeMultistatus(r, m)
}

// RootScope carries one share root and its label for root queries.
type RootScope struct {
	Label    string
	Resolved core.Resolved
}

// RootQuery answers SEARCH or REPORT against the virtual root across every share.
func (h *Handler) RootQuery(w http.ResponseWriter, r *http.Request, roots []RootScope) {
	parsed, perr := ParseReport(http.MaxBytesReader(w, r.Body, h.limits.Bytes), h.limits)
	if perr != nil {
		h.fail(w, r, perr)
		return
	}

	src := h.queryFor(parsed.Root.Space)
	if src == nil {
		h.fail(w, r, ErrNoQuerySource)
		return
	}

	want := parsed.Props
	if len(want) == 0 {
		want = []xml.Name{davName("getetag")}
	}

	req := PropFind{Mode: ModeNamed, Names: want}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)

	m := NewMultistatus(w, requestedNamespaces(req))
	segs, serr := SplitPath(r.URL.EscapedPath())
	if serr != nil {
		h.fail(w, r, serr)
		return
	}

	for _, scope := range roots {
		hits, err := src.Query(r.Context(), scope.Resolved, parsed.Leaves, want)
		if err != nil {
			continue
		}
		base := append(append([]string{}, segs...), scope.Label)
		for _, e := range hits {
			parts := append([]string{}, base...)
			rel := strings.TrimPrefix(e.Path.String(), "/")
			if rel != "" {
				parts = append(parts, strings.Split(rel, "/")...)
			}
			href := EncodeHref(parts, e.IsDir)
			if werr := h.writeEntry(r.Context(), m, req, scope.Resolved, e, href); werr != nil {
				h.log(r).Warn("the query result could not be written", "error", werr)
				break
			}
		}
	}
	h.closeMultistatus(r, m)
}

// searchMethods names what the registered sources make available. SEARCH and
// REPORT share the same machinery and the same sources, so they appear
// together: advertising one without the other invites a request the server
// then refuses.
func (h *Handler) searchMethods() []string {
	if !h.searchEnabled() {
		return nil
	}
	return []string{"SEARCH", "REPORT"}
}

// queryFor picks the source whose claimed vocabulary includes the report's
// root. First match wins, since two sources claiming one namespace would make
// the answer depend on registration order.
func (h *Handler) queryFor(ns string) QuerySource {
	for _, s := range h.sources {
		for _, claimed := range s.Namespaces() {
			if claimed == ns {
				return s
			}
		}
	}
	return nil
}
