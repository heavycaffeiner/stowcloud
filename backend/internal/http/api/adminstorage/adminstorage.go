//go:build linux

// Package adminstorage serves administrator storage accounting.
package adminstorage

import (
	"context"

	"github.com/heavycaffeiner/stowcloud/backend/internal/admin"
	"github.com/heavycaffeiner/stowcloud/backend/internal/db/state"
	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/fs/vfs"
)

type Handler struct {
	Core  *files.Core
	State *state.DB
}
type storageOutput struct{ Body admin.StorageView }

// Get answers the storage used by the database and each share.
func (h *Handler) Get(ctx context.Context, _ *struct{}) (*storageOutput, error) {
	dbBytes, err := h.State.FileBytes()
	if err != nil {
		return nil, err
	}
	roots := h.Core.Shares()
	shares := make([]admin.ShareUsage, 0, len(roots))
	for _, sh := range roots {
		usage := admin.ShareUsage{ID: sh.ID, Label: sh.Name}
		if root, ok := h.Core.ShareRoot(sh.ID); ok {
			if space, err := root.Space(vfs.RootPath()); err == nil {
				usage.Total, usage.Free, usage.Measured = space.Total, space.Available, true
			}
		}
		shares = append(shares, usage)
	}
	return &storageOutput{Body: admin.StorageOf(dbBytes, shares)}, nil
}
