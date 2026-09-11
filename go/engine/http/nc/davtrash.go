//go:build linux && compat_nc

package nc

import (
	"context"
	"hash/fnv"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// The trash tree: PROPFIND lists what a person can still get back, DELETE
// purges (one entry or everything), and MOVE into /restore puts an entry
// back where it was deleted from.
//
// This engine's trash is per share; the client addresses one flat
// collection with no share segment in it at all. The listing is therefore
// the union across every share the caller can reach, and each member's own
// name has to carry enough to find that entry again on a later restore or
// purge: which share it came from, and the store's own id within that
// share's trash directory.
//
// The encoding is "<share>-<id>": share is the decimal ShareID and id is the
// entry's own name, which core.trash mints as lowercase hex and guarantees
// never contains a dash (trash.go's splitTrashName relies on the same fact
// to cut on the first one). Decimal digits followed by a dash followed by a
// hex string has no slash, needs no percent-decoding to split, and
// round-trips through a single URL path component untouched.
func trashMemberName(share core.ShareID, id string) string {
	return strconv.FormatUint(uint64(share), 10) + "-" + id
}

// parseTrashMember reads a member name back into the share and the entry id
// TrashRestore/TrashPurge take. The share half is decimal digits, so cutting
// on the first dash is unambiguous: a decimal share id can never contain a
// dash either, so the first dash is always the boundary between the two
// halves, whatever the hex id after it looks like.
func parseTrashMember(name string) (core.ShareID, string, bool) {
	share, id, ok := strings.Cut(name, "-")
	if !ok || share == "" || id == "" {
		return 0, "", false
	}
	n, err := strconv.ParseUint(share, 10, 32)
	if err != nil {
		return 0, "", false
	}
	return core.ShareID(n), id, true
}

// davTrash serves the trash tree. Features().Trash false means this
// deployment answers every path here as absent: a client shown a trash
// screen it cannot then act on reports the whole account as broken, so the
// screen itself must never appear.
func (s *Server) davTrash(w http.ResponseWriter, r *http.Request, p Principal, t Target) {
	if !s.deps.Features().Trash {
		WriteDAVError(w, http.StatusNotFound,
			"Sabre\\DAV\\Exception\\NotFound", "File not found")
		return
	}

	switch t.Trash {
	case TrashList:
		s.trashDispatchList(w, r, p, t)
	default:
		// TrashRestore names /restore itself, and TrashNone the bare mount
		// with no half at all. Neither client ever issues a request whose
		// own path is one of these: /restore is reached only as a MOVE
		// destination, parsed separately from the header rather than from
		// the request's own path.
		s.davMethodNotAllowed(w)
	}
}

// trashDispatchList answers the /trash half: PROPFIND lists, DELETE purges,
// GET streams a member's bytes where this engine can address them, and MOVE
// restores a member (the request's own path is the source; the destination
// is read from the header inside trashRestore).
func (s *Server) trashDispatchList(w http.ResponseWriter, r *http.Request, p Principal, t Target) {
	switch r.Method {
	case "PROPFIND":
		if !t.IsRoot() {
			// Only the collection is listed. Neither client PROPFINDs a
			// single trash member directly.
			s.davMethodNotAllowed(w)
			return
		}
		s.trashPropfind(w, r, p, t)
	case http.MethodDelete:
		s.trashDelete(w, r, p, t)
	case http.MethodGet:
		if t.IsRoot() {
			s.davMethodNotAllowed(w)
			return
		}
		s.trashGet(w, r, p, t)
	case "MOVE":
		if t.IsRoot() {
			s.davMethodNotAllowed(w)
			return
		}
		s.trashRestore(w, r, p, t)
	default:
		s.davMethodNotAllowed(w)
	}
}

// trashShareRoot is one share already resolved for a trash operation.
type trashShareRoot struct {
	share core.ShareID
	label string
	res   core.Resolved
}

// trashRoots resolves every share the caller may read trash on.
//
// A share that fails to resolve is skipped rather than reported: a flat
// listing across every share the caller can reach has no per-share slot in
// one multistatus to carry a partial failure, and s.roots already excluded
// anything the caller cannot see at all.
func (s *Server) trashRoots(ctx context.Context, p Principal) []trashShareRoot {
	roots := s.roots(ctx, p)
	out := make([]trashShareRoot, 0, len(roots))
	for _, rt := range roots {
		res, err := s.resolve(ctx, p, rt.Label, acl.Read)
		if err != nil {
			continue
		}
		id, ok := shareIDOf(rt.Share)
		if !ok {
			continue
		}
		out = append(out, trashShareRoot{share: id, label: rt.Label, res: res})
	}
	return out
}

// trashPropfind answers PROPFIND on /trash: the collection itself, then one
// response per trashed entry across every share the caller can reach.
func (s *Server) trashPropfind(w http.ResponseWriter, r *http.Request, p Principal, t Target) {
	ctx := r.Context()

	query, err := ParsePropfind(r.Body)
	if err != nil {
		s.failDav(w, r, err, apierr.VisibilityKnown)
		return
	}
	depth := clampedDepth(r)

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)

	m := NewMulti(w)
	m.Open()

	selfHref := t.Href(nil, true)
	selfProps, selfMissing := s.entryProps(ctx, EntryProps{
		Query: query,
		Entry: core.Entry{IsDir: true, MTimeNs: s.clk.Now().UnixNano()},
	})
	m.Response(selfHref, selfProps, selfMissing)

	if depth > 0 {
		written := 0
	rootLoop:
		for _, root := range s.trashRoots(ctx, p) {
			entries, lerr := s.deps.Core.TrashList(ctx, root.res)
			if lerr != nil {
				continue
			}
			for _, e := range entries {
				if written >= davDepthOneCeiling {
					break rootLoop
				}
				name := trashMemberName(root.share, e.ID)
				href := t.Href([]string{name}, e.IsDir)
				found, missing := s.trashEntryProps(ctx, p, query, root, e)
				m.Response(href, found, missing)
				written++
			}
		}
	}

	if cerr := m.Close(); cerr != nil {
		s.log.Warn("a trash listing was not delivered", "error", cerr)
	}
}

// trashEntryProps renders one trashed entry's properties.
//
// A trashed entry carries no live inode to key an identity or a validator
// on: it has been moved into a control directory rather than deleted, and
// TrashEntry (core/trash.go) reports only {ID, Name, OrigPath, IsDir, Size,
// DeletedAtNs}, none of which is a dev/ino pair. s.fileID and FileETag both
// need one, so neither is used here; the identity and etag this file mints
// instead derive from the pair that actually names the entry stably for as
// long as it survives: the share it sits in and the store's own random
// trash id (see trashEntrySyntheticID/trashEntryToken). A client only ever
// addresses a trash entry through this collection's own hrefs, never by
// resolving a fileid back to a path, so a synthetic value that happens to
// coincide with a live file's numeric id elsewhere in the tree is harmless:
// nothing compares the two namespaces.
func (s *Server) trashEntryProps(
	ctx context.Context, p Principal, query PropQuery, root trashShareRoot, e core.TrashEntry,
) ([]Prop, []PropName) {
	origLocation := e.OrigPath
	if s.deps.VpathOf != nil && e.OrigPath != "" {
		if vp, err := s.deps.VpathOf(user(p), root.share, e.OrigPath); err == nil {
			origLocation = vp
		}
		// An error leaves origLocation as the raw share-relative path
		// rather than dropping the property: the client shows this as the
		// restore target, and the share-relative spelling is still an
		// honest answer, unlike an empty value that reads as unknown.
	}

	entry := core.Entry{
		Name:    e.Name,
		IsDir:   e.IsDir,
		Size:    e.Size,
		MTimeNs: e.DeletedAtNs,
		ETag:    trashEntryToken(root.share, e.ID),
	}

	in := EntryProps{
		Query: query,
		Entry: entry,
		Perms: root.res.Perms(),
		Trash: &TrashProps{
			FileName:         e.Name,
			OriginalLocation: origLocation,
			DeletedAtNs:      e.DeletedAtNs,
		},
	}
	found, missing := s.entryProps(ctx, in)
	found, missing = trashOverrideIdentity(found, missing, query, root.share, e.ID)
	return trashOverrideSize(found, missing, query, e.Size)
}

// trashOverrideIdentity replaces whatever entryProps decided for oc:id and
// oc:fileid with the synthetic identity this file mints. entryProps derives
// both from a zero Ident on the bare core.Entry built above (a trash entry
// has none), so it reports them missing; this restores them to the stable
// value the trash screen needs to key its own state on.
func trashOverrideIdentity(
	found []Prop, missing []PropName, query PropQuery, share core.ShareID, id string,
) ([]Prop, []PropName) {
	if !query.Asked(PropID()) && !query.Asked(PropFileID()) {
		return found, missing
	}
	fid := trashEntrySyntheticID(share, id)
	found = removeProps(found, PropID(), PropFileID())
	missing = removePropNames(missing, PropID(), PropFileID())
	if query.NamesOnly {
		if query.Asked(PropID()) {
			found = append(found, Prop{Name: PropID()})
		}
		if query.Asked(PropFileID()) {
			found = append(found, Prop{Name: PropFileID()})
		}
		return found, missing
	}
	if query.Asked(PropID()) {
		found = append(found, TextProp(PropID(), strconv.FormatUint(fid, 10)))
	}
	if query.Asked(PropFileID()) {
		found = append(found, TextProp(PropFileID(), strconv.FormatUint(fid, 10)))
	}
	return found, missing
}

// trashOverrideSize corrects oc:size for a trashed directory.
//
// entryProps' own PropSize() handler calls s.entrySize, which for a
// directory calls Core.Aggregate keyed on the entry's Ident.Share: the bare
// core.Entry built above carries a zero Ident, since a trash entry has no
// live inode to key one on (see trashEntryProps), so Aggregate resolves
// nothing and the property silently renders zero. TrashList already
// measured the directory's total when it built the listing, and that is
// the figure restored here. A file's own oc:size took the fast path inside
// entrySize (a non-directory answers e.Size directly) and needs no
// correction.
func trashOverrideSize(found []Prop, missing []PropName, query PropQuery, size uint64) ([]Prop, []PropName) {
	if !query.Asked(PropSize()) || query.NamesOnly {
		return found, missing
	}
	found = removeProps(found, PropSize())
	missing = removePropNames(missing, PropSize())
	found = append(found, TextProp(PropSize(), strconv.FormatUint(size, 10)))
	return found, missing
}

// removeProps drops any property carrying one of the given names.
func removeProps(in []Prop, names ...PropName) []Prop {
	out := in[:0]
	for _, p := range in {
		keep := true
		for _, n := range names {
			if p.Name.Equal(n) {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, p)
		}
	}
	return out
}

// removePropNames drops any name matching one of the given names.
func removePropNames(in []PropName, names ...PropName) []PropName {
	out := in[:0]
	for _, p := range in {
		keep := true
		for _, n := range names {
			if p.Equal(n) {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, p)
		}
	}
	return out
}

// trashEntrySyntheticID mints the numeric identity a trash entry's oc:id and
// oc:fileid render, from the pair that names it stably: the share and the
// store's own random trash id. See trashEntryProps for why this cannot be
// the live-file identity path, and why a value that happens to coincide
// with a real file's fileid elsewhere in the tree is never confused with
// it: nothing reaches a trash entry by fileid, only by this collection's
// own href.
func trashEntrySyntheticID(share core.ShareID, id string) uint64 {
	// Assembled then hashed once, so no write can fail: the separator keeps
	// two different pairs from hashing alike.
	input := append([]byte(strconv.FormatUint(uint64(share), 10)), 0)
	input = append(input, id...)
	h := fnv.New64a()
	h.Write(input) //nolint:errcheck,gosec // hash.Hash.Write cannot fail.
	return h.Sum64()
}

// trashEntryToken mints the validator a trash entry's d:getetag renders,
// from the same pair as its identity. A trashed entry's bytes never change
// while it sits in the trash, so a value derived from what names it rather
// than from its content is stable for exactly as long as the entry exists,
// which is the whole of what a validator here has to promise.
func trashEntryToken(share core.ShareID, id string) string {
	return strconv.FormatUint(trashEntrySyntheticID(share, id+"\x00etag"), 16)
}

// trashDelete answers DELETE. A member name purges that one entry; the bare
// collection purges every entry across every share the caller can reach.
// One client accepts only an exact 204 for either.
func (s *Server) trashDelete(w http.ResponseWriter, r *http.Request, p Principal, t Target) {
	ctx := r.Context()

	if t.IsRoot() {
		roots := s.roots(ctx, p)
		purged := 0
		for _, rt := range roots {
			res, err := s.resolve(ctx, p, rt.Label, acl.Delete)
			if err != nil {
				continue
			}
			purged++
			if perr := s.deps.Core.TrashPurge(ctx, res, nil); perr != nil {
				// A share with trash off, or one with nothing to purge,
				// answers an error here that names nothing wrong with the
				// request: emptying the whole trash is not refused because
				// one of many shares had nothing in it or cannot trash at
				// all.
				s.log.Warn("purging one share's trash failed during an empty-everything request",
					"error", perr)
			}
		}
		// Nowhere the caller may delete, while there is somewhere they can
		// see: the credential is read-only or scoped away from every share.
		// Answering 204 there would report an emptied trash to a client that
		// emptied nothing, and the next listing would show every entry back.
		if purged == 0 && len(roots) > 0 {
			s.failDav(w, r, core.ErrDenied, apierr.VisibilityKnown)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	share, id, ok := parseTrashMember(t.Path[0])
	if !ok {
		s.failDav(w, r, ErrBadPath, apierr.VisibilityHidden)
		return
	}
	res, rerr := s.trashShareResolve(ctx, p, share, acl.Delete)
	if rerr != nil {
		s.failDav(w, r, rerr, apierr.VisibilityHidden)
		return
	}
	if perr := s.deps.Core.TrashPurge(ctx, res, &id); perr != nil {
		s.failDav(w, r, perr, apierr.VisibilityHidden)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// trashRestore answers MOVE from a trash member to /restore/<name>. A
// destination anywhere else is refused: the trash collection is the only
// source this handler serves a MOVE from, and /restore is the only legal
// target for one.
func (s *Server) trashRestore(w http.ResponseWriter, r *http.Request, p Principal, t Target) {
	ctx := r.Context()

	share, id, ok := parseTrashMember(t.Path[0])
	if !ok {
		s.failDav(w, r, ErrBadPath, apierr.VisibilityHidden)
		return
	}
	if _, ok := parseRestoreDestination(r); !ok {
		WriteDAVError(w, http.StatusBadRequest,
			"Sabre\\DAV\\Exception\\BadRequest", "The destination must be under /restore")
		return
	}

	res, rerr := s.trashShareResolve(ctx, p, share, acl.Create)
	if rerr != nil {
		s.failDav(w, r, rerr, apierr.VisibilityHidden)
		return
	}
	if _, err := s.deps.Core.TrashRestore(ctx, res, id); err != nil {
		s.failDav(w, r, err, apierr.VisibilityHidden)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// parseRestoreDestination reads a restore MOVE's Destination header.
//
// A separate parser from the files tree's parseDestination, which refuses
// anything that does not resolve to KindFiles: the trash tree's own
// restore target resolves to KindTrash with the restore half set, a shape
// that parser has no way to accept. The host check is the same rule
// applied everywhere else on this surface: a Destination naming another
// host names a resource this server cannot act on.
func parseRestoreDestination(r *http.Request) (Target, bool) {
	raw := strings.TrimSpace(r.Header.Get("Destination"))
	if raw == "" {
		return Target{}, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil {
		return Target{}, false
	}
	if u.Host != "" {
		destinationHost := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
		requestHost := authorityHostname(r.Host)
		if destinationHost == "" || requestHost == "" || destinationHost != requestHost {
			return Target{}, false
		}
	}
	dest, ok := ParseTarget(collapseSlashes(u.EscapedPath()))
	if !ok || dest.Kind != KindTrash || dest.Trash != TrashRestore {
		return Target{}, false
	}
	return dest, true
}

// trashGet streams a trashed file's bytes.
//
// This engine keeps a trashed entry as an ordinary file moved into a
// control directory, but nothing in core exposes a read path over that
// control location to a protocol layer: TrashList reports size and kind
// only, and OpenStream/OpenRandom both take a Resolved built from a live
// client path, which a trash entry no longer has one of. A body cannot be
// produced without a read path this package has no access to, so this
// answers honestly rather than guessing at one.
func (s *Server) trashGet(w http.ResponseWriter, r *http.Request, p Principal, t Target) {
	ctx := r.Context()
	share, id, ok := parseTrashMember(t.Path[0])
	if !ok {
		s.failDav(w, r, ErrBadPath, apierr.VisibilityHidden)
		return
	}
	res, rerr := s.trashShareResolve(ctx, p, share, acl.Read|acl.Download)
	if rerr != nil {
		s.failDav(w, r, rerr, apierr.VisibilityHidden)
		return
	}
	entries, lerr := s.deps.Core.TrashList(ctx, res)
	if lerr != nil {
		s.failDav(w, r, lerr, apierr.VisibilityHidden)
		return
	}
	found := false
	for _, e := range entries {
		if e.ID == id {
			found = true
			break
		}
	}
	if !found {
		s.failDav(w, r, core.ErrNotFound, apierr.VisibilityHidden)
		return
	}
	WriteDAVError(w, http.StatusNotImplemented,
		"Sabre\\DAV\\Exception\\NotImplemented", "Reading a trashed file's contents is not available")
}

// trashShareResolve resolves one share by id for an operation targeting a
// specific member, applying the caller's own mask and share allowlist
// exactly as any other path on this surface does.
func (s *Server) trashShareResolve(
	ctx context.Context, p Principal, share core.ShareID, need acl.Perms,
) (core.Resolved, error) {
	for _, rt := range s.roots(ctx, p) {
		id, ok := shareIDOf(rt.Share)
		if !ok || id != share {
			continue
		}
		return s.resolve(ctx, p, rt.Label, need)
	}
	return core.Resolved{}, core.ErrNotFound
}

// shareIDOf narrows a store row's share id, which is wider than a share id
// because the column is a generic foreign key. A row too wide to be one is
// corrupt, and the caller treats it as a share it cannot reach.
func shareIDOf(v int64) (core.ShareID, bool) {
	narrowed, err := num.Narrow[uint32](v)
	if err != nil {
		return 0, false
	}
	return core.ShareID(narrowed), true
}
