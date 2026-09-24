//go:build linux

package app

import (
	"context"

	core "github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/database/state"
	num "github.com/heavycaffeiner/stowcloud/go/internal/platform/number"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/objstore"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/vfs"
)

func directProvider(root vfs.Root) (objstore.DirectTransferProvider, bool) {
	p, ok := root.(objstore.DirectTransferProvider)
	return p, ok
}

func (e *Engine) directProviderForRow(ctx context.Context, row state.DirectTransferReservation) (objstore.DirectTransferProvider, bool, error) {
	sharePath, err := vfs.ParseSharePath(row.Path)
	if err != nil {
		return nil, false, core.ErrNotFound
	}
	shareID, serr := num.Narrow[uint32](row.Share)
	if serr != nil {
		return nil, false, core.ErrNotFound
	}
	vpath, err := e.Core.VpathFor(core.UserID(row.Owner), core.ShareID(shareID), sharePath)
	if err != nil {
		return nil, false, core.ErrNotFound
	}
	r, err := e.resolve(core.UserID(row.Owner), vpath.String(), acl.Write|acl.Create)
	if err != nil {
		return nil, false, err
	}
	p, ok := directProvider(r.Root())
	return p, ok, nil
}

func (e *Engine) revalidateDirectDestination(ctx context.Context, row state.DirectTransferReservation) error {
	sharePath, err := vfs.ParseSharePath(row.Path)
	if err != nil {
		return core.ErrNotFound
	}
	shareID, serr := num.Narrow[uint32](row.Share)
	if serr != nil {
		return core.ErrNotFound
	}
	vpath, err := e.Core.VpathFor(core.UserID(row.Owner), core.ShareID(shareID), sharePath)
	if err != nil {
		return core.ErrNotFound
	}
	r, err := e.resolve(core.UserID(row.Owner), vpath.String(), acl.Write|acl.Create)
	if err != nil {
		return err
	}
	if gerr := e.guardDavLock(ctx, uint32(r.Share()), r.Path().String(), row.Owner); gerr != nil {
		return gerr
	}
	st, err := r.Root().Stat(r.Path())
	if err == nil {
		if st.Kind.IsDir() {
			return core.ErrUnprocessable
		}
		current, _ := core.FileETag(st)
		if current != row.PriorETag {
			return core.ErrPrecondition
		}
		return nil
	}
	if row.PriorETag != "" {
		return core.ErrPrecondition
	}
	return nil
}
