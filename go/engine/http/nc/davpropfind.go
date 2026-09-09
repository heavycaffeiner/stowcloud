//go:build linux && compat_nc

package nc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// PROPFIND and PROPPATCH against the files tree and the virtual root.
//
// A PROPFIND response starts with the resource itself, because one client
// treats the first response in a multistatus as the folder being listed and
// reads every property from it before it ever looks at a child. Everything
// below that answers depth 1, never more: this compat layer's discovery
// contract only ever sees a client send 0 or 1, and the one value RFC 4918
// defaults an absent header to, infinity, is clamped rather than attempted.
// A recursive walk of an arbitrarily large tree inside one response is a
// denial of service this server can mount against itself.

// davDepthOneCeiling bounds how many children one level of a PROPFIND may
// enumerate, self included. The depth clamp already stops an infinite walk;
// this is insurance against a single directory, or a caller's whole account
// at the root, large enough that streaming every child through one response
// is itself the outage.
const davDepthOneCeiling = limits.DavInfinityEntries

// clampedDepth reads the Depth header and folds infinity down to one level.
func clampedDepth(r *http.Request) int {
	depth, infinite := depthOf(r)
	if infinite {
		return 1
	}
	return depth
}

// davPropfind answers PROPFIND against a resource inside the files tree.
func (s *Server) davPropfind(w http.ResponseWriter, r *http.Request, p Principal, t Target) {
	ctx := r.Context()

	query, err := ParsePropfind(r.Body)
	if err != nil {
		s.failDav(w, r, err, apierr.VisibilityKnown)
		return
	}

	res, err := s.resolveComponents(ctx, p, t.Path, acl.Read)
	if err != nil {
		s.failDav(w, r, err, apierr.VisibilityHidden)
		return
	}
	self, err := s.deps.Core.Stat(ctx, res)
	if err != nil {
		s.failDav(w, r, err, apierr.VisibilityHidden)
		return
	}

	depth := clampedDepth(r)
	ownerID, ownerName := s.ownerNames(ctx, p)
	favSet := s.favoriteSet(ctx, p, query)
	var quota *QuotaProps
	if query.Asked(PropQuotaUsed()) || query.Asked(PropQuotaAvailable()) {
		quota = s.accountQuota(ctx, p)
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)

	m := NewMulti(w)
	m.Open()

	selfHref := t.Href(t.Path, self.IsDir)
	found, missing := s.renderFileEntry(ctx, query, self, res.Perms(), false, favSet, ownerID, ownerName, quota)
	m.Response(selfHref, found, missing)

	if depth > 0 && self.IsDir {
		if werr := s.walkChildren(ctx, m, query, res, t, favSet, ownerID, ownerName, quota); werr != nil {
			s.log.Warn("a propfind listing stopped early", "error", werr)
		}
	}

	if cerr := m.Close(); cerr != nil {
		s.log.Warn("a propfind response was not delivered", "error", cerr)
	}
}

// walkChildren writes one level of a collection's members.
func (s *Server) walkChildren(
	ctx context.Context, m *Multi, query PropQuery, res core.Resolved, t Target,
	favSet FavoriteSet, ownerID, ownerName string, quota *QuotaProps,
) error {
	var cur core.Cursor
	written := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := s.deps.Core.List(ctx, res, cur)
		if err != nil {
			return err
		}
		for _, e := range page.Entries {
			if written >= davDepthOneCeiling {
				return nil
			}
			childPath, jerr := res.Path().JoinExisting(e.Name)
			if jerr != nil {
				continue
			}
			child, rerr := s.deps.Core.ResolveUnder(res, childPath, acl.Read)
			if rerr != nil {
				// A member this caller may not reach: left out rather than
				// reported, since a 403 inside the listing would confirm
				// it exists.
				continue
			}
			href := t.Href(append(t.Path[:len(t.Path):len(t.Path)], e.Name), e.IsDir)
			found, missing := s.renderFileEntry(ctx, query, e, child.Perms(), false, favSet, ownerID, ownerName, quota)
			m.Response(href, found, missing)
			written++
		}
		if page.Next == "" {
			return nil
		}
		cur = page.Next
	}
}

// davRootPropfind answers PROPFIND on the virtual root: one entry per share
// the caller may reach, with nothing on disk behind the root itself.
func (s *Server) davRootPropfind(w http.ResponseWriter, r *http.Request, p Principal, t Target) {
	ctx := r.Context()

	query, err := ParsePropfind(r.Body)
	if err != nil {
		s.failDav(w, r, err, apierr.VisibilityKnown)
		return
	}

	depth := clampedDepth(r)
	ownerID, ownerName := s.ownerNames(ctx, p)
	favSet := s.favoriteSet(ctx, p, query)
	var quota *QuotaProps
	if query.Asked(PropQuotaUsed()) || query.Asked(PropQuotaAvailable()) {
		quota = s.accountQuota(ctx, p)
	}

	roots := s.roots(ctx, p)

	// Every share this caller may reach is resolved once here, so the self
	// entry's etag and permission union and the children below are built from
	// the same set.
	//
	// A share that will not resolve stays in the listing, marked unreachable.
	// Dropping it would be the one answer that loses data: a folder missing
	// from an otherwise successful parent listing means "deleted on the
	// server" to a sync client, so a locked container or an unmounted disk
	// would have it delete the person's local copy. Present but unreadable is
	// an error the client reports and defers on, which is what a transient
	// outage should look like.
	children := make([]rootChild, 0, len(roots))
	union := acl.Perms(0)
	for _, rt := range roots {
		res, rerr := s.resolve(ctx, p, rt.Label, acl.Read)
		if rerr != nil {
			s.log.Warn("a share is listed but cannot be read",
				"share", rt.Label, "reason", rt.BrokenReason, "error", rerr)
			children = append(children, unreachableRoot(rt.Label))
			continue
		}
		entry, serr := s.deps.Core.Stat(ctx, res)
		if serr != nil {
			s.log.Warn("a share is listed but cannot be described",
				"share", rt.Label, "error", serr)
			children = append(children, unreachableRoot(rt.Label))
			continue
		}
		children = append(children, rootChild{label: rt.Label, res: res, entry: entry})
		union |= res.Perms()
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)

	m := NewMulti(w)
	m.Open()

	selfEntry := core.Entry{
		IsDir:   true,
		MTimeNs: s.clk.Now().UnixNano(),
		ETag:    rootEtagOf(children, s.instanceTag()),
	}
	selfHref := t.Href(nil, true)
	found, missing := s.renderFileEntry(ctx, query, selfEntry, union, false, nil, ownerID, ownerName, quota)
	m.Response(selfHref, found, missing)

	if depth > 0 {
		written := 0
		for _, c := range children {
			if written >= davDepthOneCeiling {
				break
			}
			href := t.Href([]string{c.label}, true)
			if c.unreachable {
				// Reported as a collection that cannot be read: no identity,
				// no validator, read-only letters. A sync client marks an
				// entry it cannot fully describe as an error and leaves the
				// local copy alone, which is the deferral this case wants.
				m.Response(href, []Prop{
					TextProp(PropDisplayName(), c.label),
					{Name: PropResourceType(), Children: []Node{{Name: dav("collection")}}},
					TextProp(PropPermissions(), PermString(acl.Read, true, false, true)),
					TextProp(PropMountType(), "external"),
				}, nil)
				written++
				continue
			}
			found, missing := s.renderFileEntry(
				ctx, query, c.entry, c.res.Perms(), true, favSet, ownerID, ownerName, quota,
			)
			m.Response(href, found, missing)
			written++
		}
	}

	if cerr := m.Close(); cerr != nil {
		s.log.Warn("a root propfind response was not delivered", "error", cerr)
	}
}

// rootChild is one share the caller may reach, already resolved: the etag
// derivation and the child rendering below both need the same resolution,
// so it is computed once per share rather than twice.
type rootChild struct {
	label string
	res   core.Resolved
	entry core.Entry
	// unreachable marks a share the account holds that this request could not
	// resolve or stat. It is listed anyway; see the loop that builds this set.
	unreachable bool
}

// unreachableRoot is one share that is known and currently unreadable.
func unreachableRoot(label string) rootChild {
	return rootChild{label: label, unreachable: true}
}

// rootEtagOf derives the virtual root's own etag from its children's,
// never empty: a root with no reachable share still needs a stable
// validator, so the instance tag stands in for an empty child list.
func rootEtagOf(children []rootChild, instance string) string {
	// Assembled first and hashed once, so there is no write to fail: the
	// separators keep two different child lists from hashing alike.
	input := []byte(instance)
	for _, c := range children {
		input = append(input, 0)
		input = append(input, c.label...)
		input = append(input, 0)
		input = append(input, c.entry.ETag...)
	}
	sum := sha256.Sum256(input)
	return hex.EncodeToString(sum[:])[:32]
}

// davProppatch applies a PROPPATCH body's set and remove instructions.
//
// oc:favorite is the one property this surface stores. Everything else a
// client sends is answered 403: the property is understood well enough to
// name, but nothing here keeps a value for it.
func (s *Server) davProppatch(w http.ResponseWriter, r *http.Request, p Principal, t Target) {
	if !s.deps.Features().Favorites {
		WriteDAVError(w, http.StatusForbidden,
			"Sabre\\DAV\\Exception\\Forbidden", "This server does not store properties")
		return
	}

	ctx := r.Context()
	ops, err := ParseProppatch(r.Body)
	if err != nil {
		s.failDav(w, r, err, apierr.VisibilityKnown)
		return
	}

	res, err := s.resolveComponents(ctx, p, t.Path, acl.Read)
	if err != nil {
		s.failDav(w, r, err, apierr.VisibilityHidden)
		return
	}
	entry, err := s.deps.Core.Stat(ctx, res)
	if err != nil {
		s.failDav(w, r, err, apierr.VisibilityHidden)
		return
	}

	var applied, refused []PropName
	for _, op := range ops {
		if !op.Name.Equal(PropFavorite()) {
			refused = append(refused, op.Name)
			continue
		}
		on := !op.Remove && op.Value == "1"
		if serr := s.deps.Store.SetFavorite(ctx, user(p), entry, on); serr != nil {
			s.log.Warn("a favourite could not be stored", "error", serr)
			refused = append(refused, op.Name)
			continue
		}
		applied = append(applied, op.Name)
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)

	m := NewMulti(w)
	m.Open()
	writePropPatchResponse(m, t.Href(t.Path, entry.IsDir), applied, refused)
	if cerr := m.Close(); cerr != nil {
		s.log.Warn("a proppatch response was not delivered", "error", cerr)
	}
}

// writePropPatchResponse writes one response element carrying a 200 group
// for the properties this surface stored and a 403 group for the rest.
//
// Built from Multi's own element writer rather than a second XML path: both
// files live in this package, and a PROPPATCH response is not the
// found/missing shape Multi.Response already carries.
func writePropPatchResponse(m *Multi, href string, applied, refused []PropName) {
	m.write("<d:response><d:href>")
	m.escape(href)
	m.write("</d:href>")
	if len(applied) > 0 {
		m.write("<d:propstat><d:prop>")
		for _, n := range applied {
			m.empty(n)
		}
		m.write("</d:prop><d:status>" + statusLine(http.StatusOK) + "</d:status></d:propstat>")
	}
	if len(refused) > 0 {
		m.write("<d:propstat><d:prop>")
		for _, n := range refused {
			m.empty(n)
		}
		m.write("</d:prop><d:status>" + statusLine(http.StatusForbidden) + "</d:status></d:propstat>")
	}
	m.write("</d:response>")
}

// renderFileEntry answers one entry's properties, filling in the favourite
// flag and the account-wide facts every entry in a response shares.
func (s *Server) renderFileEntry(
	ctx context.Context, query PropQuery, e core.Entry, perms acl.Perms, shareRoot bool,
	favSet FavoriteSet, ownerID, ownerName string, quota *QuotaProps,
) ([]Prop, []PropName) {
	fav := false
	if favSet != nil {
		fav = favSet.Has(e)
	}
	in := EntryProps{
		Query:     query,
		Entry:     e,
		Perms:     perms,
		ShareRoot: shareRoot,
		Favorite:  fav,
		OwnerID:   ownerID,
		OwnerName: ownerName,
	}
	if e.IsDir {
		in.Quota = quota
	}
	return s.entryProps(ctx, in)
}

// ownerNames answers the identity every entry in a response carries: the
// caller's own login name and display name. This engine has no per-file
// ownership record beyond the account a session belongs to, so every entry
// the caller can reach is rendered as theirs.
func (s *Server) ownerNames(ctx context.Context, p Principal) (id, name string) {
	id = s.loginNameOf(ctx, p)
	name = id
	if info, err := s.deps.Auth.AccountInfo(ctx, p.UserID); err == nil && info.DisplayName != "" {
		name = info.DisplayName
	}
	return id, name
}

// favoriteSet reads the caller's starred set once for a whole response, and
// only when the query could use it: a client that did not ask for
// oc:favorite gets no store round trip on its account.
func (s *Server) favoriteSet(ctx context.Context, p Principal, query PropQuery) FavoriteSet {
	if !query.Asked(PropFavorite()) {
		return nil
	}
	set, err := s.deps.Store.Favorites(ctx, user(p))
	if err != nil {
		return nil
	}
	return set
}

// accountQuota reads the account's quota once for a whole response.
// Available is negative for an account with no configured limit, which
// entryProps renders as the sentinel a client reads as unlimited; a real
// account crossed the limit that instant is clamped to zero rather than
// read the same way.
func (s *Server) accountQuota(ctx context.Context, p Principal) *QuotaProps {
	info, err := s.deps.Auth.AccountInfo(ctx, p.UserID)
	if err != nil {
		return nil
	}
	q := &QuotaProps{Used: info.UsageBytes}
	if info.QuotaBytes == nil {
		q.Available = -1
		return q
	}
	used, uerr := num.Narrow[int64](info.UsageBytes)
	if uerr != nil {
		// A usage figure too large to compare against a cap is reported as no
		// cap at all rather than as a negative allowance, which a client
		// renders as a full account.
		q.Available = -1
		return q
	}
	avail := *info.QuotaBytes - used
	if avail < 0 {
		avail = 0
	}
	q.Available = avail
	return q
}
