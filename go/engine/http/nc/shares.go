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

// The share API. One engine concept answers for it: a public link
// (core.Link, share type 3).
//
// An acl grant is not a share here and is never rendered as one. A grant is
// how this deployment's administration hands an account access to a share
// in the first place; the account did not publish it, and the grant table
// records no creator, so a grant somebody made through a client and a grant
// an administrator wrote are the same row. Listing them beside the links
// showed a person their own access as something they had shared, which is
// why the whole family is answered from the link store alone: listing,
// creation, update and deletion. Account and group sharing is refused
// rather than half-answered, because a share a client can create and never
// see again is worse than one it cannot create.
//
// A wire id is a link's own row id, and it has to stay inside a signed
// 32-bit integer: one client reads it with a 32-bit parse, and a single id
// past that ceiling makes it discard the entire document the id arrived in,
// not just the one entry.

// parseLinkID reads a wire id back into the link store's row id.
func parseLinkID(raw string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 32)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// listShares answers GET .../shares.
//
// Without a path: every link the caller owns. With a path: only the links
// on that exact resource; with subfiles=true, the links on the entries
// directly below it instead. shared_with_me asks for what other people
// shared with the caller, which on this deployment is the access they were
// granted rather than anything anybody published, so it is empty.
// reshares and include_tags are accepted and not otherwise consulted: no
// link here has a second creator to report, and no tag storage beyond
// favourites exists for this listing to draw on.
//
// An empty result is an empty list, never a refusal: a share listing is a
// routine question, and answering "forbidden" for "nothing published here
// yet" would show every unshared file as an error.
func (s *Server) listShares(c *fiber.Ctx, p Principal) (Val, bool, *Error) {
	ctx := c.UserContext()
	owner := user(p)

	if queryBool(c.Query("shared_with_me")) {
		return List(), true, nil
	}

	// The path a client sends is its own spelling, and a folder always carries
	// a trailing separator there. Resolution splits on that separator, so the
	// spelling arrived as a path with an empty last component and answered as
	// absent: the sharing panel of every folder reported that it could not
	// read the folder's shares, and the share screen it fronts never opened.
	path := strings.Trim(c.Query("path"), "/")
	if path == "" {
		// The account root, addressed either as nothing or as the separator
		// alone. It holds no links of its own: what it holds is the shares
		// themselves, so with subfiles asked for the answer is theirs, and
		// without it the answer is every link this caller published.
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

// sharesUnderRoot answers the links that sit on the account root's own
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

// sharesAtResolved answers every link the caller owns exactly at one path
// they have already resolved for reading.
func (s *Server) sharesAtResolved(c *fiber.Ctx, res core.Resolved) []Val {
	ctx := c.UserContext()
	items := make([]Val, 0)
	links, err := s.deps.Core.ListLinks(ctx, res.User(), &res)
	if err != nil {
		return items
	}
	for _, l := range links {
		items = append(items, s.linkShareVal(c, l))
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
		// Sharing with an account is this deployment's administration, not
		// a gesture a client makes: it writes an acl grant that nothing
		// distinguishes from the ones an administrator wrote, and no
		// listing here reports one.
		return Val{}, false, Forbidden("this deployment shares by link only")
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

// getShare, updateShare and deleteShare all start by parsing the wire id.
// An id the caller does not own answers not-found, never denied: the id
// space is a small guessable integer, and a denied answer would let a
// stranger enumerate which ids are somebody else's real links.

// getShare answers GET .../shares/{id}.
func (s *Server) getShare(c *fiber.Ctx, p Principal, id string) (Val, bool, *Error) {
	ctx := c.UserContext()
	storeID, ok := parseLinkID(id)
	if !ok {
		return Val{}, false, NotFound("The requested share could not be found")
	}
	link, lerr := s.deps.Core.GetLink(ctx, user(p), storeID)
	if lerr != nil {
		return Val{}, false, NotFound("The requested share could not be found")
	}
	return s.linkShareVal(c, link), true, nil
}

// grantSubpathOf trims the leading separator off a stored path.
func grantSubpathOf(stored string) string { return strings.TrimPrefix(stored, "/") }

// updateShare answers PUT .../shares/{id}. The client sends JSON here
// (unlike create's form body), but both bodies are accepted on both
// routes: parseSharePatchBody reads whichever one arrived.
func (s *Server) updateShare(c *fiber.Ctx, p Principal, id string) (Val, bool, *Error) {
	storeID, ok := parseLinkID(id)
	if !ok {
		return Val{}, false, NotFound("The requested share could not be found")
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

// deleteShare answers DELETE .../shares/{id}.
func (s *Server) deleteShare(c *fiber.Ctx, p Principal, id string) (Val, bool, *Error) {
	ctx := c.UserContext()
	storeID, ok := parseLinkID(id)
	if !ok {
		return Val{}, false, NotFound("The requested share could not be found")
	}
	if derr := s.deps.Core.DeleteLink(ctx, user(p), storeID); derr != nil {
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
		P("id", Str(strconv.FormatInt(l.ID, 10))),
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

// passwordEvidence renders the password field: present only as evidence a
// password protects the share, never the value or anything derived from
// it. The Android client reads only whether the node is non-empty.
func passwordEvidence(has bool) Val {
	if !has {
		return Str("")
	}
	return Str("1")
}
