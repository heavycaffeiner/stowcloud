//go:build linux && compat_nc

package lifecycle

import (
	"context"
	"fmt"
	"strconv"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/compat"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/preview"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
)

func (e *Engine) sealDirectClaim(user core.UserID, path string) (string, error) {
	nowNs := e.clock.Nanos()
	cl := handler.Claim{
		Purpose: handler.PurposeDownload,
		UserID:  int64(user),
		Path:    path,
	}
	return handler.SealClaim(e.claimKey, cl, nowNs)
}

func (e *Engine) openDirectClaim(token string) (core.UserID, string, error) {
	nowNs := e.clock.Nanos()
	keys := map[uint32][]byte{e.claimKey.Version: e.claimKey.Key}
	cl, err := handler.OpenClaim(keys, handler.PurposeDownload, token, nowNs)
	if err != nil {
		return 0, "", err
	}
	return core.UserID(cl.UserID), cl.Path, nil
}

// locateCompatFile finds a virtual path for the user given a file id.
func (e *Engine) locateCompatFile(ctx context.Context, user core.UserID, fileID uint64) (vfs.Vpath, error) {
	if e.Cache != nil {
		if n, nerr := num.Narrow[int64](fileID); nerr == nil {
			sID, sPath, err := e.Cache.Resolve(ctx, ident.FileID(n))
			if err == nil {
				vp, verr := e.Core.VpathFor(user, sID, sPath)
				if verr == nil {
					return vp, nil
				}
			}
		}
	}

	for _, rt := range e.Core.Roots(user) {
		vp, verr := vfs.ParseVpath("/" + rt.Label)
		if verr != nil {
			continue
		}
		res, rerr := e.Core.Resolve(user, vp, acl.Read)
		if rerr != nil {
			continue
		}
		found, ferr := e.walkFindFileID(ctx, res, fileID)
		if ferr == nil {
			return found, nil
		}
	}

	return vfs.Vpath{}, core.ErrNotFound
}

func (e *Engine) walkFindFileID(ctx context.Context, r core.Resolved, targetID uint64) (vfs.Vpath, error) {
	page, err := e.Core.List(ctx, r, "")
	if err != nil {
		return vfs.Vpath{}, err
	}
	for _, entry := range page.Entries {
		fid, ferr := e.compatFileID(ctx, entry)
		vp, verr := e.Core.VpathFor(r.User(), r.Share(), entry.Path)
		if ferr == nil && fid == targetID && verr == nil {
			return vp, nil
		}
		if entry.IsDir && verr == nil {
			childRes, cerr := e.Core.Resolve(r.User(), vp, acl.Read)
			if cerr == nil {
				if found, ferr := e.walkFindFileID(ctx, childRes, targetID); ferr == nil {
					return found, nil
				}
			}
		}
	}
	return vfs.Vpath{}, core.ErrNotFound
}

// compatDirect answers a direct-URL request by minting a signed content claim.
func (e *Engine) compatDirect(
	c *fiber.Ctx, user core.UserID,
) (compat.Val, bool, *compat.OCSError) {
	rawID := c.FormValue("fileId")
	if rawID == "" {
		rawID = c.Query("fileId")
	}
	id, err := compat.ParseFileID(rawID)
	if err != nil {
		return compat.Val{}, false, compat.NotFound("File not found")
	}

	vp, err := e.locateCompatFile(c.UserContext(), user, id)
	if err != nil {
		return compat.Val{}, false, compat.NotFound("File not found")
	}

	_, err = e.resolve(user, vp.String(), acl.Read|acl.Download)
	if err != nil {
		return compat.Val{}, false, compat.NotFound("File not found")
	}

	token, err := e.sealDirectClaim(user, vp.String())
	if err != nil {
		return compat.Val{}, false, compat.ServerError("could not mint direct URL")
	}

	directURL := e.compatOriginOf(c) + "/remote.php/direct/" + token
	return compat.DirectURL(directURL), true, nil
}

// compatDirectStream serves an unauthenticated direct media stream via a signed claim.
func (e *Engine) compatDirectStream(c *fiber.Ctx) error {
	token := c.Params("claim")
	if token == "" {
		return c.SendStatus(fiber.StatusNotFound)
	}

	user, path, err := e.openDirectClaim(token)
	if err != nil {
		return c.SendStatus(fiber.StatusNotFound)
	}

	r, err := e.resolve(user, path, acl.Download)
	if err != nil {
		return c.SendStatus(fiber.StatusNotFound)
	}

	entry, stream, err := e.Core.OpenStream(c.UserContext(), r, nil)
	if err != nil {
		return c.SendStatus(fiber.StatusNotFound)
	}

	size, nerr := strconv.ParseInt(strconv.FormatUint(entry.Size, 10), 10, 64)
	if nerr != nil {
		e.closeStream(stream, entry.Name)
		return c.SendStatus(fiber.StatusNotFound)
	}

	rng, ranged, rerr := handler.ParseRange(c.Get(fiber.HeaderRange), size)
	if rerr != nil {
		e.closeStream(stream, entry.Name)
		return c.SendStatus(fiber.StatusRequestedRangeNotSatisfiable)
	}

	if ranged {
		e.closeStream(stream, entry.Name)
		start, serr := strconv.ParseUint(strconv.FormatInt(rng.Start, 10), 10, 64)
		last, lerr := strconv.ParseUint(strconv.FormatInt(rng.End-1, 10), 10, 64)
		if serr != nil || lerr != nil {
			return c.SendStatus(fiber.StatusRequestedRangeNotSatisfiable)
		}
		var oerr error
		entry, stream, oerr = e.Core.OpenStream(c.UserContext(), r, &[2]uint64{start, last})
		if oerr != nil {
			return c.SendStatus(fiber.StatusNotFound)
		}
	}

	// Inline: a direct URL is what a media player opens, so a disposition
	// would make the client save the file instead of playing it.
	return e.sendStream(c, entry, stream, ranged, rng, size, "")
}

// compatPreview handles thumbnail and preview requests by fileId or path.
func (e *Engine) compatPreview(c *fiber.Ctx) error {
	user, ok := compatUser(c)
	if !ok {
		return c.SendStatus(fiber.StatusUnauthorized)
	}

	q := compat.ParsePreviewQuery(func(k string) string { return c.Query(k) })
	if q.ForceIcon {
		return c.SendStatus(fiber.StatusNotFound)
	}

	var r core.Resolved
	var err error

	if q.Path != "" {
		r, err = e.resolve(user, q.Path, acl.Read|acl.Download)
	} else if q.FileID != nil {
		var vp vfs.Vpath
		vp, err = e.locateCompatFile(c.UserContext(), user, *q.FileID)
		if err == nil {
			r, err = e.resolve(user, vp.String(), acl.Read|acl.Download)
		}
	} else {
		return c.SendStatus(fiber.StatusNotFound)
	}

	if err != nil || !e.thumbnailEnabled() {
		return c.SendStatus(fiber.StatusNotFound)
	}

	maxDim := max(q.Width, q.Height)
	preset := preview.PresetSmall
	if maxDim > 512 {
		preset = preview.PresetLarge
	} else if maxDim > 256 {
		preset = preview.PresetMedium
	}

	thumb, terr := e.Preview.Get(c.UserContext(), r, preset)
	if terr != nil {
		return c.SendStatus(fiber.StatusNotFound)
	}

	return e.sendThumb(c, thumb)
}

// compatThumbnailByPath handles requests matching /apps/files/api/v1/thumbnail/:x/:y/*.
func (e *Engine) compatThumbnailByPath(c *fiber.Ctx) error {
	user, ok := compatUser(c)
	if !ok {
		return c.SendStatus(fiber.StatusUnauthorized)
	}

	tail := c.Params("*")
	if tail == "" {
		return c.SendStatus(fiber.StatusNotFound)
	}

	x, err := strconv.Atoi(c.Params("x"))
	if err != nil || x <= 0 {
		x = 64
	}
	y, err := strconv.Atoi(c.Params("y"))
	if err != nil || y <= 0 {
		y = 64
	}

	r, err := e.resolve(user, tail, acl.Read|acl.Download)
	if err != nil || !e.thumbnailEnabled() {
		return c.SendStatus(fiber.StatusNotFound)
	}

	maxDim := max(x, y)
	preset := preview.PresetSmall
	if maxDim > 512 {
		preset = preview.PresetLarge
	} else if maxDim > 256 {
		preset = preview.PresetMedium
	}

	thumb, terr := e.Preview.Get(c.UserContext(), r, preset)
	if terr != nil {
		return c.SendStatus(fiber.StatusNotFound)
	}

	return e.sendThumb(c, thumb)
}

// compatRecent answers queries for recently modified entries.
func (e *Engine) compatRecent(
	c *fiber.Ctx, user core.UserID,
) (compat.Val, bool, *compat.OCSError) {
	q, err := compat.ParseRecentQuery(c.Query("limit"), c.Query("since"), e.clock.Now())
	if err != nil {
		return compat.Val{}, false, compat.BadRequest(err.Error())
	}

	hits, rerr := e.Core.Recent(c.UserContext(), user, core.RecentQuery{
		SinceNs: q.Since.UnixNano(),
		Limit:   q.Limit,
	})
	if rerr != nil {
		return compat.Val{}, false, compat.ServerError("the recency query failed")
	}

	entries := make([]compat.Val, 0, len(hits))
	for _, hit := range hits {
		r, rerr := e.resolve(user, hit.Vpath.String(), acl.Read)
		var fid uint64
		if rerr == nil {
			st, serr := r.Root().Stat(r.Path())
			if serr == nil {
				entry := e.Core.EntryAt(r, st)
				if id, ferr := e.compatFileID(c.UserContext(), entry); ferr == nil {
					fid = id
				}
			}
		}

		thumbURL := ""
		if fid > 0 {
			thumbURL = fmt.Sprintf("%s/index.php/core/preview?fileId=%d&x=64&y=64",
				e.compatOriginOf(c), fid)
		}

		entries = append(entries, compat.SearchEntry(
			hit.Name,
			"/"+hit.Vpath.String(),
			fid,
			thumbURL,
			e.compatOriginOf(c),
		))
	}

	return compat.SearchPage("Recent", entries, -1), true, nil
}

// compatRevokeAppPassword revokes the credential used to issue the request.
func (e *Engine) compatRevokeAppPassword(
	c *fiber.Ctx, user core.UserID,
) (compat.Val, bool, *compat.OCSError) {
	p, ok := c.Locals(middleware.KeyCredential).(middleware.Principal)
	if !ok || (p.Kind != middleware.CredentialBasicApp && p.Kind != middleware.CredentialBearerApp) ||
		p.AppPasswordID == 0 {
		return compat.Val{}, false, compat.Forbidden("an app password is required")
	}
	if err := e.Auth.RevokeAppPassword(c.UserContext(), int64(user), p.AppPasswordID); err != nil {
		return compat.Val{}, false, compat.ServerError("could not revoke credential")
	}
	return compat.Object(), true, nil
}
