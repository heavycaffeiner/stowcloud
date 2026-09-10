//go:build linux && compat_nc

package nc

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search/svc"
)

// The oc:filter-files REPORT and the DAV SEARCH method.
//
// Both answer a 207 built the same way as PROPFIND, from whatever entries
// the underlying question resolves, and both scope their answer to what the
// caller may read: an entry a resolve call refuses is left out of the
// listing rather than surfaced as a per-entry failure, the same rule
// PROPFIND's own child walk follows.

// davSearchWalkCeiling bounds a bounded fallback walk, the same ceiling
// PROPFIND's own depth-one listing uses. It exists for the same reason: an
// unbounded recursive walk of a caller's whole tree inside one request is a
// denial of service this server can mount against itself.
const davSearchWalkCeiling = limits.DavInfinityEntries

// davReport answers the oc:filter-files REPORT.
//
// A favourite filter is the only one this deployment can answer from a real
// index; anything else gets an empty multistatus rather than a refusal,
// which is what every client here treats a REPORT it does not recognise as
// meaning nothing matched, not that the server is broken.
func (s *Server) davReport(w http.ResponseWriter, r *http.Request, p Principal, t Target) {
	ctx := r.Context()
	fq, err := ParseFilterFiles(r.Body)
	if err != nil {
		s.failDav(w, r, err, apierr.VisibilityKnown)
		return
	}

	query := fq.Query
	if len(query.Names) == 0 {
		query.All = true
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)

	m := NewMulti(w)
	m.Open()

	if fq.Favorite {
		ownerID, ownerName := s.ownerNames(ctx, p)
		for _, hit := range s.favoriteHits(ctx, p) {
			found, missing := s.entryProps(ctx, EntryProps{
				Query: query, Entry: hit.entry, Perms: hit.res.Perms(), Favorite: true,
				OwnerID: ownerID, OwnerName: ownerName,
			})
			m.Response(t.Href(splitVpath(hit.vpath), hit.entry.IsDir), found, missing)
		}
	}

	if cerr := m.Close(); cerr != nil {
		s.log.Warn("a filter-files response was not delivered", "error", cerr)
	}
}

// splitVpath breaks a wire vpath into the components Target.Href appends.
func splitVpath(vpath string) []string {
	vpath = strings.Trim(vpath, "/")
	if vpath == "" {
		return nil
	}
	return strings.Split(vpath, "/")
}

// favoriteHits is the caller's starred set, resolved and stat'd, in the shape
// both the filter REPORT and the equivalent SEARCH render from.
//
// A row whose file the caller can no longer reach is dropped rather than
// surfaced: the star still exists, but the existence rule every other listing
// here follows says nothing about a path this caller may not see.
func (s *Server) favoriteHits(ctx context.Context, p Principal) []searchHit {
	set, err := s.deps.Store.Favorites(ctx, user(p))
	if err != nil {
		s.log.Warn("the favourite set could not be read", "error", err)
		return nil
	}
	rows := set.List()
	out := make([]searchHit, 0, len(rows))
	for _, fav := range rows {
		vpath, ok := s.vpathOfFavorite(user(p), fav)
		if !ok {
			continue
		}
		res, rerr := s.resolve(ctx, p, vpath, acl.Read)
		if rerr != nil {
			continue
		}
		entry, serr := s.deps.Core.Stat(ctx, res)
		if serr != nil {
			continue
		}
		out = append(out, searchHit{vpath: vpath, entry: entry, res: res})
	}
	return out
}

// davSearch answers the DAV SEARCH method.
//
// The where clause is reduced to one of four shapes this surface actually
// sees on the wire; anything it does not recognise answers an empty
// multistatus, the same as a filter REPORT this deployment cannot run.
func (s *Server) davSearch(w http.ResponseWriter, r *http.Request, p Principal, t Target) {
	ctx := r.Context()
	sq, err := ParseSearchRequest(r.Body)
	if err != nil {
		s.failDav(w, r, err, apierr.VisibilityKnown)
		return
	}

	query := PropQuery{Names: sq.Select}
	if len(query.Names) == 0 {
		query.All = true
	}

	scopeVpath := joinComponents(scopeComponents(sq.ScopeHref))
	// A body that names no row count is asking for the answer, not for the
	// first page of it: the phone's own search sends exactly that, and a
	// hundred rows of a thousand matches reads as "this is all there is".
	limit, complete := searchRowsOf(sq.Limit)
	href := searchHrefTarget(t, s.loginNameOf(ctx, p))
	ownerID, ownerName := s.ownerNames(ctx, p)
	favSet := s.favoriteSet(ctx, p, query)

	var entries []searchHit
	switch {
	case sq.Where.Op == "":
		// No where clause at all: nothing to run.

	case hasFavoriteEq(sq.Where):
		// One client asks for the starred set with a search rather than with
		// the filter report, and it is the same question: without this branch
		// its favourites screen is empty while the report answers correctly.
		entries = s.favoriteHits(ctx, p)

	case hasSearchFileID(sq.Where):
		if lit, ok := findFileIDLiteral(sq.Where); ok {
			entries = s.searchByFileID(ctx, p, lit)
		}

	case hasContentTypeLike(sq.Where):
		lower, upper := searchWindow(sq.Where)
		entries = s.searchByMedia(ctx, p, scopeVpath, lower, upper, limit, complete, sq.Descending)

	case hasNameLike(sq.Where):
		if lit, ok := findNameLike(sq.Where); ok {
			entries = s.searchByName(ctx, p, scopeVpath, lit, limit, complete)
		}

	case hasTimeComparison(sq.Where):
		lower, _ := searchWindow(sq.Where)
		entries = s.searchByModTime(ctx, p, scopeVpath, lower, limit, complete, sq.Descending)
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)

	m := NewMulti(w)
	m.Open()
	for _, h := range entries {
		found, missing := s.entryProps(ctx, EntryProps{
			Query: query, Entry: h.entry, Perms: h.res.Perms(),
			Favorite:  favSet != nil && favSet.Has(h.entry),
			OwnerID:   ownerID,
			OwnerName: ownerName,
		})
		m.Response(href.Href(splitVpath(h.vpath), h.entry.IsDir), found, missing)
	}
	if cerr := m.Close(); cerr != nil {
		s.log.Warn("a search response was not delivered", "error", cerr)
	}
}

// searchHit pairs a resolved capability with the entry it names, since
// rendering needs both and a walk or a lookup produces both together.
type searchHit struct {
	res   core.Resolved
	entry core.Entry
	// vpath is the wire path a client requests this hit back under: the
	// share label the caller navigates the share as, not the share's own
	// internal path, which a resolve call never sees and a client cannot
	// address.
	vpath string
}

// searchHrefTarget is the Target hrefs in a SEARCH response grow from.
//
// A SEARCH request's own path is the bare mount: the scope that actually
// matters lives inside the body, and a result can name a file under any
// share the caller reads, not just whichever tree the request happened to
// address. Rendered under the files layout unconditionally, since every
// client that sends SEARCH reads its results back from that layout.
func searchHrefTarget(t Target, login string) Target {
	if t.Kind == KindFiles {
		return t
	}
	return Target{Prefix: t.Prefix + "/files/" + escapeSegment(login)}
}

// scopeComponents reads a search body's scope href into the components a
// files-tree vpath is built from: the tree at the front and the account
// segment right after it name the request layout itself, not part of the
// path being searched.
func scopeComponents(href string) []string {
	href = strings.Trim(href, "/")
	if href == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(href, "/") {
		if part == "" {
			continue
		}
		if dec, derr := url.PathUnescape(part); derr == nil {
			part = dec
		}
		out = append(out, part)
	}
	if len(out) > 0 && out[0] == "files" {
		out = out[1:]
		if len(out) > 0 {
			out = out[1:]
		}
	}
	return out
}

// searchRowsOf reads a client-supplied row count.
//
// A stated count is honoured as stated, since the client is paging and knows
// what it asked for. An absent one asks for everything the query matches,
// which is what the reference server answers and what the phone's search
// screen expects: it sends no count at all.
//
// The second value says which of the two happened, because "no ceiling" is
// not a number a limit field can hold.
func searchRowsOf(n int) (limit int, complete bool) {
	if n <= 0 {
		return 0, true
	}
	return n, false
}

// searchByFileID answers oc:fileid = eq, the one query the client opens the
// first row of without checking it: at most one entry is ever returned.
func (s *Server) searchByFileID(ctx context.Context, p Principal, literal string) []searchHit {
	id, err := strconv.ParseUint(strings.TrimSpace(literal), 10, 64)
	if err != nil || s.deps.LocateFile == nil {
		return nil
	}
	vpath, err := s.deps.LocateFile(ctx, user(p), id)
	if err != nil {
		return nil
	}
	res, err := s.resolve(ctx, p, vpath, acl.Read)
	if err != nil {
		return nil
	}
	entry, err := s.deps.Core.Stat(ctx, res)
	if err != nil {
		return nil
	}
	return []searchHit{{res: res, entry: entry, vpath: vpath}}
}

// searchByName answers d:like on d:displayname, inside the scope the request
// named and to the end when the request named no limit.
func (s *Server) searchByName(
	ctx context.Context, p Principal, scopeVpath, literal string, limit int, complete bool,
) []searchHit {
	pattern := strings.Trim(literal, "%")
	if pattern == "" {
		return nil
	}
	folded := strings.ToLower(pattern)

	if s.deps.Search != nil {
		sources, ok := s.searchSourcesUnder(ctx, p, scopeVpath)
		if !ok {
			return nil
		}
		results, err := s.deps.Search.Query(ctx, sources, svc.QueryOptions{
			Query: pattern, Limit: limit, Complete: complete, Scope: scopeVpath,
		})
		if err != nil {
			return nil
		}
		out := make([]searchHit, 0, len(results.Hits))
		for _, h := range results.Hits {
			res, entry, rok := s.searchResolveAt(ctx, p, h.Path)
			if !rok {
				continue
			}
			out = append(out, searchHit{res: res, entry: entry, vpath: h.Path})
			if !complete && len(out) >= limit {
				break
			}
		}
		return out
	}

	roots := s.searchScopeRoots(ctx, p, scopeVpath)
	hits := s.walkForSearch(ctx, roots, func(e core.Entry) bool {
		return strings.Contains(strings.ToLower(e.Name), folded)
	})
	sort.Slice(hits, func(i, j int) bool { return hits[i].entry.Name < hits[j].entry.Name })
	if !complete && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// searchSourcesUnder is the set of trees one search may walk.
//
// The scope a SEARCH body names is a confinement, not a hint: a client
// asking about one folder must not be answered with matches from its
// siblings, which is what a ranking-only scope did. Without a scope the
// answer is every share the caller reads.
//
// False reports that the scope names nothing this caller can search, which
// is an empty answer rather than an account-wide one.
func (s *Server) searchSourcesUnder(ctx context.Context, p Principal, scopeVpath string) ([]search.Source, bool) {
	all := s.searchSourcesOf(user(p))
	if scopeVpath == "" {
		return all, true
	}
	res, err := s.resolve(ctx, p, scopeVpath, acl.Read)
	if err != nil {
		return nil, false
	}
	for _, src := range all {
		if src.Share != uint32(res.Share()) {
			continue
		}
		// Only the starting point moves. A walked path stays relative to the
		// share root, so the prefix that turns it back into a vpath is still
		// the share's own label: narrowing it too spelled every hit as
		// "/Files/reports/reports/file", which resolves to nothing.
		src.Base = res.Path()
		return []search.Source{src}, true
	}
	return nil, false
}

// searchByMedia answers the image/video gallery query: a bounded scoped walk
// filtered by content type and the modification-time window, newest first
// unless the client asked for the other order.
func (s *Server) searchByMedia(
	ctx context.Context, p Principal, scopeVpath string, lowerNs, upperNs int64, limit int, complete bool, descending bool,
) []searchHit {
	roots := s.searchScopeRoots(ctx, p, scopeVpath)
	hits := s.walkForSearch(ctx, roots, func(e core.Entry) bool {
		if !withinWindow(e.MTimeNs, lowerNs, upperNs) {
			return false
		}
		ct := ContentTypeOf(false, e.Name)
		return strings.HasPrefix(ct, "image/") || strings.HasPrefix(ct, "video/")
	})
	sortByMTime(hits, descending)
	if !complete && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// searchByModTime answers a plain modification-time query through the
// journal-backed recent listing.
func (s *Server) searchByModTime(
	ctx context.Context, p Principal, scopeVpath string, lowerNs int64, limit int, complete bool, descending bool,
) []searchHit {
	q := core.RecentQuery{
		SinceNs: core.RecentSinceOf(lowerNs, s.clk.Now()),
		Limit:   core.RecentLimitOf(limit),
		Scope:   scopeVpath,
	}
	rows, err := s.deps.Core.Recent(ctx, user(p), q)
	if err != nil {
		return nil
	}
	out := make([]searchHit, 0, len(rows))
	for _, row := range rows {
		res, entry, ok := s.searchResolveAt(ctx, p, row.Vpath.String())
		if !ok {
			continue
		}
		out = append(out, searchHit{res: res, entry: entry, vpath: row.Vpath.String()})
	}
	sortByMTime(out, descending)
	if !complete && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// searchResolveAt resolves and stats one vpath through the caller's own
// scope, dropping it silently on a refusal: a search or journal hit is a
// moment-old answer, never a guarantee the path still resolves or is still
// this caller's to see.
func (s *Server) searchResolveAt(ctx context.Context, p Principal, vpath string) (core.Resolved, core.Entry, bool) {
	res, err := s.resolve(ctx, p, vpath, acl.Read)
	if err != nil {
		return core.Resolved{}, core.Entry{}, false
	}
	entry, err := s.deps.Core.Stat(ctx, res)
	if err != nil {
		return core.Resolved{}, core.Entry{}, false
	}
	return res, entry, true
}

// searchRoot pairs a scoped walk's starting resolution with the vpath it
// is addressed under, so a hit found under it can report the wire path a
// client requests it back by.
type searchRoot struct {
	vpath string
	res   core.Resolved
}

// searchScopeRoots resolves the starting points a scoped walk descends
// from: the one path the scope names, or every share the caller may read
// when the scope names the whole account.
func (s *Server) searchScopeRoots(ctx context.Context, p Principal, scopeVpath string) []searchRoot {
	if scopeVpath != "" {
		res, err := s.resolve(ctx, p, scopeVpath, acl.Read)
		if err != nil {
			return nil
		}
		return []searchRoot{{vpath: scopeVpath, res: res}}
	}
	roots := s.roots(ctx, p)
	out := make([]searchRoot, 0, len(roots))
	for _, rt := range roots {
		res, err := s.resolve(ctx, p, rt.Label, acl.Read)
		if err != nil {
			continue
		}
		out = append(out, searchRoot{vpath: rt.Label, res: res})
	}
	return out
}

// walkForSearch descends every (vpath, resolution) root, bounded by
// davSearchWalkCeiling total entries visited across the whole call,
// collecting the files match accepts. A directory is never itself a match:
// every query this walk answers is a query for files.
func (s *Server) walkForSearch(
	ctx context.Context, roots []searchRoot, match func(core.Entry) bool,
) []searchHit {
	var out []searchHit
	visited := 0

	var walk func(vpath string, res core.Resolved)
	walk = func(vpath string, res core.Resolved) {
		var cur core.Cursor
		for {
			if visited >= davSearchWalkCeiling || ctx.Err() != nil {
				return
			}
			page, err := s.deps.Core.List(ctx, res, cur)
			if err != nil {
				return
			}
			for _, e := range page.Entries {
				if visited >= davSearchWalkCeiling {
					return
				}
				visited++
				childPath, jerr := res.Path().JoinExisting(e.Name)
				if jerr != nil {
					continue
				}
				child, rerr := s.deps.Core.ResolveUnder(res, childPath, acl.Read)
				if rerr != nil {
					continue
				}
				childVpath := vpath + "/" + e.Name
				if !e.IsDir && match(e) {
					out = append(out, searchHit{res: child, entry: e, vpath: childVpath})
				}
				if e.IsDir {
					walk(childVpath, child)
				}
			}
			if page.Next == "" {
				return
			}
			cur = page.Next
		}
	}
	for _, r := range roots {
		walk(r.vpath, r.res)
	}
	return out
}

// sortByMTime orders search hits newest first, or oldest first when the
// client's orderby asked for ascending.
//
// The client that pages a media or recent query narrows its upper bound to
// the oldest row it was last given, so the order here is load-bearing:
// answering out of order makes that client's scroll never terminate.
func sortByMTime(hits []searchHit, descending bool) {
	sort.Slice(hits, func(i, j int) bool {
		if descending {
			return hits[i].entry.MTimeNs > hits[j].entry.MTimeNs
		}
		return hits[i].entry.MTimeNs < hits[j].entry.MTimeNs
	})
}

// withinWindow reports whether a timestamp falls inside a bound: zero on
// either side imposes no limit on that side.
func withinWindow(ns, lowerNs, upperNs int64) bool {
	if lowerNs != 0 && ns < lowerNs {
		return false
	}
	if upperNs != 0 && ns > upperNs {
		return false
	}
	return true
}

// isSearchTimeProp reports whether a where-clause comparison names one of
// the two properties the reference clients bound a time window on.
func isSearchTimeProp(n PropName) bool {
	return n.Equal(PropLastModified()) || n.Equal(PropUploadTime())
}

// searchWindow collects the tightest lower and upper time bounds anywhere
// in a where clause, zero on either side when the clause named none.
func searchWindow(t SearchTerm) (lowerNs, upperNs int64) {
	switch t.Op {
	case "and", "or":
		for _, c := range t.Terms {
			cl, cu := searchWindow(c)
			if cl > lowerNs {
				lowerNs = cl
			}
			if cu != 0 && (upperNs == 0 || cu < upperNs) {
				upperNs = cu
			}
		}
	case "gt", "gte":
		if isSearchTimeProp(t.Prop) {
			if ns, ok := parseSearchTime(t.Literal); ok && ns > lowerNs {
				lowerNs = ns
			}
		}
	case "lt", "lte":
		if isSearchTimeProp(t.Prop) {
			if ns, ok := parseSearchTime(t.Literal); ok && (upperNs == 0 || ns < upperNs) {
				upperNs = ns
			}
		}
	}
	return lowerNs, upperNs
}

// parseSearchTime reads a where-clause literal as a nanosecond timestamp.
// The clients here spell one two ways: a decimal epoch value, seconds or
// milliseconds, and an ISO 8601 date-time string.
func parseSearchTime(literal string) (int64, bool) {
	literal = strings.TrimSpace(literal)
	if literal == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(literal, 10, 64); err == nil {
		// A value this large cannot be epoch seconds for centuries yet, so
		// a client that sent milliseconds is still read correctly.
		if n > 1_000_000_000_000 {
			n /= 1000
		}
		return n * int64(time.Second), true
	}
	if tm, err := time.Parse("2006-01-02T15:04:05Z07:00", literal); err == nil {
		return tm.UnixNano(), true
	}
	return 0, false
}

// hasSearchFileID reports whether a where clause names oc:fileid anywhere.
func hasSearchFileID(t SearchTerm) bool {
	_, ok := findFileIDLiteral(t)
	return ok
}

// findFileIDLiteral returns the literal an oc:fileid equality names, the
// first found in document order.
func findFileIDLiteral(t SearchTerm) (string, bool) {
	if t.Op == "eq" && sameVendorProp(t.Prop, PropFileID()) {
		return t.Literal, true
	}
	for _, c := range t.Terms {
		if lit, ok := findFileIDLiteral(c); ok {
			return lit, true
		}
	}
	return "", false
}

// hasFavoriteEq reports whether a where clause asks for the starred set.
//
// The literal is whatever the client spells "yes" as, which is not the same
// word in every client, so anything other than an explicit zero counts.
func hasFavoriteEq(t SearchTerm) bool {
	if t.Op == "eq" && sameVendorProp(t.Prop, PropFavorite()) {
		lit := strings.TrimSpace(strings.ToLower(t.Literal))
		return lit != "" && lit != "0" && lit != "no" && lit != "false"
	}
	for _, c := range t.Terms {
		if hasFavoriteEq(c) {
			return true
		}
	}
	return false
}

// sameVendorProp compares two property names, tolerating which of the vendor
// namespaces a client bound the name to. Its clients disagree about that for
// the search vocabulary, and which one they picked says nothing about what
// they are asking for.
func sameVendorProp(got, want PropName) bool {
	if got.Local != want.Local {
		return false
	}
	return isVendorNS(got.NS) && isVendorNS(want.NS)
}

// isVendorNS reports whether a namespace is one of the vendor's own.
func isVendorNS(ns string) bool {
	switch ns {
	case nsOwnCloud, nsNextcloud, nsNextcloudAlt:
		return true
	default:
		return false
	}
}

// hasContentTypeLike reports whether a where clause tests
// d:getcontenttype anywhere, which is what marks a media query.
func hasContentTypeLike(t SearchTerm) bool {
	if t.Op == "like" && t.Prop.Equal(PropContentType()) {
		return true
	}
	for _, c := range t.Terms {
		if hasContentTypeLike(c) {
			return true
		}
	}
	return false
}

// hasNameLike reports whether a where clause tests d:displayname anywhere.
func hasNameLike(t SearchTerm) bool {
	_, ok := findNameLike(t)
	return ok
}

// findNameLike returns the literal a d:displayname like-comparison names.
func findNameLike(t SearchTerm) (string, bool) {
	if t.Op == "like" && t.Prop.Equal(PropDisplayName()) {
		return t.Literal, true
	}
	for _, c := range t.Terms {
		if lit, ok := findNameLike(c); ok {
			return lit, true
		}
	}
	return "", false
}

// hasTimeComparison reports whether a where clause compares one of the two
// time properties anywhere, which is what marks a modification-time query
// once a media query has already been ruled out.
func hasTimeComparison(t SearchTerm) bool {
	switch t.Op {
	case "gt", "gte", "lt", "lte":
		if isSearchTimeProp(t.Prop) {
			return true
		}
	}
	for _, c := range t.Terms {
		if hasTimeComparison(c) {
			return true
		}
	}
	return false
}
