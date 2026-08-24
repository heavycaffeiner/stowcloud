//go:build linux

package core

import (
	"context"
	"errors"
	"log/slog"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/journal"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// PublishPart moves an already-complete file from a control name onto its
// destination, durably.
//
// It is CreateFile's sibling for content that is already on disk. CreateFile
// stages what a caller writes through a handle; this one publishes what the
// upload engine has been accumulating in a part file for possibly hours, so
// there is nothing to stage and a copy would be a second full write of a file
// that is already in the right directory.
//
// part must be a control name in the destination's own directory, which is
// what makes the rename atomic and what keeps it out of every listing until
// the moment it lands.
func (c *Core) PublishPart(ctx context.Context, r Resolved, part vfs.SafePath, size uint64) (Entry, error) {
	if err := r.Require(acl.Write | acl.Create); err != nil {
		return Entry{}, err
	}
	if !vfs.IsReservedName(part.Name()) {
		return Entry{}, errf(ErrDenied, "publish from a name that is not a control file")
	}
	if !part.Parent().Equal(r.path.Parent()) {
		return Entry{}, errf(ErrDenied, "publish across directories, which is not one atomic rename")
	}

	// Whether the destination exists decides both the clobber flag and whose
	// mode and ownership the published file ends up with. It is read once,
	// here, because the rename below is what settles the race either way.
	prior, priorErr := r.root.Stat(r.path)
	switch {
	case priorErr == nil:
		// The destination already exists under this name, so the creation table
		// has nothing left to protect: something else wrote it and refusing now
		// would make the name unwritable rather than unmakeable.
	case errors.Is(priorErr, vfs.ErrNotFound):
		if err := requireCreatableLeaf(r.path); err != nil {
			return Entry{}, err
		}
	default:
		return Entry{}, mapVFSErr(priorErr)
	}
	replacing := priorErr == nil

	done, err := r.root.PublishPart(part, r.path, replacing)
	if err != nil {
		return Entry{}, mapVFSErr(err)
	}
	if done.OwnerRestore != nil {
		// EPERM restoring a uid is the ordinary answer for an unprivileged
		// process. The mode is what the neighbours' access actually depends on
		// and that one is fatal inside the helper.
		c.warn("an upload replaced a file whose ownership could not be restored",
			"error", done.OwnerRestore)
	}

	// Everything from here is after the commit point and none of it can undo
	// the rename, so each failure is logged rather than returned.
	c.markDirty(ctx, r.share, r.path)
	c.record(ctx, r, journal.OpUpload)
	if replacing {
		c.chargeQuota(ctx, r.user, deltaOf(size, prior.Size))
	} else {
		c.chargeQuota(ctx, r.user, deltaOf(size, 0))
	}
	return c.buildEntry(r, r.path.Name(), r.path), nil
}

// deltaOf is the signed change in bytes an account's ledger sees, saturating
// rather than wrapping: a size that does not fit the signed width is a number
// no filesystem produced and is charged as nothing.
func deltaOf(now, before uint64) int64 {
	n, nerr := num.Narrow[int64](now)
	b, berr := num.Narrow[int64](before)
	if nerr != nil || berr != nil {
		slog.Warn("an upload's size does not fit the quota ledger; charging nothing",
			slog.Uint64("published", now), slog.Uint64("replaced", before))
		return 0
	}
	return n - b
}
