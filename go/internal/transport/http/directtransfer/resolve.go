//go:build linux

package directtransfer

import (
	"context"

	core "github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/database/state"
	num "github.com/heavycaffeiner/stowcloud/go/internal/platform/number"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/objstore"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/vfs"
)

// Provider returns the direct-transfer provider implemented by a share root.
func Provider(root vfs.Root) (objstore.DirectTransferProvider, bool) {
	provider, ok := root.(objstore.DirectTransferProvider)
	return provider, ok
}

// ProviderForRow resolves a persisted destination with the same write gate
// used when the transfer was created, then returns its direct provider.
func ProviderForRow(
	coreSvc *core.Core,
	resolve func(core.UserID, string, acl.Perms) (core.Resolved, error),
) func(context.Context, state.DirectTransferReservation) (objstore.DirectTransferProvider, bool, error) {
	return func(_ context.Context, row state.DirectTransferReservation) (objstore.DirectTransferProvider, bool, error) {
		sharePath, err := vfs.ParseSharePath(row.Path)
		if err != nil {
			return nil, false, core.ErrNotFound
		}
		shareID, err := num.Narrow[uint32](row.Share)
		if err != nil {
			return nil, false, core.ErrNotFound
		}
		vpath, err := coreSvc.VpathFor(core.UserID(row.Owner), core.ShareID(shareID), sharePath)
		if err != nil {
			return nil, false, core.ErrNotFound
		}
		r, err := resolve(core.UserID(row.Owner), vpath.String(), acl.Write|acl.Create)
		if err != nil {
			return nil, false, err
		}
		provider, ok := Provider(r.Root())
		return provider, ok, nil
	}
}

// RevalidateDestination rechecks the destination lock, type and validator just
// before provider completion. The destination can change while parts upload.
func RevalidateDestination(
	coreSvc *core.Core,
	resolve func(core.UserID, string, acl.Perms) (core.Resolved, error),
	guard func(context.Context, uint32, string, int64) error,
) func(context.Context, state.DirectTransferReservation) error {
	return func(ctx context.Context, row state.DirectTransferReservation) error {
		sharePath, err := vfs.ParseSharePath(row.Path)
		if err != nil {
			return core.ErrNotFound
		}
		shareID, err := num.Narrow[uint32](row.Share)
		if err != nil {
			return core.ErrNotFound
		}
		vpath, err := coreSvc.VpathFor(core.UserID(row.Owner), core.ShareID(shareID), sharePath)
		if err != nil {
			return core.ErrNotFound
		}
		r, err := resolve(core.UserID(row.Owner), vpath.String(), acl.Write|acl.Create)
		if err != nil {
			return err
		}
		if guard != nil {
			if guardErr := guard(ctx, uint32(r.Share()), r.Path().String(), row.Owner); guardErr != nil {
				return guardErr
			}
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
}
