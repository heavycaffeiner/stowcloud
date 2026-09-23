//go:build linux

// The trash family: what a person deleted and can still get back.
//
// A restore puts an entry where it was deleted from, which the entry records.
// The request never says where: a caller choosing the destination could move a
// file anywhere it can write, using a delete as the first half of the move.
package app

import (
	"net/http"
	"strconv"

	"strings"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/vfs"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
)

// trashList answers one share's trash, or the flat listing across every
// share the account can reach.
//
// Every id on the wire is share-qualified ("share:storeid"). The store id
// alone names nothing outside its share, and a restore or a purge built from
// a bare id would have to guess which share to look in.
func (e *Engine) trashList(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	views := make([]handler.TrashView, 0, 8)
	qualify := func(share core.ShareID, entries []core.TrashEntry) {
		for _, entry := range entries {
			v := handler.TrashOf(entry)
			v.ID = strconv.FormatUint(uint64(share), 10) + ":" + entry.ID
			views = append(views, v)
		}
	}
	if raw := c.Query("path"); raw != "" {
		r, err := e.resolve(owner, raw, acl.Read)
		if err != nil {
			fail(c, err)
			return
		}
		entries, err := e.Core.TrashList(c.Request.Context(), r)
		if err != nil {
			fail(c, err)
			return
		}
		qualify(r.Share(), entries)
		writeJSON(c, http.StatusOK, views)
		return
	}
	for _, root := range e.Core.Roots(owner) {
		r, err := e.resolve(owner, "/"+root.Label, acl.Read)
		if err != nil {
			continue
		}
		entries, err := e.Core.TrashList(c.Request.Context(), r)
		if err != nil {
			continue
		}
		qualify(r.Share(), entries)
	}
	writeJSON(c, http.StatusOK, views)
}

// trashBatch is the wire body of the restore and purge calls: one entry id
// per item, each share-qualified as the listing produced it.
type trashBatch struct {
	IDs []string `json:"ids"`
}

// trashBatchItem is one item's outcome. The client renders per-item results,
// so a refusal on one entry still reports the others.
type trashBatchItem struct {
	Path  string       `json:"path"`
	OK    bool         `json:"ok"`
	Error *apierr.Wire `json:"error,omitempty"`
}

// trashRestore puts entries back where they came from, per item.
//
// Create is the permission, because a restore adds a file to the tree. Delete
// is not enough: an account that may remove things is not thereby allowed to
// put them back somewhere.
func (e *Engine) trashRestore(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	var req trashBatch
	if err := decodeBody(c, &req); err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	results := make([]trashBatchItem, 0, len(req.IDs))
	for _, raw := range req.IDs {
		item := trashBatchItem{Path: raw}
		r, id, err := e.resolveTrashID(owner, raw, acl.Create)
		if err != nil {
			w := apierr.WireOf(err, apierr.VisibilityKnown)
			item.Error = &w
			results = append(results, item)
			continue
		}
		restored, ferr := e.Core.TrashRestore(c.Request.Context(), r, id)
		if ferr != nil {
			w := apierr.WireOf(ferr, apierr.VisibilityKnown)
			item.Error = &w
			results = append(results, item)
			continue
		}
		item.OK = true
		if vp, verr := e.Core.VpathFor(owner, r.Share(), restored.Share()); verr == nil {
			item.Path = vp.String()
		}
		results = append(results, item)
	}
	writeJSON(c, http.StatusOK, map[string]any{"results": results})
}

// trashPurge removes entries permanently, per item.
//
// A purge names its entries; there is no "everything" request, because a
// client that meant to empty the trash can name every entry it was just
// shown, and a form that emptied on a missing field would be a data-loss
// bug wearing a convenience label.
func (e *Engine) trashPurge(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	var req trashBatch
	if err := decodeBody(c, &req); err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	results := make([]trashBatchItem, 0, len(req.IDs))
	for _, raw := range req.IDs {
		item := trashBatchItem{Path: raw}
		r, id, err := e.resolveTrashID(owner, raw, acl.Delete)
		if err != nil {
			w := apierr.WireOf(err, apierr.VisibilityKnown)
			item.Error = &w
			results = append(results, item)
			continue
		}
		if err := e.Core.TrashPurge(c.Request.Context(), r, &id); err != nil {
			w := apierr.WireOf(err, apierr.VisibilityKnown)
			item.Error = &w
			results = append(results, item)
			continue
		}
		item.OK = true
		results = append(results, item)
	}
	writeJSON(c, http.StatusOK, map[string]any{"results": results})
}

// resolveTrashID splits a share-qualified id and resolves the share through
// the caller's own roots. A share the caller holds no grant over answers
// not-found, identical to an id that never existed: the existence rule holds
// for ids exactly as it does for paths.
func (e *Engine) resolveTrashID(
	owner core.UserID, raw string, need acl.Perms, allowedShares ...string,
) (core.Resolved, string, error) {
	share, id, ok := strings.Cut(raw, ":")
	if !ok || id == "" {
		return core.Resolved{}, "", apierr.BadRequest("trash.bad_id", "ids")
	}
	n, perr := strconv.ParseUint(share, 10, 32)
	if perr != nil {
		return core.Resolved{}, "", apierr.BadRequest("trash.bad_id", "ids")
	}

	for _, root := range e.Core.Roots(owner) {
		if len(allowedShares) > 0 {
			allowed := false
			for _, s := range allowedShares {
				if s == root.Label {
					allowed = true
					break
				}
			}
			if !allowed {
				continue
			}
		}
		narrowed, nerr := num.Narrow[uint32](root.Share)
		if nerr != nil || uint64(narrowed) != n {
			continue
		}
		vp, verr := vfs.ParseVpath("/" + root.Label)
		if verr != nil {
			continue
		}
		resolved, rerr := e.Core.Resolve(owner, vp, need)
		if rerr != nil {
			continue
		}
		return resolved, id, nil
	}
	return core.Resolved{}, "", core.ErrNotFound
}
