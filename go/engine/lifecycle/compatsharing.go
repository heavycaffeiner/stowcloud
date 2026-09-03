//go:build linux && compat_nc

package lifecycle

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/compat"
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

func permsFromShareMask(mask int64, isDir bool) acl.Perms {
	if mask <= 0 {
		if isDir {
			return acl.Read | acl.Download | acl.Write | acl.Create | acl.Delete | acl.Rename | acl.Move
		}
		return acl.Read | acl.Download | acl.Write
	}
	var p acl.Perms
	if mask&compat.SharePermRead != 0 {
		p |= acl.Read | acl.Download
	}
	if mask&compat.SharePermUpdate != 0 {
		p |= acl.Write | acl.Rename | acl.Move
	}
	if mask&compat.SharePermCreate != 0 {
		p |= acl.Create
	}
	if mask&compat.SharePermDelete != 0 {
		p |= acl.Delete
	}
	if mask&compat.SharePermShare != 0 {
		p |= acl.Share
	}
	return p
}

type compatGroupInfo struct {
	names   map[int64]string
	members map[int64]struct{}
}

func (e *Engine) compatGroups(ctx context.Context, user core.UserID) (compatGroupInfo, error) {
	rows, err := e.Auth.ListGroups(ctx)
	if err != nil {
		return compatGroupInfo{}, err
	}

	info := compatGroupInfo{
		names:   make(map[int64]string, len(rows)),
		members: make(map[int64]struct{}),
	}
	for _, row := range rows {
		info.names[row.ID] = row.Name
		for _, member := range row.Members {
			if member == int64(user) {
				info.members[row.ID] = struct{}{}
				break
			}
		}
	}
	return info, nil
}

func grantIsForUser(g core.Grant, user core.UserID, groups compatGroupInfo) bool {
	if g.User != nil && *g.User == int64(user) {
		return true
	}
	if g.Group != nil {
		_, ok := groups.members[*g.Group]
		return ok
	}
	return false
}

func (e *Engine) compatGrantVpath(user core.UserID, g core.Grant) (vfs.Vpath, error) {
	path, err := vfs.ParseSharePath(g.Subpath)
	if err != nil {
		return vfs.Vpath{}, err
	}
	share, err := num.Narrow[uint32](g.Share)
	if err != nil {
		return vfs.Vpath{}, err
	}
	return e.Core.VpathFor(user, core.ShareID(share), path)
}

func (e *Engine) compatCanManageGrant(user core.UserID, g core.Grant) bool {
	path, err := e.compatGrantVpath(user, g)
	if err != nil {
		return false
	}
	_, err = e.resolve(user, path.String(), acl.Share)
	return err == nil
}

func (e *Engine) compatManagedGrant(
	ctx context.Context, user core.UserID, id int64,
) (core.Grant, bool, error) {
	grants, err := e.Core.ListGrants(ctx, core.GrantFilter{})
	if err != nil {
		return core.Grant{}, false, err
	}
	for _, grant := range grants {
		if grant.ID == id && e.compatCanManageGrant(user, grant) {
			return grant, true, nil
		}
	}
	return core.Grant{}, false, nil
}

func parseExpirationDate(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	for _, layout := range []string{"2006-01-02", "2006-01-02 15:04:05", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UnixNano(), nil
		}
	}
	return 0, fmt.Errorf("invalid date format: %s", s)
}

func (e *Engine) formatLinkShare(ctx context.Context, c *fiber.Ctx, l core.Link) compat.Share {
	ownerName := ""
	ownerDisplay := ""
	if info, err := e.Auth.UserByID(ctx, int64(l.Owner)); err == nil {
		ownerName = info.Name
		ownerDisplay = info.Display
	}

	tokStr := ""
	if l.Token != nil {
		tokStr = string(l.Token.Reveal())
	}

	var expS *int64
	if l.Expires > 0 {
		s := l.Expires / 1e9
		expS = &s
	}

	path := "/" + l.Path.String()
	isDir := false
	var fid uint64
	vp, verr := e.Core.VpathFor(l.Owner, l.Share, l.Path)
	if verr == nil {
		path = "/" + vp.String()
		r, rerr := e.Core.Resolve(l.Owner, vp, acl.Read)
		if rerr == nil {
			st, serr := r.Root().Stat(r.Path())
			if serr == nil {
				isDir = st.Kind.IsDir()
				entry := e.Core.EntryAt(r, st)
				if id, ferr := e.compatFileID(ctx, entry); ferr == nil {
					fid = id
				}
			}
		}
	}

	url := ""
	if tokStr != "" {
		url = fmt.Sprintf("%s/s/%s", e.compatOriginOf(c), tokStr)
	}

	return compat.Share{
		ID:           l.ID,
		Kind:         compat.GranteeLink,
		Owner:        ownerName,
		OwnerDisplay: ownerDisplay,
		Perms:        compat.SharePermissions(permBitsOf(l.Perms)),
		CreatedS:     l.CreatedNs / 1e9,
		ExpiresS:     expS,
		Token:        tokStr,
		Note:         l.Note,
		Label:        l.Label,
		Path:         path,
		IsDir:        isDir,
		FileID:       fid,
		HasPassword:  l.HasPassword,
		URL:          url,
	}
}

func (e *Engine) formatGrantShare(
	ctx context.Context, user core.UserID, g core.Grant, groups compatGroupInfo,
) compat.Share {
	granteeName := ""
	granteeDisplay := ""

	kind := compat.GranteeUser
	if g.Group != nil {
		kind = compat.GranteeGroup
		granteeName = groups.names[*g.Group]
		if granteeName == "" {
			granteeName = fmt.Sprintf("group_%d", *g.Group)
		}
		granteeDisplay = granteeName
	} else if g.User != nil {
		if info, err := e.Auth.UserByID(ctx, *g.User); err == nil {
			granteeName = info.Name
			granteeDisplay = info.Display
		}
	}

	path := "/" + g.Subpath
	isDir := false
	var fid uint64
	if vp, err := e.compatGrantVpath(user, g); err == nil {
		path = "/" + vp.String()
		if r, rerr := e.resolve(user, vp.String(), acl.Read); rerr == nil {
			if st, serr := r.Root().Stat(r.Path()); serr == nil {
				isDir = st.Kind.IsDir()
				entry := e.Core.EntryAt(r, st)
				if id, ferr := e.compatFileID(ctx, entry); ferr == nil {
					fid = id
				}
			}
		}
	}

	return compat.Share{
		ID:             g.ID,
		Kind:           kind,
		Perms:          compat.SharePermissions(permBitsOf(acl.Perms(g.Allow))),
		CreatedS:       g.CreatedNs / 1e9,
		Path:           path,
		IsDir:          isDir,
		FileID:         fid,
		Grantee:        granteeName,
		GranteeDisplay: granteeDisplay,
	}
}

func (e *Engine) compatListShares(
	c *fiber.Ctx, user core.UserID,
) (compat.Val, bool, *compat.OCSError) {
	filter := compat.ParseShareFilter(func(k string) string { return c.Query(k) })
	ctx := c.UserContext()
	groups, err := e.compatGroups(ctx, user)
	if err != nil {
		return compat.Val{}, false, compat.ServerError("could not read groups")
	}
	grants, err := e.Core.ListGrants(ctx, core.GrantFilter{})
	if err != nil {
		return compat.Val{}, false, compat.ServerError("could not read shares")
	}

	matchesPath := func(s compat.Share) bool {
		return filter.Path == "" || strings.TrimPrefix(s.Path, "/") == filter.Path
	}
	shares := make([]compat.Val, 0, len(grants))

	if filter.SharedWithMe {
		for _, grant := range grants {
			if !grantIsForUser(grant, user, groups) {
				continue
			}
			share := e.formatGrantShare(ctx, user, grant, groups)
			if matchesPath(share) {
				shares = append(shares, compat.FormatShare(share))
			}
		}
		return compat.ListOf(shares), true, nil
	}

	links, err := e.Core.ListLinks(ctx, user, nil)
	if err != nil {
		return compat.Val{}, false, compat.ServerError("could not read shares")
	}
	for _, link := range links {
		if link.Owner != user {
			continue
		}
		share := e.formatLinkShare(ctx, c, link)
		if matchesPath(share) {
			shares = append(shares, compat.FormatShare(share))
		}
	}
	for _, grant := range grants {
		if grantIsForUser(grant, user, groups) || !e.compatCanManageGrant(user, grant) {
			continue
		}
		share := e.formatGrantShare(ctx, user, grant, groups)
		if matchesPath(share) {
			shares = append(shares, compat.FormatShare(share))
		}
	}

	return compat.ListOf(shares), true, nil
}

func (e *Engine) compatCreateShare(
	c *fiber.Ctx, user core.UserID,
) (compat.Val, bool, *compat.OCSError) {
	req := compat.ShareRequestFromForm(func(k string) string { return c.FormValue(k) }, func(k string) bool { return c.FormValue(k) != "" })
	if req.Path == "" {
		req.Path = c.Query("path")
	}
	if req.Path == "" {
		return compat.Val{}, false, compat.BadRequest("missing path")
	}

	ctx := c.UserContext()
	cleanPath := compat.NormaliseClientPath(req.Path)
	r, err := e.resolve(user, cleanPath, acl.Share)
	if err != nil {
		return compat.Val{}, false, compat.NotFound("File not found")
	}

	st, serr := r.Root().Stat(r.Path())
	if serr != nil {
		return compat.Val{}, false, compat.NotFound("File not found")
	}
	isDir := st.Kind.IsDir()

	shareType := int64(compat.ShareTypeUser)
	if req.ShareType != nil {
		shareType = *req.ShareType
	}

	switch shareType {
	case compat.ShareTypePublicLink:
		var mask int64
		if req.Permissions != nil {
			mask = *req.Permissions
		}
		perms := permsFromShareMask(mask, isDir)

		var pw *string
		if req.Password != "" {
			pw = &req.Password
		}

		var expiresNs int64
		if req.Expiration != "" {
			exp, perr := parseExpirationDate(req.Expiration)
			if perr != nil {
				return compat.Val{}, false, compat.BadRequest("invalid expiration date")
			}
			expiresNs = exp
		}

		link, _, lerr := e.Core.CreateLink(ctx, r, core.LinkSpec{
			Perms:    perms,
			Password: pw,
			Expires:  expiresNs,
			Note:     req.Note,
			Label:    req.Label,
		})
		if lerr != nil {
			return compat.Val{}, false, compat.ServerError(lerr.Error())
		}
		return compat.FormatShare(e.formatLinkShare(ctx, c, link)), true, nil

	case compat.ShareTypeUser, compat.ShareTypeGroup:
		if req.ShareWith == "" {
			return compat.Val{}, false, compat.BadRequest("missing shareWith")
		}
		var spec core.GrantSpec
		groups := compatGroupInfo{}
		switch shareType {
		case compat.ShareTypeUser:
			targetUser, uerr := e.Auth.UserIDByName(ctx, req.ShareWith)
			if uerr != nil {
				return compat.Val{}, false, compat.NotFound("user not found")
			}
			holder := int64(targetUser)
			spec.User = &holder
		case compat.ShareTypeGroup:
			groupID, found, gerr := e.Auth.ResolveGroup(ctx, req.ShareWith)
			if gerr != nil {
				return compat.Val{}, false, compat.ServerError("could not read group")
			}
			if !found {
				return compat.Val{}, false, compat.NotFound("group not found")
			}
			spec.Group = &groupID
			groups.names = map[int64]string{groupID: req.ShareWith}
		}
		var mask int64
		if req.Permissions != nil {
			mask = *req.Permissions
		}
		spec.Share = r.Share()
		spec.Subpath = r.Path().String()
		spec.Allow = permsFromShareMask(mask, isDir)
		spec.Inherit = isDir
		spec.Label = req.Label

		grant, gerr := e.Core.CreateGrant(ctx, spec)
		if gerr != nil {
			return compat.Val{}, false, compat.ServerError(gerr.Error())
		}
		return compat.FormatShare(e.formatGrantShare(ctx, user, grant, groups)), true, nil

	default:
		return compat.Val{}, false, compat.BadRequest("unsupported share type")
	}
}

func (e *Engine) compatGetShare(
	c *fiber.Ctx, user core.UserID, idStr string,
) (compat.Val, bool, *compat.OCSError) {
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return compat.Val{}, false, compat.NotFound("invalid share id")
	}

	ctx := c.UserContext()
	links, lerr := e.Core.ListLinks(ctx, user, nil)
	if lerr != nil {
		return compat.Val{}, false, compat.ServerError("could not read shares")
	}
	for _, link := range links {
		if link.ID == id && link.Owner == user {
			return compat.FormatShare(e.formatLinkShare(ctx, c, link)), true, nil
		}
	}

	groups, gerr := e.compatGroups(ctx, user)
	if gerr != nil {
		return compat.Val{}, false, compat.ServerError("could not read groups")
	}
	grants, gerr := e.Core.ListGrants(ctx, core.GrantFilter{})
	if gerr != nil {
		return compat.Val{}, false, compat.ServerError("could not read shares")
	}
	for _, grant := range grants {
		if grant.ID != id {
			continue
		}
		if grantIsForUser(grant, user, groups) || e.compatCanManageGrant(user, grant) {
			return compat.FormatShare(e.formatGrantShare(ctx, user, grant, groups)), true, nil
		}
	}

	return compat.Val{}, false, compat.NotFound("share not found")
}

func (e *Engine) compatUpdateShare(
	c *fiber.Ctx, user core.UserID, idStr string,
) (compat.Val, bool, *compat.OCSError) {
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return compat.Val{}, false, compat.NotFound("invalid share id")
	}

	ctx := c.UserContext()
	links, lerr := e.Core.ListLinks(ctx, user, nil)
	if lerr != nil {
		return compat.Val{}, false, compat.ServerError("could not read shares")
	}
	for _, link := range links {
		if link.ID != id || link.Owner != user {
			continue
		}
		patch := core.LinkPatch{}
		if rawPerms := c.FormValue("permissions"); rawPerms != "" {
			mask, perr := strconv.ParseInt(rawPerms, 10, 64)
			if perr != nil {
				return compat.Val{}, false, compat.BadRequest("invalid permissions")
			}
			p := permsFromShareMask(mask, false)
			patch.Perms = &p
		}
		if rawPassword := c.FormValue("password"); rawPassword != "" {
			pwp := &rawPassword
			patch.Password = &pwp
		}
		if rawExp := c.FormValue("expireDate"); rawExp != "" {
			exp, perr := parseExpirationDate(rawExp)
			if perr != nil {
				return compat.Val{}, false, compat.BadRequest("invalid expiration date")
			}
			expp := &exp
			patch.Expires = &expp
		}
		if rawLabel := c.FormValue("label"); rawLabel != "" {
			patch.Label = &rawLabel
		}
		if rawNote := c.FormValue("note"); rawNote != "" {
			patch.Note = &rawNote
		}

		updated, uerr := e.Core.UpdateLink(ctx, user, id, patch)
		if uerr != nil {
			return compat.Val{}, false, compat.ServerError(uerr.Error())
		}
		return compat.FormatShare(e.formatLinkShare(ctx, c, updated)), true, nil
	}

	grant, found, gerr := e.compatManagedGrant(ctx, user, id)
	if gerr != nil {
		return compat.Val{}, false, compat.ServerError("could not read shares")
	}
	if !found {
		return compat.Val{}, false, compat.NotFound("share not found")
	}
	allow := acl.Perms(grant.Allow)
	if rawPerms := c.FormValue("permissions"); rawPerms != "" {
		mask, perr := strconv.ParseInt(rawPerms, 10, 64)
		if perr != nil || mask <= 0 {
			return compat.Val{}, false, compat.BadRequest("invalid permissions")
		}
		allow = permsFromShareMask(mask, false)
	}
	label := grant.Label
	if rawLabel := c.FormValue("label"); rawLabel != "" {
		label = rawLabel
	}
	if uerr := e.Core.UpdateGrant(ctx, id, allow, acl.Perms(grant.Deny), grant.Inherit, label); uerr != nil {
		return compat.Val{}, false, compat.ServerError("could not update share")
	}
	grant.Allow = uint16(allow)
	grant.Label = label
	groups, gerr := e.compatGroups(ctx, user)
	if gerr != nil {
		return compat.Val{}, false, compat.ServerError("could not read groups")
	}
	return compat.FormatShare(e.formatGrantShare(ctx, user, grant, groups)), true, nil
}

func (e *Engine) compatDeleteShare(
	c *fiber.Ctx, user core.UserID, idStr string,
) (compat.Val, bool, *compat.OCSError) {
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return compat.Val{}, false, compat.NotFound("invalid share id")
	}

	ctx := c.UserContext()
	links, lerr := e.Core.ListLinks(ctx, user, nil)
	if lerr != nil {
		return compat.Val{}, false, compat.ServerError("could not read shares")
	}
	for _, link := range links {
		if link.ID != id || link.Owner != user {
			continue
		}
		if derr := e.Core.DeleteLink(ctx, user, id); derr != nil {
			return compat.Val{}, false, compat.ServerError("could not delete link")
		}
		return compat.Object(), true, nil
	}

	_, found, gerr := e.compatManagedGrant(ctx, user, id)
	if gerr != nil {
		return compat.Val{}, false, compat.ServerError("could not read shares")
	}
	if !found {
		return compat.Val{}, false, compat.NotFound("share not found")
	}
	if derr := e.Core.DeleteGrant(ctx, id); derr != nil {
		return compat.Val{}, false, compat.ServerError("could not delete share")
	}
	return compat.Object(), true, nil
}
