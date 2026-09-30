//go:build linux

// Package trash contains the authenticated HTTP adapters for the file trash.
// It owns request parsing and wire projection while the files package owns trash
// policy and storage semantics.
package trash

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/fs/vfs"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/handler"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/humabridge"
	num "github.com/heavycaffeiner/stowcloud/backend/internal/platform/number"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/shares/acl"
)

// Deps are the capabilities needed by the authenticated trash routes.
// Identity, path resolution, and error projection remain application policy
// supplied as callbacks; Core is the feature boundary for trash operations.
type Deps struct {
	Core    *files.Core
	Owner   func(*gin.Context) (files.UserID, bool)
	Resolve func(files.UserID, string, acl.Perms) (files.Resolved, error)
	Errors  *apierr.Classifier
}

// Handler implements the authenticated trash HTTP routes.
type Handler struct{ d Deps }
type listInput struct {
	Path string `query:"path"`
}

type listOutput struct {
	Body []handler.TrashView
}

type batchBody struct {
	IDs []string `json:"ids"`
}

type batchInput struct {
	Body batchBody
}

type batchOutput struct {
	Body struct {
		Results []trashBatchItem `json:"results"`
	}
}

// Register mounts the three conventional trash operations below the API prefix.
func Register(api huma.API, d Deps) {
	h := &Handler{d: d}
	huma.Register[listInput, listOutput](api, huma.Operation{
		OperationID: "trash.list", Method: http.MethodGet, Path: "/trash",
	}, h.listHuma)
	huma.Register[batchInput, batchOutput](api, huma.Operation{
		OperationID: "trash.restore", Method: http.MethodPost, Path: "/trash/restore",
	}, h.restoreHuma)
	huma.Register[batchInput, batchOutput](api, huma.Operation{
		OperationID: "trash.purge", Method: http.MethodPost, Path: "/trash/purge",
	}, h.purgeHuma)
}
func (h *Handler) humaOwner(ctx context.Context) (files.UserID, error) {
	c := humabridge.Gin(ctx)
	owner, ok := h.owner(c)
	if !ok {
		return 0, humabridge.Refusal(apierr.Classified{Class: apierr.AuthRequired})
	}
	return owner, nil
}

func (h *Handler) listHuma(ctx context.Context, in *listInput) (*listOutput, error) {
	owner, err := h.humaOwner(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]handler.TrashView, 0, 8)
	qualify := func(share files.ShareID, entries []files.TrashEntry) {
		for _, entry := range entries {
			view := handler.TrashOf(entry)
			view.ID = strconv.FormatUint(uint64(share), 10) + ":" + entry.ID
			views = append(views, view)
		}
	}
	if in.Path != "" {
		r, err := h.resolve(owner, in.Path, acl.Read)
		if err != nil {
			return nil, humabridge.Failure(ctx, err)
		}
		entries, err := h.d.Core.TrashList(ctx, r)
		if err != nil {
			return nil, humabridge.Failure(ctx, err)
		}
		qualify(r.Share(), entries)
		return &listOutput{Body: views}, nil
	}
	for _, root := range h.d.Core.Roots(owner) {
		r, err := h.resolve(owner, "/"+root.Label, acl.Read)
		if err != nil {
			continue
		}
		entries, err := h.d.Core.TrashList(ctx, r)
		if err != nil {
			continue
		}
		qualify(r.Share(), entries)
	}
	return &listOutput{Body: views}, nil
}

func (h *Handler) batchHuma(ctx context.Context, ids []string, need acl.Perms, restore bool) (*batchOutput, error) {
	owner, err := h.humaOwner(ctx)
	if err != nil {
		return nil, err
	}
	results := make([]trashBatchItem, 0, len(ids))
	for _, raw := range ids {
		item := trashBatchItem{Path: raw}
		r, id, err := h.resolveTrashID(owner, raw, need)
		if err != nil {
			wire := h.d.Errors.WireOf(err, apierr.VisibilityKnown)
			item.Error = &wire
			results = append(results, item)
			continue
		}
		if restore {
			restored, rerr := h.d.Core.TrashRestore(ctx, r, id)
			if rerr != nil {
				wire := h.d.Errors.WireOf(rerr, apierr.VisibilityKnown)
				item.Error = &wire
				results = append(results, item)
				continue
			}
			item.OK = true
			if vp, verr := h.d.Core.VpathFor(owner, r.Share(), restored.Share()); verr == nil {
				item.Path = vp.String()
			}
		} else if perr := h.d.Core.TrashPurge(ctx, r, &id); perr != nil {
			wire := h.d.Errors.WireOf(perr, apierr.VisibilityKnown)
			item.Error = &wire
			results = append(results, item)
			continue
		} else {
			item.OK = true
		}
		results = append(results, item)
	}
	out := &batchOutput{}
	out.Body.Results = results
	return out, nil
}

func (h *Handler) restoreHuma(ctx context.Context, in *batchInput) (*batchOutput, error) {
	return h.batchHuma(ctx, in.Body.IDs, acl.Create, true)
}

func (h *Handler) purgeHuma(ctx context.Context, in *batchInput) (*batchOutput, error) {
	return h.batchHuma(ctx, in.Body.IDs, acl.Delete, false)
}

func (h *Handler) owner(c *gin.Context) (files.UserID, bool) {
	if h.d.Owner == nil {
		return 0, false
	}
	return h.d.Owner(c)
}

func (h *Handler) resolve(owner files.UserID, raw string, need acl.Perms) (files.Resolved, error) {
	if h.d.Resolve == nil {
		return files.Resolved{}, files.ErrNotFound
	}
	return h.d.Resolve(owner, raw, need)
}

type trashBatchItem struct {
	Path  string       `json:"path"`
	OK    bool         `json:"ok"`
	Error *apierr.Wire `json:"error,omitempty"`
}

// resolveTrashID accepts only the share-qualified ids emitted by List and
// resolves the share through the caller's own roots. A foreign share and an
// unknown entry both remain not-found.
func (h *Handler) resolveTrashID(owner files.UserID, raw string, need acl.Perms) (files.Resolved, string, error) {
	share, id, ok := strings.Cut(raw, ":")
	if !ok || id == "" {
		return files.Resolved{}, "", apierr.BadRequest("trash.bad_id", "ids")
	}
	n, err := strconv.ParseUint(share, 10, 32)
	if err != nil {
		return files.Resolved{}, "", apierr.BadRequest("trash.bad_id", "ids")
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
	return files.Resolved{}, "", files.ErrNotFound
}
