//go:build linux && compat_nc

package nc

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// The share API. Two engine concepts answer for it: a public link
// (core.Link, share type 3) and an acl grant naming an account or a group
// (share types 0 and 1, through core.CreateGrant and its siblings). Both
// render into the one wire shape the clients parse, and this file is where
// the two get reconciled.
//
// Wire id scheme: a link and a grant both number their rows from 1 in their
// own tables, so a bare id is ambiguous between them. The wire id encodes
// which kind it names by a fixed offset: a link's wire id is its own row
// id, unchanged; a grant's wire id is its row id plus grantIDOffset.
// getShare, updateShare and deleteShare invert the offset to learn which
// store to consult.
//
// The offset has to leave the whole range inside a signed 32-bit integer:
// one client reads this id with a 32-bit parse, and a single id past that
// ceiling makes it discard the entire listing it arrived in, not just the
// one entry. So links get the first billion ids and grants the second, and
// a deployment reaching either is far past what a share table holds.
//
// A grant carries no creator column: this engine's grant table records who
// it applies to (User or Group) and what it allows, never who wrote it. So
// "the caller administers this grant" is answered the way CreateGrant
// itself decides whether to allow one: by re-resolving the grant's own
// target through the caller's principal, exactly the path s.resolve uses
// everywhere else in this package, and asking whether Share still holds
// there. That is also what keeps the answer honest when access changes
// after the grant was made, the same way UpdateLink re-checks a link's
// creator before letting an update widen it, and what keeps a device
// credential's mask and share allowlist in force for this decision the
// same as for every other one in this package.

// grantIDOffset separates the grant wire-id range from the link range,
// with both halves inside a signed 32-bit integer.
const grantIDOffset = int64(1) << 30

func wireLinkID(id int64) int64  { return id }
func wireGrantID(id int64) int64 { return id + grantIDOffset }

// parseShareID reads a wire id back into the store id and which kind it
// names.
func parseShareID(raw string) (id int64, isGrant bool, ok bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || n <= 0 {
		return 0, false, false
	}
	if n >= grantIDOffset {
		return n - grantIDOffset, true, true
	}
	return n, false, true
}

// listShares answers GET .../shares.
//
// Without a path: every share the caller owns, links plus grants they
// administer. With a path: only the shares on that exact resource; with
// subfiles=true, the shares on the entries directly below it instead.
// shared_with_me lists the grants pointing at the caller rather than the
// ones they administer. reshares and include_tags are accepted and not
// otherwise consulted: this engine's "administers" answer already covers
// every share the caller could have made regardless of who first made it,
// and no tag storage beyond favourites exists for this listing to draw on.
//
// An empty result is an empty list, never a refusal: a share listing is a
// routine question, and answering "forbidden" for "nothing published here
// yet" would show every unshared file as an error.
func (s *Server) listShares(c *fiber.Ctx, p Principal) (Val, bool, *Error) {
	ctx := c.UserContext()
	owner := user(p)

	if queryBool(c.Query("shared_with_me")) {
		return List(s.grantsSharedWith(ctx, owner)...), true, nil
	}

	// The path a client sends is its own spelling, and a folder always carries
	// a trailing separator there. Resolution splits on that separator, so the
	// spelling arrived as a path with an empty last component and answered as
	// absent: the sharing panel of every folder reported that it could not
	// read the folder's shares, and the share screen it fronts never opened.
	path := strings.Trim(c.Query("path"), "/")
	if path == "" {
		// The account root, addressed either as nothing or as the separator
		// alone. It holds no shares of its own: what it holds is the shares
		// themselves, so with subfiles asked for the answer is theirs, and
		// without it the answer is everything this caller administers, which
		// is what a client asking about the root is looking for.
		if queryBool(c.Query("subfiles")) {
			return List(s.sharesUnderRoot(c, p)...), true, nil
		}
		items := make([]Val, 0)
		links, err := s.deps.Core.ListLinks(ctx, owner, nil)
		if err != nil {
			return Val{}, false, ocsErrorOf(err, apierr.VisibilityHidden)
		}
		for _, l := range links {
			items = append(items, s.linkShareVal(c, l))
		}
		items = append(items, s.grantsAdministeredBy(c, p)...)
		return List(items...), true, nil
	}

	res, err := s.resolve(ctx, p, path, acl.Read)
	if err != nil {
		return Val{}, false, ocsErrorOf(err, apierr.VisibilityHidden)
	}

	if !queryBool(c.Query("subfiles")) {
		return List(s.sharesAtResolved(c, res)...), true, nil
	}

	page, err := s.deps.Core.List(ctx, res, "")
	if err != nil {
		return Val{}, false, ocsErrorOf(err, apierr.VisibilityHidden)
	}
	items := make([]Val, 0)
	for _, e := range page.Entries {
		childPath, perr := e.Path.Safe()
		if perr != nil {
			continue
		}
		child, cerr := s.deps.Core.ResolveUnder(res, childPath, acl.Read)
		if cerr != nil {
			continue
		}
		items = append(items, s.sharesAtResolved(c, child)...)
	}
	return List(items...), true, nil
}

// sharesUnderRoot answers the shares that sit on the account root's own
// children, which are the shares this caller may reach. A client asking about
// the root with subfiles is asking which of the folders it can see are
// shared.
func (s *Server) sharesUnderRoot(c *fiber.Ctx, p Principal) []Val {
	ctx := c.UserContext()
	items := make([]Val, 0)
	for _, rt := range s.roots(ctx, p) {
		res, err := s.resolve(ctx, p, rt.Label, acl.Read)
		if err != nil {
			continue
		}
		items = append(items, s.sharesAtResolved(c, res)...)
	}
	return items
}

// sharesAtResolved answers every link and grant the caller owns exactly at
// one path they have already resolved for reading.
func (s *Server) sharesAtResolved(c *fiber.Ctx, res core.Resolved) []Val {
	ctx := c.UserContext()
	owner := res.User()
	items := make([]Val, 0)
	if links, err := s.deps.Core.ListLinks(ctx, owner, &res); err == nil {
		for _, l := range links {
			items = append(items, s.linkShareVal(c, l))
		}
	}
	// The caller may only administer a grant here if they currently hold
	// Share on the exact resolution already in hand: no second resolve
	// needed, since res.Perms() is the full effective set at this path,
	// already narrowed by whatever mask the caller's credential carries.
	if !res.Perms().Has(acl.Share) {
		return items
	}
	grants, err := s.deps.Core.ListGrants(ctx, core.GrantFilter{Share: int64(res.Share())})
	if err != nil {
		return items
	}
	target := acl.NewPath(res.Path().Components()...).String()
	for _, g := range grants {
		if acl.ParsePath(g.Subpath).String() != target {
			continue
		}
		// A grant that names the caller is what gives them their own access,
		// not something they shared with somebody else. It belongs in the
		// shared-with-me listing, and reporting it here shows a person their
		// own account as a recipient of their own files.
		if s.grantNamesCaller(ctx, g, owner) {
			continue
		}
		if v, ok := s.grantShareValAt(ctx, owner, g, res); ok {
			items = append(items, v)
		}
	}
	return items
}

// grantsAdministeredBy answers every grant the caller currently holds
// Share permission over, whatever share it lands on. Global rather than
// path-scoped, so it costs one resolve per grant in the deployment; the
// listing it answers is the "my shares" screen a person opens deliberately,
// not a per-file check on a hot path.
func (s *Server) grantsAdministeredBy(c *fiber.Ctx, p Principal) []Val {
	ctx := c.UserContext()
	all, err := s.deps.Core.ListGrants(ctx, core.GrantFilter{})
	if err != nil {
		return nil
	}
	items := make([]Val, 0, len(all))
	for _, g := range all {
		if s.grantNamesCaller(ctx, g, user(p)) {
			continue
		}
		res, ok := s.grantAdminContext(ctx, p, g)
		if !ok {
			continue
		}
		if v, ok := s.grantShareValAt(ctx, user(p), g, res); ok {
			items = append(items, v)
		}
	}
	return items
}

// grantNamesCaller reports whether a grant hands access to the caller
// themselves, directly or through a group they belong to.
func (s *Server) grantNamesCaller(ctx context.Context, g core.Grant, caller core.UserID) bool {
	if g.User != nil && *g.User == int64(caller) {
		return true
	}
	if g.Group == nil {
		return false
	}
	ids, err := s.deps.Auth.GroupIDsOf(ctx, int64(caller))
	if err != nil {
		return false
	}
	for _, id := range ids {
		if id == *g.Group {
			return true
		}
	}
	return false
}

// grantsSharedWith answers every grant naming the caller, directly or
// through a group they belong to: what shared_with_me asks for.
func (s *Server) grantsSharedWith(ctx context.Context, caller core.UserID) []Val {
	all, err := s.deps.Core.ListGrants(ctx, core.GrantFilter{User: int64(caller)})
	if err != nil {
		return nil
	}
	groupIDs, gerr := s.deps.Auth.GroupIDsOf(ctx, int64(caller))
	if gerr == nil && len(groupIDs) > 0 {
		byGroup := make(map[int64]struct{}, len(groupIDs))
		for _, id := range groupIDs {
			byGroup[id] = struct{}{}
		}
		wider, werr := s.deps.Core.ListGrants(ctx, core.GrantFilter{})
		if werr == nil {
			for _, g := range wider {
				if g.Group == nil {
					continue
				}
				if _, in := byGroup[*g.Group]; in {
					all = append(all, g)
				}
			}
		}
	}
	items := make([]Val, 0, len(all))
	for _, g := range all {
		if v, ok := s.grantViewerVal(ctx, caller, g); ok {
			items = append(items, v)
		}
	}
	return items
}

// createShare answers POST .../shares.
func (s *Server) createShare(c *fiber.Ctx, p Principal) (Val, bool, *Error) {
	ctx := c.UserContext()
	// Trimmed for the same reason the listing trims it: a client spells a
	// folder with a trailing separator, and the path it shares is the path it
	// shows.
	path := strings.Trim(shareFormValue(c, "path"), "/")
	if path == "" {
		return Val{}, false, BadRequest("path is required")
	}
	shareType, err := strconv.Atoi(shareFormValue(c, "shareType"))
	if err != nil {
		return Val{}, false, BadRequest("shareType is required")
	}

	switch shareType {
	case ShareTypePublicLink:
		return s.createLinkShare(c, ctx, p, path)
	case ShareTypeUser, ShareTypeGroup:
		return s.createGrantShare(c, ctx, p, path, shareType)
	default:
		return Val{}, false, BadRequest("this share type is not supported")
	}
}

// createLinkShare mints a public link share.
func (s *Server) createLinkShare(c *fiber.Ctx, ctx context.Context, p Principal, path string) (Val, bool, *Error) {
	res, err := s.resolve(ctx, p, path, acl.Share)
	if err != nil {
		return Val{}, false, ocsErrorOf(err, apierr.VisibilityHidden)
	}

	features := s.deps.Features()

	perms := acl.Read | acl.Download
	if queryBool(shareFormValue(c, "publicUpload")) {
		perms |= acl.Create
	}
	if raw := shareFormValue(c, "permissions"); raw != "" {
		if n, perr := strconv.ParseInt(raw, 10, 64); perr == nil && n > 0 {
			perms = permsFromMask(n)
		}
	}

	var password *string
	if raw := shareFormValue(c, "password"); raw != "" {
		password = &raw
	} else if features.LinkPasswordEnforced {
		return Val{}, false, Forbidden("this deployment requires a password on every link")
	}

	var expires int64
	if raw := shareFormValue(c, "expireDate"); raw != "" {
		t, perr := parseShareDate(raw)
		if perr != nil {
			return Val{}, false, BadRequest("expireDate could not be parsed")
		}
		expires = t
	} else if features.LinkExpiryEnforced {
		return Val{}, false, Forbidden("this deployment requires an expiry date on every link")
	}
	if features.LinkExpiryEnforced && features.LinkExpiryDays > 0 && expires != 0 {
		maxAt := s.clk.Nanos() + int64(features.LinkExpiryDays)*int64(24*time.Hour)
		if expires > maxAt {
			return Val{}, false, Forbidden("the expiry date exceeds this deployment's maximum")
		}
	}

	link, _, cerr := s.deps.Core.CreateLink(ctx, res, core.LinkSpec{
		Perms:    perms,
		Password: password,
		Expires:  expires,
		MaxDown:  -1,
		Label:    shareFormValue(c, "label"),
		Note:     shareFormValue(c, "note"),
	})
	if cerr != nil {
		// The caller demonstrably reached this path through their own
		// resolve above, so a refusal here may say what it refused.
		return Val{}, false, ocsErrorOf(cerr, apierr.VisibilityKnown)
	}
	return s.linkShareVal(c, link), true, nil
}

// createGrantShare mints an account or group grant, share types 0 and 1.
//
// The default permission set, when the client sends none, is everything
// the caller themselves holds at the target: the ordinary "share what I
// can do" gesture. An explicit permissions mask narrows or widens from
// there, but never past what the caller holds, which is the same
// escalation guard CreateLink enforces for a link.
func (s *Server) createGrantShare(
	c *fiber.Ctx, ctx context.Context, p Principal, path string, shareType int,
) (Val, bool, *Error) {
	res, err := s.resolve(ctx, p, path, acl.Share)
	if err != nil {
		return Val{}, false, ocsErrorOf(err, apierr.VisibilityHidden)
	}

	shareWith := shareFormValue(c, "shareWith")
	if shareWith == "" {
		return Val{}, false, BadRequest("shareWith is required for this share type")
	}

	perms := res.Perms()
	if raw := shareFormValue(c, "permissions"); raw != "" {
		if n, perr := strconv.ParseInt(raw, 10, 64); perr == nil && n > 0 {
			perms = permsFromMask(n)
		}
	}
	if !res.Perms().Has(perms) {
		return Val{}, false, Forbidden("the requested permissions exceed what you may share")
	}

	spec := core.GrantSpec{
		Share:   res.Share(),
		Subpath: res.Path().String(),
		Allow:   perms,
		Inherit: true,
	}
	label := res.Path().Name()
	if label == "" {
		if def, ok := s.deps.Core.Share(res.Share()); ok {
			label = def.Name
		}
	}
	spec.Label = label

	switch shareType {
	case ShareTypeUser:
		id, ok, aerr := s.deps.Auth.ResolveAccount(ctx, shareWith)
		if aerr != nil {
			return Val{}, false, ocsErrorOf(aerr, apierr.VisibilityHidden)
		}
		if !ok {
			return Val{}, false, BadRequest("no such account")
		}
		spec.User = &id
	case ShareTypeGroup:
		id, ok, gerr := s.deps.Auth.ResolveGroup(ctx, shareWith)
		if gerr != nil {
			return Val{}, false, ocsErrorOf(gerr, apierr.VisibilityHidden)
		}
		if !ok {
			return Val{}, false, BadRequest("no such group")
		}
		spec.Group = &id
	}

	grant, gerr := s.deps.Core.CreateGrant(ctx, spec)
	if gerr != nil {
		return Val{}, false, ocsErrorOf(gerr, apierr.VisibilityKnown)
	}
	v, ok := s.grantShareValAt(ctx, user(p), grant, res)
	if !ok {
		return Val{}, false, Failure("the share could not be rendered")
	}
	return v, true, nil
}

// getShare, updateShare and deleteShare all start by parsing the wire id
// and finding out whether the caller administers it. An id the caller does
// not administer answers not-found, never denied: the id space is a small
// guessable integer, and a denied answer would let a stranger enumerate
// which ids are somebody else's real shares.

// getShare answers GET .../shares/{id}.
func (s *Server) getShare(c *fiber.Ctx, p Principal, id string) (Val, bool, *Error) {
	ctx := c.UserContext()
	owner := user(p)
	storeID, isGrant, ok := parseShareID(id)
	if !ok {
		return Val{}, false, NotFound("The requested share could not be found")
	}
	if isGrant {
		grant, res, err := s.adminGrant(c, p, storeID)
		if err != nil {
			return Val{}, false, NotFound("The requested share could not be found")
		}
		v, ok := s.grantShareValAt(ctx, owner, grant, res)
		if !ok {
			return Val{}, false, NotFound("The requested share could not be found")
		}
		return v, true, nil
	}
	link, lerr := s.deps.Core.GetLink(ctx, owner, storeID)
	if lerr != nil {
		return Val{}, false, NotFound("The requested share could not be found")
	}
	return s.linkShareVal(c, link), true, nil
}

// adminGrant reads a grant the caller currently administers, folding a
// missing row and one the caller no longer holds Share over into one
// refusal.
func (s *Server) adminGrant(c *fiber.Ctx, p Principal, id int64) (core.Grant, core.Resolved, error) {
	grant, err := s.deps.Core.GrantByID(c.UserContext(), id)
	if err != nil {
		return core.Grant{}, core.Resolved{}, core.ErrNotFound
	}
	res, ok := s.grantAdminContext(c.UserContext(), p, grant)
	if !ok {
		return core.Grant{}, core.Resolved{}, core.ErrNotFound
	}
	return grant, res, nil
}

// grantAdminContext resolves a grant's target through the caller's own
// principal and answers it only when Share still holds there, which is
// this package's whole test for "the caller administers this grant". Using
// s.resolve, not a bare core.Resolve, is what keeps a device credential's
// mask and share allowlist in force for the decision.
func (s *Server) grantAdminContext(ctx context.Context, p Principal, g core.Grant) (core.Resolved, bool) {
	vpath, ok := s.grantVpathFor(user(p), g)
	if !ok {
		return core.Resolved{}, false
	}
	res, err := s.resolve(ctx, p, vpath, acl.Share)
	if err != nil {
		return core.Resolved{}, false
	}
	return res, true
}

// grantVpathFor crosses a grant's stored share and subpath back into the
// path one viewer's own client would address it by.
func (s *Server) grantVpathFor(viewer core.UserID, g core.Grant) (string, bool) {
	if s.deps.VpathOf == nil {
		return "", false
	}
	share, ok := shareIDOf(g.Share)
	if !ok {
		return "", false
	}
	vpath, err := s.deps.VpathOf(viewer, share, grantSubpath(g))
	if err != nil {
		return "", false
	}
	return vpath, true
}

// grantSubpath reads a grant's stored subpath as the share-relative form the
// crossing expects: no leading slash.
func grantSubpath(g core.Grant) string {
	return grantSubpathOf(acl.ParsePath(g.Subpath).String())
}

// grantSubpathOf trims the leading separator off a stored path.
func grantSubpathOf(stored string) string { return strings.TrimPrefix(stored, "/") }

// lastComponent is the name at the end of a path, which is what a media type
// and a preview flag are decided from.
func lastComponent(path string) string {
	trimmed := strings.TrimSuffix(path, "/")
	if i := strings.LastIndexByte(trimmed, '/'); i >= 0 {
		return trimmed[i+1:]
	}
	return trimmed
}

// updateShare answers PUT .../shares/{id}. The client sends JSON here
// (unlike create's form body), but both bodies are accepted on both
// routes: parseSharePatchBody reads whichever one arrived.
func (s *Server) updateShare(c *fiber.Ctx, p Principal, id string) (Val, bool, *Error) {
	storeID, isGrant, ok := parseShareID(id)
	if !ok {
		return Val{}, false, NotFound("The requested share could not be found")
	}
	if isGrant {
		return s.updateGrantShare(c, p, storeID)
	}
	return s.updateLinkShare(c, p, storeID)
}

// updateLinkShare applies a share update to a public link. UpdateLink
// itself re-checks the owner's present access before it lets permissions
// widen, so this handler only translates the wire patch.
//
// hideDownload, note and label have nothing this engine cannot already
// represent through Perms and the link's own Note and Label fields, so
// they are folded into those rather than refused: a link's own
// hide-download is exactly "Read without Download". attributes and
// federation have no representation and nothing in this request asks for
// either, so there is nothing to refuse.
func (s *Server) updateLinkShare(c *fiber.Ctx, p Principal, id int64) (Val, bool, *Error) {
	ctx := c.UserContext()
	owner := user(p)
	current, cerr := s.deps.Core.GetLink(ctx, owner, id)
	if cerr != nil {
		return Val{}, false, NotFound("The requested share could not be found")
	}

	req := parseSharePatchBody(c)
	var patch core.LinkPatch

	switch {
	case req.hasPermissions:
		perms := permsFromMask(req.permissions)
		if req.hasPublicUpload && req.publicUpload {
			perms |= acl.Create
		}
		patch.Perms = &perms
	case req.hasPublicUpload:
		perms := current.Perms
		if req.publicUpload {
			perms |= acl.Create
		} else {
			perms = perms.Remove(acl.Create)
		}
		patch.Perms = &perms
	}

	if req.hasPassword {
		if req.password == "" {
			var nilPW *string
			patch.Password = &nilPW
		} else {
			pw := req.password
			set := &pw
			patch.Password = &set
		}
	}
	if req.hasExpireDate {
		if req.expireDate == "" {
			var nilExp *int64
			patch.Expires = &nilExp
		} else {
			t, perr := parseShareDate(req.expireDate)
			if perr != nil {
				return Val{}, false, BadRequest("expireDate could not be parsed")
			}
			exp := t
			set := &exp
			patch.Expires = &set
		}
	}
	if req.hasNote {
		note := req.note
		patch.Note = &note
	}
	if req.hasLabel {
		label := req.label
		patch.Label = &label
	}

	link, uerr := s.deps.Core.UpdateLink(ctx, owner, id, patch)
	if uerr != nil {
		return Val{}, false, ocsErrorOf(uerr, apierr.VisibilityKnown)
	}
	return s.linkShareVal(c, link), true, nil
}

// updateGrantShare applies a share update to an account or group grant.
//
// A grant carries no expiry, no password and no hide-download flag: this
// engine's grants are permanent and unconditional past their permission
// set. A request clearing one of those (an empty password or expiry) is
// accepted as a no-op, since there was never one to clear; a request
// setting one is refused with a clear message, because silently dropping
// it would leave whoever set it believing a password or expiry now
// protects a share that carries neither. The escalation guard runs against
// the caller's current access, the same as create, rather than whatever
// justified the grant when it was made.
func (s *Server) updateGrantShare(c *fiber.Ctx, p Principal, id int64) (Val, bool, *Error) {
	ctx := c.UserContext()
	current, res, aerr := s.adminGrant(c, p, id)
	if aerr != nil {
		return Val{}, false, NotFound("The requested share could not be found")
	}

	req := parseSharePatchBody(c)
	if req.hasExpireDate && req.expireDate != "" {
		return Val{}, false, BadRequest("an account or group share cannot carry an expiry date")
	}
	if req.hasPassword && req.password != "" {
		return Val{}, false, BadRequest("an account or group share cannot carry a password")
	}

	allow := acl.Perms(current.Allow)
	if req.hasPermissions {
		allow = permsFromMask(req.permissions)
		if !res.Perms().Has(allow) {
			return Val{}, false, Forbidden("the requested permissions exceed what you may share")
		}
	}
	label := current.Label
	if req.hasLabel {
		label = req.label
	}

	grant, uerr := s.deps.Core.UpdateGrant(ctx, id, allow, acl.Perms(current.Deny), current.Inherit, label)
	if uerr != nil {
		return Val{}, false, ocsErrorOf(uerr, apierr.VisibilityKnown)
	}
	v, ok := s.grantShareValAt(ctx, user(p), grant, res)
	if !ok {
		return Val{}, false, Failure("the share could not be rendered")
	}
	return v, true, nil
}

// deleteShare answers DELETE .../shares/{id}.
func (s *Server) deleteShare(c *fiber.Ctx, p Principal, id string) (Val, bool, *Error) {
	ctx := c.UserContext()
	owner := user(p)
	storeID, isGrant, ok := parseShareID(id)
	if !ok {
		return Val{}, false, NotFound("The requested share could not be found")
	}
	if isGrant {
		if _, _, aerr := s.adminGrant(c, p, storeID); aerr != nil {
			return Val{}, false, NotFound("The requested share could not be found")
		}
		if derr := s.deps.Core.DeleteGrant(ctx, storeID); derr != nil {
			return Val{}, false, ocsErrorOf(derr, apierr.VisibilityKnown)
		}
		return Obj(), true, nil
	}
	if derr := s.deps.Core.DeleteLink(ctx, owner, storeID); derr != nil {
		return Val{}, false, ocsErrorOf(derr, apierr.VisibilityHidden)
	}
	return Obj(), true, nil
}

// sharePatchBody is a share update, read once from whichever of the two
// bodies the request arrived with. Every field distinguishes "not sent"
// from "sent empty", because an empty password or expiry date clears the
// field while an absent one leaves it alone, and a typed struct with plain
// fields cannot carry that distinction.
type sharePatchBody struct {
	permissions     int64
	password        string
	expireDate      string
	note            string
	label           string
	publicUpload    bool
	hasPermissions  bool
	hasPassword     bool
	hasExpireDate   bool
	hasNote         bool
	hasLabel        bool
	hasPublicUpload bool
}

// parseSharePatchBody reads a share update from whichever body shape the
// request carries: JSON object keys, or form post-args presence.
func parseSharePatchBody(c *fiber.Ctx) sharePatchBody {
	var req sharePatchBody
	if c.Is("json") {
		var raw map[string]any
		if err := c.BodyParser(&raw); err == nil {
			applyJSONSharePatch(&req, raw)
		}
		return req
	}
	args := c.Request().PostArgs()
	if args.Has("permissions") {
		if n, err := strconv.ParseInt(c.FormValue("permissions"), 10, 64); err == nil && n > 0 {
			req.permissions, req.hasPermissions = n, true
		}
	}
	if args.Has("password") {
		req.password, req.hasPassword = c.FormValue("password"), true
	}
	if args.Has("expireDate") {
		req.expireDate, req.hasExpireDate = c.FormValue("expireDate"), true
	}
	if args.Has("note") {
		req.note, req.hasNote = c.FormValue("note"), true
	}
	if args.Has("label") {
		req.label, req.hasLabel = c.FormValue("label"), true
	}
	if args.Has("publicUpload") {
		req.publicUpload, req.hasPublicUpload = queryBool(c.FormValue("publicUpload")), true
	}
	return req
}

// applyJSONSharePatch reads the same fields out of a decoded JSON object.
// The map form is what lets "absent" and "present and empty" stay
// distinguishable, which a struct of plain typed fields cannot do.
func applyJSONSharePatch(req *sharePatchBody, raw map[string]any) {
	if v, ok := raw["permissions"]; ok {
		if n, ok := v.(float64); ok && n > 0 {
			req.permissions, req.hasPermissions = int64(n), true
		}
	}
	if v, ok := raw["password"]; ok {
		if str, ok := v.(string); ok {
			req.password, req.hasPassword = str, true
		}
	}
	if v, ok := raw["expireDate"]; ok {
		if str, ok := v.(string); ok {
			req.expireDate, req.hasExpireDate = str, true
		}
	}
	if v, ok := raw["note"]; ok {
		if str, ok := v.(string); ok {
			req.note, req.hasNote = str, true
		}
	}
	if v, ok := raw["label"]; ok {
		if str, ok := v.(string); ok {
			req.label, req.hasLabel = str, true
		}
	}
	if v, ok := raw["publicUpload"]; ok {
		switch t := v.(type) {
		case bool:
			req.publicUpload, req.hasPublicUpload = t, true
		case string:
			req.publicUpload, req.hasPublicUpload = queryBool(t), true
		}
	}
}

// shareFormValue reads one field of a create request, tolerating a JSON
// content type as well as the form body every reference client sends: one
// client's create call may arrive as either, and both are accepted on both
// share routes.
func shareFormValue(c *fiber.Ctx, key string) string {
	if c.Is("json") {
		var raw map[string]any
		if err := c.BodyParser(&raw); err != nil {
			return ""
		} else if v, ok := raw[key]; ok {
			switch t := v.(type) {
			case string:
				return t
			case float64:
				return strconv.FormatFloat(t, 'f', -1, 64)
			case bool:
				if t {
					return "true"
				}
				return "false"
			}
		}
		return ""
	}
	return c.FormValue(key)
}

// queryBool reads the "true"/"1" spelling every reference client sends for
// a boolean form or query value.
func queryBool(v string) bool { return v == "true" || v == "1" }

// permsFromMask reads the share API's integer bitmask into this engine's
// permission bits: the direct inverse of SharePermissionMask. The wire
// vocabulary has no bit for Rename or Move, so a mask never grants either;
// a share whose recipient should also rename or move within it has no wire
// spelling this engine can accept, and none is invented here.
func permsFromMask(mask int64) acl.Perms {
	var p acl.Perms
	if mask&SharePermRead != 0 {
		p |= acl.Read | acl.Download
	}
	if mask&SharePermUpdate != 0 {
		p |= acl.Write
	}
	if mask&SharePermCreate != 0 {
		p |= acl.Create
	}
	if mask&SharePermDelete != 0 {
		p |= acl.Delete
	}
	if mask&SharePermShare != 0 {
		p |= acl.Share
	}
	return p
}

// parseShareDate reads the share API's date parameter, which arrives as
// either a bare "2006-01-02" or the full timestamp form, and answers the
// nanosecond epoch UpdateLink and CreateLink expect.
func parseShareDate(raw string) (int64, error) {
	for _, layout := range []string{"2006-01-02", "2006-01-02 15:04:05"} {
		t, err := time.ParseInLocation(layout, raw, time.UTC)
		if err != nil {
			continue
		}
		// A date picker reaches years this server cannot count nanoseconds
		// to, and the conversion silently wraps into the past, which the
		// store then refuses as an expiry already gone. Clamped instead: a
		// client asking for a date beyond the representable range means
		// "effectively never", and answering that is closer to the request
		// than refusing it.
		if t.After(maxExpiry()) {
			return maxExpiry().UnixNano(), nil
		}
		return t.UnixNano(), nil
	}
	return 0, errBadRange
}

// maxExpiry is the furthest instant a nanosecond timestamp can name, less a
// margin so arithmetic on it cannot wrap.
func maxExpiry() time.Time { return time.Unix(0, 1<<62).UTC() }

// formatShareExpiration renders a link's expiry the way the field table
// specifies: "2006-01-02 15:04:05", empty when there is none.
func formatShareExpiration(ns int64) string {
	if ns == 0 {
		return ""
	}
	return time.Unix(0, ns).UTC().Format("2006-01-02 15:04:05")
}

// linkShareVal renders a public link as the wire share shape.
//
// url is always the absolute public URL when a token and a path renderer
// are both available: an empty url makes the Android client synthesise
// <base>/index.php/s/<token> instead, which only works because token is
// always correct, so this never leaves url empty while a token exists and
// PublicLinkPath is wired. password is never emitted past evidence it
// exists: no value, no hash, ever crosses this boundary.
func (s *Server) linkShareVal(c *fiber.Ctx, l core.Link) Val {
	ctx := c.UserContext()
	login := s.loginNameOf(ctx, Principal{UserID: int64(l.Owner)})

	token := ""
	if l.Token != nil {
		token = string(l.Token.Reveal())
	}
	url := ""
	if s.deps.PublicLinkPath != nil && token != "" {
		url = s.deps.Origin(originRequestOf(c)) + s.deps.PublicLinkPath(token)
	}

	itemType := "file"
	path := "/" + l.Path.String()
	var fileID uint64
	if s.deps.VpathOf != nil {
		if vpath, verr := s.deps.VpathOf(l.Owner, l.Share, l.Path.String()); verr == nil {
			path = "/" + vpath
			if res, rerr := s.deps.Resolve(l.Owner, vpath, 0); rerr == nil {
				if entry, serr := s.deps.Core.Stat(ctx, res); serr == nil {
					if entry.IsDir {
						itemType = "folder"
					}
					fileID = s.fileID(ctx, entry)
				}
			}
		}
	}
	if itemType == "folder" && !strings.HasSuffix(path, "/") {
		path += "/"
	}

	return Obj(
		P("id", Str(strconv.FormatInt(wireLinkID(l.ID), 10))),
		P("share_type", Int(ShareTypePublicLink)),
		P("uid_owner", Str(login)),
		P("displayname_owner", Str(login)),
		P("uid_file_owner", Str(login)),
		P("displayname_file_owner", Str(login)),
		P("permissions", Int(SharePermissionMask(l.Perms))),
		P("stime", Int(l.CreatedNs/int64(time.Second))),
		P("parent", Str("")),
		P("expiration", Str(formatShareExpiration(l.Expires))),
		P("token", Str(token)),
		P("path", Str(path)),
		P("item_type", Str(itemType)),
		P("item_source", Int(int64(fileID))),
		P("file_source", Int(int64(fileID))),
		P("file_parent", Int(0)),
		P("file_target", Str(path)),
		P("mimetype", Str(ContentTypeOf(itemType == "folder", l.Path.String()))),
		P("storage", Int(0)),
		P("storage_id", Str("")),
		P("share_with", Str("")),
		P("share_with_displayname", Str("")),
		P("share_with_link", Str(url)),
		P("url", Str(url)),
		P("mail_send", Bool(false)),
		P("hide_download", Bool(!l.Perms.Has(acl.Download))),
		P("note", Str(l.Note)),
		P("label", Str(l.Label)),
		P("password", passwordEvidence(l.HasPassword)),
		P("has_preview", Bool(itemType != "folder" && PreviewableName(l.Path.String()))),
		P("can_edit", Bool(l.Perms.Has(acl.Write))),
		P("can_delete", Bool(l.Perms.Has(acl.Delete))),
		P("attributes", Str("[]")),
		P("send_password_by_talk", Bool(false)),
	)
}

// grantViewerVal renders a grant for one viewer, re-resolving its context
// from that viewer's own vantage first. It reports false when the grant no
// longer names a share this deployment can resolve, or a subpath the
// viewer can no longer reach at all.
func (s *Server) grantViewerVal(ctx context.Context, viewer core.UserID, g core.Grant) (Val, bool) {
	vpath, ok := s.grantVpathFor(viewer, g)
	if !ok {
		return Val{}, false
	}
	res, err := s.deps.Resolve(viewer, vpath, 0)
	if err != nil {
		return Val{}, false
	}
	return s.grantShareValAt(ctx, viewer, g, res)
}

// grantShareValAt renders a grant whose context has already been resolved,
// for a caller that already paid for the resolution.
func (s *Server) grantShareValAt(ctx context.Context, viewer core.UserID, g core.Grant, res core.Resolved) (Val, bool) {
	if s.deps.VpathOf == nil {
		return Val{}, false
	}
	share, ok := shareIDOf(g.Share)
	if !ok {
		return Val{}, false
	}
	vpath, verr := s.deps.VpathOf(viewer, share, grantSubpath(g))
	if verr != nil {
		return Val{}, false
	}
	path := "/" + vpath

	itemType := "file"
	var fileID uint64
	if entry, serr := s.deps.Core.Stat(ctx, res); serr == nil {
		if entry.IsDir {
			itemType = "folder"
		}
		fileID = s.fileID(ctx, entry)
	}
	if itemType == "folder" && !strings.HasSuffix(path, "/") {
		path += "/"
	}

	// The account whose vantage this render ran under: for the caller's
	// own administered listing that is the person a Nextcloud client
	// calls the owner, since this engine keeps no separate creator column
	// on a grant. For a "shared with me" listing it is the caller too,
	// which understates who really shared it, but a wrong name here is
	// strictly less misleading than a fabricated one this engine has no
	// stored fact to back.
	login := s.loginNameOf(ctx, Principal{UserID: int64(viewer)})

	shareType := ShareTypeUser
	shareWith := ""
	shareWithDisplay := ""
	if g.User != nil {
		if info, err := s.deps.Auth.AccountInfo(ctx, *g.User); err == nil {
			shareWith = info.LoginName
			shareWithDisplay = info.DisplayName
		}
	} else if g.Group != nil {
		shareType = ShareTypeGroup
		if groups, gerr := s.deps.Auth.ListGroups(ctx); gerr == nil {
			for _, row := range groups {
				if row.ID == *g.Group {
					shareWith = row.Name
					shareWithDisplay = row.Name
					break
				}
			}
		}
	}

	perms := acl.Perms(g.Allow)
	return Obj(
		P("id", Str(strconv.FormatInt(wireGrantID(g.ID), 10))),
		P("share_type", Int(int64(shareType))),
		P("uid_owner", Str(login)),
		P("displayname_owner", Str(login)),
		P("uid_file_owner", Str(login)),
		P("displayname_file_owner", Str(login)),
		P("permissions", Int(SharePermissionMask(perms))),
		P("stime", Int(g.CreatedNs/int64(time.Second))),
		P("parent", Str("")),
		P("expiration", Str("")),
		P("token", Str("")),
		P("path", Str(path)),
		P("item_type", Str(itemType)),
		P("item_source", Int(int64(fileID))),
		P("file_source", Int(int64(fileID))),
		P("file_parent", Int(0)),
		P("file_target", Str(path)),
		P("mimetype", Str(ContentTypeOf(itemType == "folder", lastComponent(path)))),
		P("storage", Int(0)),
		P("storage_id", Str("")),
		P("share_with", Str(shareWith)),
		P("share_with_displayname", Str(shareWithDisplay)),
		// No url and no share_with_link: a client that finds either one
		// re-types the share as a public link whatever share_type said,
		// and a grant has no link to name.
		P("mail_send", Bool(false)),
		P("hide_download", Bool(!perms.Has(acl.Download))),
		P("note", Str("")),
		P("label", Str(g.Label)),
		P("password", Str("")),
		P("has_preview", Bool(itemType != "folder" && PreviewableName(lastComponent(path)))),
		P("can_edit", Bool(perms.Has(acl.Write))),
		P("can_delete", Bool(perms.Has(acl.Delete))),
		P("attributes", Str("[]")),
		P("send_password_by_talk", Bool(false)),
	), true
}

// passwordEvidence renders the password field: present only as evidence a
// password protects the share, never the value or anything derived from
// it. The Android client reads only whether the node is non-empty.
func passwordEvidence(has bool) Val {
	if !has {
		return Str("")
	}
	return Str("1")
}
