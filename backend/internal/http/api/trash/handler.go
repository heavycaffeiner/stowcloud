//go:build linux

// Package trash contains the authenticated HTTP adapters for the file trash.
// It owns request parsing and wire projection while the files package owns trash
// policy and storage semantics.
package trash

import (
	"context"
	"strconv"
	"strings"

	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/fs/vfs"
	num "github.com/heavycaffeiner/stowcloud/backend/internal/platform/number"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/shares/acl"
)

// Handler serves the caller's trash. Core owns the trash semantics; Errors
// renders the per-item outcome of a batch.
type Handler struct {
	Core    *files.Core
	Resolve func(files.UserID, string, acl.Perms) (files.Resolved, error)
	Errors  *apierr.Classifier
}

type listInput struct {
	Path string `query:"path"`
}

type listOutput struct {
	Body []files.TrashView
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

// List answers the trash under a path, or under every root.
func (h *Handler) List(ctx context.Context, in *listInput) (*listOutput, error) {
	owner, err := files.OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]files.TrashView, 0, 8)
	qualify := func(share files.ShareID, entries []files.TrashEntry) {
		for _, entry := range entries {
			view := files.TrashOf(entry)
			view.ID = strconv.FormatUint(uint64(share), 10) + ":" + entry.ID
			views = append(views, view)
		}
	}
	if in.Path != "" {
		r, err := h.resolve(owner, in.Path, acl.Read)
		if err != nil {
			return nil, err
		}
		entries, err := h.Core.TrashList(ctx, r)
		if err != nil {
			return nil, err
		}
		qualify(r.Share(), entries)
		return &listOutput{Body: views}, nil
	}
	for _, root := range h.Core.Roots(owner) {
		r, err := h.resolve(owner, "/"+root.Label, acl.Read)
		if err != nil {
			continue
		}
		entries, err := h.Core.TrashList(ctx, r)
		if err != nil {
			continue
		}
		qualify(r.Share(), entries)
	}
	return &listOutput{Body: views}, nil
}

func (h *Handler) batch(ctx context.Context, ids []string, need acl.Perms, restore bool) (*batchOutput, error) {
	owner, err := files.OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	results := make([]trashBatchItem, 0, len(ids))
	for _, raw := range ids {
		item := trashBatchItem{Path: raw}
		r, id, err := h.resolveTrashID(owner, raw, need)
		if err != nil {
			wire := h.Errors.WireOf(err, apierr.VisibilityKnown)
			item.Error = &wire
			results = append(results, item)
			continue
		}
		if restore {
			restored, rerr := h.Core.TrashRestore(ctx, r, id)
			if rerr != nil {
				wire := h.Errors.WireOf(rerr, apierr.VisibilityKnown)
				item.Error = &wire
				results = append(results, item)
				continue
			}
			item.OK = true
			if vp, verr := h.Core.VpathFor(owner, r.Share(), restored.Share()); verr == nil {
				item.Path = vp.String()
			}
		} else if perr := h.Core.TrashPurge(ctx, r, &id); perr != nil {
			wire := h.Errors.WireOf(perr, apierr.VisibilityKnown)
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

// Restore puts trashed entries back.
func (h *Handler) Restore(ctx context.Context, in *batchInput) (*batchOutput, error) {
	return h.batch(ctx, in.Body.IDs, acl.Create, true)
}

// Purge deletes trashed entries for good.
func (h *Handler) Purge(ctx context.Context, in *batchInput) (*batchOutput, error) {
	return h.batch(ctx, in.Body.IDs, acl.Delete, false)
}

func (h *Handler) resolve(owner files.UserID, raw string, need acl.Perms) (files.Resolved, error) {
	if h.Resolve == nil {
		return files.Resolved{}, files.ErrNotFound
	}
	return h.Resolve(owner, raw, need)
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
	for _, root := range h.Core.Roots(owner) {
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
