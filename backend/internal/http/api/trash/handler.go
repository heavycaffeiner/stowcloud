//go:build linux

// Package trash contains the authenticated HTTP adapters for the file trash.
// It owns request parsing and wire projection while feature/files owns trash
// policy and storage semantics.
package trash

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	core "github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/handler"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/middleware"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/route"
	num "github.com/heavycaffeiner/stowcloud/backend/internal/platform/number"
	"github.com/heavycaffeiner/stowcloud/backend/internal/storage/vfs"
)

// Deps are the capabilities needed by the authenticated trash routes.
// Identity, path resolution, and error projection remain application policy
// supplied as callbacks; Core is the feature boundary for trash operations.
type Deps struct {
	Core    *core.Core
	Owner   func(*gin.Context) (core.UserID, bool)
	Resolve func(core.UserID, string, acl.Perms) (core.Resolved, error)
	Decode  func(*gin.Context, any) error
	Fail    func(*gin.Context, error)
	Refuse  func(*gin.Context, apierr.Classified)
}

// Handler implements the authenticated trash HTTP routes.
type Handler struct{ d Deps }

// NewHandler constructs a trash transport handler with explicit dependencies.
func NewHandler(d Deps) *Handler { return &Handler{d: d} }

func (h *Handler) owner(c *gin.Context) (core.UserID, bool) {
	if h.d.Owner == nil {
		return 0, false
	}
	return h.d.Owner(c)
}

func (h *Handler) resolve(owner core.UserID, raw string, need acl.Perms) (core.Resolved, error) {
	if h.d.Resolve == nil {
		return core.Resolved{}, core.ErrNotFound
	}
	return h.d.Resolve(owner, raw, need)
}

func (h *Handler) decode(c *gin.Context, into any) error {
	if h.d.Decode != nil {
		return h.d.Decode(c, into)
	}
	return middleware.DecodeJSON(middleware.LimitBody(c.Request.Body, route.BodyJSON), into)
}

func (h *Handler) refuse(c *gin.Context, class apierr.Classified) {
	if h.d.Refuse != nil {
		h.d.Refuse(c, class)
		return
	}
	status, body := apierr.REST(class)
	c.JSON(status, body)
}

func (h *Handler) fail(c *gin.Context, err error) {
	if h.d.Fail != nil {
		h.d.Fail(c, err)
		return
	}
	middleware.SetCause(c, err)
	h.refuse(c, apierr.Classify(err, apierr.VisibilityKnown))
}

// List answers one share's trash, or the flat listing across every share the
// account can reach. Every id is share-qualified as "share:storeid".
func (h *Handler) List(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		h.refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	views := make([]handler.TrashView, 0, 8)
	qualify := func(share core.ShareID, entries []core.TrashEntry) {
		for _, entry := range entries {
			view := handler.TrashOf(entry)
			view.ID = strconv.FormatUint(uint64(share), 10) + ":" + entry.ID
			views = append(views, view)
		}
	}
	if raw := c.Query("path"); raw != "" {
		r, err := h.resolve(owner, raw, acl.Read)
		if err != nil {
			h.fail(c, err)
			return
		}
		entries, err := h.d.Core.TrashList(c.Request.Context(), r)
		if err != nil {
			h.fail(c, err)
			return
		}
		qualify(r.Share(), entries)
		writeJSON(c, http.StatusOK, views)
		return
	}
	for _, root := range h.d.Core.Roots(owner) {
		r, err := h.resolve(owner, "/"+root.Label, acl.Read)
		if err != nil {
			continue
		}
		entries, err := h.d.Core.TrashList(c.Request.Context(), r)
		if err != nil {
			continue
		}
		qualify(r.Share(), entries)
	}
	writeJSON(c, http.StatusOK, views)
}

type trashBatch struct {
	IDs []string `json:"ids"`
}

type trashBatchItem struct {
	Path  string       `json:"path"`
	OK    bool         `json:"ok"`
	Error *apierr.Wire `json:"error,omitempty"`
}

// Restore puts entries back where they came from, reporting each item
// independently. Restore requires Create permission because it adds an entry
// to the live tree.
func (h *Handler) Restore(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		h.refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	var req trashBatch
	if err := h.decode(c, &req); err != nil {
		h.refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	results := make([]trashBatchItem, 0, len(req.IDs))
	for _, raw := range req.IDs {
		item := trashBatchItem{Path: raw}
		r, id, err := h.resolveTrashID(owner, raw, acl.Create)
		if err != nil {
			wire := apierr.WireOf(err, apierr.VisibilityKnown)
			item.Error = &wire
			results = append(results, item)
			continue
		}
		restored, err := h.d.Core.TrashRestore(c.Request.Context(), r, id)
		if err != nil {
			wire := apierr.WireOf(err, apierr.VisibilityKnown)
			item.Error = &wire
			results = append(results, item)
			continue
		}
		item.OK = true
		if vp, err := h.d.Core.VpathFor(owner, r.Share(), restored.Share()); err == nil {
			item.Path = vp.String()
		}
		results = append(results, item)
	}
	writeJSON(c, http.StatusOK, map[string]any{"results": results})
}

// Purge permanently removes named entries, reporting each item independently.
// There is deliberately no empty-all operation: callers must name entries.
func (h *Handler) Purge(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		h.refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	var req trashBatch
	if err := h.decode(c, &req); err != nil {
		h.refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	results := make([]trashBatchItem, 0, len(req.IDs))
	for _, raw := range req.IDs {
		item := trashBatchItem{Path: raw}
		r, id, err := h.resolveTrashID(owner, raw, acl.Delete)
		if err != nil {
			wire := apierr.WireOf(err, apierr.VisibilityKnown)
			item.Error = &wire
			results = append(results, item)
			continue
		}
		if err := h.d.Core.TrashPurge(c.Request.Context(), r, &id); err != nil {
			wire := apierr.WireOf(err, apierr.VisibilityKnown)
			item.Error = &wire
			results = append(results, item)
			continue
		}
		item.OK = true
		results = append(results, item)
	}
	writeJSON(c, http.StatusOK, map[string]any{"results": results})
}

// resolveTrashID accepts only the share-qualified ids emitted by List and
// resolves the share through the caller's own roots. A foreign share and an
// unknown entry both remain not-found.
func (h *Handler) resolveTrashID(owner core.UserID, raw string, need acl.Perms) (core.Resolved, string, error) {
	share, id, ok := strings.Cut(raw, ":")
	if !ok || id == "" {
		return core.Resolved{}, "", apierr.BadRequest("trash.bad_id", "ids")
	}
	n, err := strconv.ParseUint(share, 10, 32)
	if err != nil {
		return core.Resolved{}, "", apierr.BadRequest("trash.bad_id", "ids")
	}
	for _, root := range h.d.Core.Roots(owner) {
		narrowed, err := num.Narrow[uint32](root.Share)
		if err != nil || uint64(narrowed) != n {
			continue
		}
		if _, parseErr := vfs.ParseVpath("/" + root.Label); parseErr != nil {
			continue
		}
		resolved, err := h.resolve(owner, "/"+root.Label, need)
		if err != nil {
			continue
		}
		return resolved, id, nil
	}
	return core.Resolved{}, "", core.ErrNotFound
}

func writeJSON(c *gin.Context, status int, value any) { c.JSON(status, value) }

func (h *Handler) ListHandler(c *gin.Context)    { h.List(c) }
func (h *Handler) RestoreHandler(c *gin.Context) { h.Restore(c) }
func (h *Handler) PurgeHandler(c *gin.Context)   { h.Purge(c) }
