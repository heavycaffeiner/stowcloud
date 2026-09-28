//go:build linux

// Package adminstorage serves administrator storage accounting.
package adminstorage

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	core "github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/handler"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/humabridge"
	"github.com/heavycaffeiner/stowcloud/backend/internal/storage/vfs"
	"github.com/heavycaffeiner/stowcloud/backend/internal/store/state"
)

type Deps struct {
	Core  *core.Core
	State *state.DB
}
type storageOutput struct{ Body handler.StorageView }

// Register adds the typed administrator storage operation. The path is
// relative to the caller's /api/v1 mount.
func Register(api huma.API, d Deps) {
	huma.Register(api, huma.Operation{
		OperationID: "admin.storage",
		Method:      http.MethodGet,
		Path:        "/admin/storage",
		Summary:     "Get administrator storage usage",
	}, (&handlers{d: d}).getHuma)
}

func (h *handlers) getHuma(ctx context.Context, _ *struct{}) (*storageOutput, error) {
	dbBytes, err := h.d.State.FileBytes()
	if err != nil {
		return nil, humabridge.Failure(ctx, err)
	}
	roots := h.d.Core.Shares()
	shares := make([]handler.ShareUsage, 0, len(roots))
	for _, sh := range roots {
		usage := handler.ShareUsage{ID: sh.ID, Label: sh.Name}
		if root, ok := h.d.Core.ShareRoot(sh.ID); ok {
			if space, err := root.Space(vfs.RootPath()); err == nil {
				usage.Total, usage.Free, usage.Measured = space.Total, space.Available, true
			}
		}
		shares = append(shares, usage)
	}
	return &storageOutput{Body: handler.StorageOf(dbBytes, shares)}, nil
}

type handlers struct{ d Deps }
