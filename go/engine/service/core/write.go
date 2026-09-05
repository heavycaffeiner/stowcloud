//go:build linux

package core

import (
	"context"
	"fmt"
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/journal"
	"io"
	"math"
)

// Every mutation in this file runs the same six steps in the same order:
// take a Resolved (the gate already ran), require the bits, check the
// precondition, act through one named VFS operation, invalidate, record one
// journal row. The last two are after the commit point; their failures are
// logged and never returned, because a committed write reported as failed
// tells a client its data is gone when it is on disk.

// precondition is the concurrent-edit gate, and it refuses every validator
// it is given.
//
// The chain: every file ETag this system mints is weak, because statx has no
// change-version field and a metadata token cannot honestly claim strong;
// If-Match requires strong comparison; a weak token cannot pass one. So a
// supplied validator always fails, whatever its value. The current token
// rides in the refusal so a conflict screen can show what the file is now
// without a second round trip, and the explicit unconditional retry (the
// caller dropping its validator) is the only way past. The server cannot
// prove nothing changed, so it refuses to pretend and the human decides.
func precondition(ifMatch *Token, st vfs.Stat) error {
	if ifMatch == nil {
		return nil
	}
	cur, _ := FileETag(st)
	return &PreconditionError{Current: cur}
}

// record writes one journal row for a mutation that already committed.
//
// Best-effort by contract: the journal feeds the recent-files surface, and
// nothing may read a missing row as evidence that nothing happened. A nil
// journal is a silent no-op, since a deployment without one costs the recent
// listing and nothing else.
func (c *Core) record(ctx context.Context, r Resolved, op journal.Op) {
	if c.journal == nil {
		return
	}
	// A user id past the journal's account column skips the row rather than
	// truncating into some other account's history.
	account, err := num.Narrow[uint32](int64(r.user))
	if err != nil {
		c.warn("a write was not journalled: the account id does not fit the journal",
			"user", int64(r.user), "error", err)
		return
	}
	if err := c.journal.Record(ctx, journal.Event{
		Account: account,
		Share:   r.share,
		Path:    r.path.Share(),
		Op:      op,
	}); err != nil {
		c.warn("journalling a write failed; the write itself committed", "error", err)
	}
}

// Mkdir creates one directory at the resolved path.
func (c *Core) Mkdir(ctx context.Context, r Resolved) (Entry, error) {
	if err := r.Require(acl.Create); err != nil {
		return Entry{}, err
	}
	if err := requireCreatableLeaf(r.path); err != nil {
		return Entry{}, mapVFSErr(err)
	}
	if err := r.root.Mkdir(r.path); err != nil {
		return Entry{}, mapVFSErr(err)
	}

	c.markDirty(ctx, r.share, r.path)
	c.record(ctx, r, journal.OpUpload)
	return c.buildEntry(r, r.path.Name(), r.path), nil
}

// WriteStream writes a file's whole content from a reader.
//
// It exists so a caller can write a file without naming a filesystem type.
// CreateFile takes the mode and hands back an open descriptor, which suits the
// assembly tier that already sees every layer; a protocol handler may not
// import that tier, and a method that had to would be reaching past the domain
// to reach the disk.
//
// The mode is the share's own rather than a constant, so a file arriving over
// one protocol is reachable on the same terms as one arriving over another. A
// per-caller constant is how two routes end up creating files that differ only
// by which route made them.
func (c *Core) WriteStream(ctx context.Context, r Resolved, src io.Reader, ifMatch *Token) (Entry, error) {
	opts := vfs.DurableOpts{Mode: r.root.Policy().ModeFile}
	return c.CreateFile(ctx, r, opts, ifMatch, func(f *vfs.File) error {
		// Positional, because the descriptor carries no shared offset: the
		// running total is the only cursor there is.
		var off int64
		_, err := io.Copy(writerAt{f: f, off: &off}, src)
		return err
	})
}

// writerAt turns positional writes into a stream.
type writerAt struct {
	f   *vfs.File
	off *int64
}

func (w writerAt) Write(p []byte) (int, error) {
	n, err := w.f.WriteAt(p, *w.off)
	*w.off += int64(n)
	return n, err
}

// CreateFile writes a file's whole content, whether the name is new or being
// replaced. Both go through the durable write: stage under a reserved name,
// sync, publish by an atomic rename, sync the parent. A truncate-and-write
// replace is neither atomic nor mode-preserving, which is why there is no
// second write path.
//
// Both bits are demanded rather than one chosen by whether the target
// exists: the two-stat answer would race, and a caller allowed to replace
// but not create getting a different refusal depending on a concurrent
// delete is a permission model nobody can reason about.
func (c *Core) CreateFile(
	ctx context.Context, r Resolved, mode vfs.DurableOpts,
	ifMatch *Token, write func(*vfs.File) error,
) (Entry, error) {
	if err := r.Require(acl.Write | acl.Create); err != nil {
		return Entry{}, err
	}

	// One stat, and the write below is what actually settles the race with
	// it. WriteDurable's own clobber and replace semantics are the atomic
	// truth; the precondition is advisory ordering on top, as it is in
	// every If-Match implementation over a real filesystem.
	st, serr := r.root.Stat(r.path)
	switch {
	case serr == nil:
		if perr := precondition(ifMatch, st); perr != nil {
			return Entry{}, perr
		}
	case mapVFSErr(serr) == ErrNotFound:
		if ifMatch != nil {
			// A validator against a missing file failed by definition, and
			// the empty current token says the file is gone rather than
			// changed.
			return Entry{}, &PreconditionError{Current: ""}
		}
		// The name is about to be minted, so the creation table applies.
		if cerr := requireCreatableLeaf(r.path); cerr != nil {
			return Entry{}, mapVFSErr(cerr)
		}
	default:
		return Entry{}, mapVFSErr(serr)
	}

	if _, err := r.root.WriteDurable(r.path, mode, write); err != nil {
		return Entry{}, mapVFSErr(err)
	}

	if newSt, err := r.root.Stat(r.path); err == nil {
		if newSize, nerr := num.Narrow[int64](newSt.Size); nerr == nil {
			delta := newSize
			if serr == nil {
				if oldSize, oerr := num.Narrow[int64](st.Size); oerr == nil {
					delta = newSize - oldSize
				}
			}
			c.chargeQuota(ctx, r.user, delta)
		}
	}
	c.markDirty(ctx, r.share, r.path)
	c.record(ctx, r, journal.OpUpload)
	return c.buildEntry(r, r.path.Name(), r.path), nil
}

// Rename changes a name within its own directory. Crossing a directory is
// Move, and the grant model distinguishes the two on purpose.
func (c *Core) Rename(ctx context.Context, r Resolved, newName string, ifMatch *Token) (Entry, error) {
	if err := r.Require(acl.Rename); err != nil {
		return Entry{}, err
	}
	st, err := r.root.Stat(r.path)
	if err != nil {
		return Entry{}, mapVFSErr(err)
	}
	if perr := precondition(ifMatch, st); perr != nil {
		return Entry{}, perr
	}

	// Join, not JoinExisting: the new name is being minted, so a name no
	// Windows or SMB client could open is refused here.
	dest, err := r.path.Parent().Join(newName)
	if err != nil {
		return Entry{}, err
	}
	// noReplace, so a taken destination is refused atomically by the kernel
	// rather than by a check that races.
	if err := r.root.Rename(r.path, dest, true); err != nil {
		return Entry{}, mapVFSErr(err)
	}

	// Both ends: the name left one listing and joined another.
	c.markDirty(ctx, r.share, r.path)
	c.markDirty(ctx, r.share, dest)

	moved := Resolved{user: r.user, share: r.share, root: r.root, path: dest, perms: r.perms}
	// OpMove, not a rename op of its own: to the recent-files surface a
	// rename is the file moving, and one op keeps the vocabulary small.
	c.record(ctx, moved, journal.OpMove)
	return c.buildEntry(moved, dest.Name(), dest), nil
}

// Delete removes an entry, through the trash when the share has one.
//
// permanent is the caller spelling out that the trash is to be bypassed. A
// share without trash deletes permanently either way; the UI says so before
// the action, and the core does not soften it.
func (c *Core) Delete(ctx context.Context, r Resolved, permanent bool) error {
	if err := r.Require(acl.Delete); err != nil {
		return err
	}
	st, err := r.root.Stat(r.path)
	if err != nil {
		return mapVFSErr(err)
	}

	if !permanent {
		if def, ok := c.Share(r.share); ok && def.TrashEnabled {
			return c.trashMove(ctx, r, st)
		}
	}
	return c.deleteResolved(ctx, r, st, true)
}

// deleteResolved is the permanent delete, with the ledger credit.
//
// charge false is for the callers that account the bytes themselves: the
// cross-device leg of a move deletes a source whose bytes were already
// charged at the destination copy, and crediting here would count the move
// as a shrink.
func (c *Core) deleteResolved(ctx context.Context, r Resolved, st vfs.Stat, charge bool) error {
	var freed uint64
	if st.Kind.IsDir() {
		// Read the recursive size while the tree still exists: it is the
		// only source that matches what the disk held, and the delete
		// destroys it. A failure here fails the delete before any child is
		// touched, because deleting first and guessing the credit after
		// would corrupt the ledger.
		agg, err := c.Aggregate(ctx, r.share, r.path)
		if err != nil {
			return err
		}
		freed = agg.RSize
		if err := c.deleteRecursive(ctx, r); err != nil {
			return err
		}
	} else {
		freed = st.Size
		if err := r.root.Unlink(r.path); err != nil {
			return mapVFSErr(err)
		}
	}

	if charge && freed > 0 {
		c.chargeQuota(ctx, r.user, int64Minus(freed))
	}
	c.markDirty(ctx, r.share, r.path)
	return nil
}

// deleteRecursive walks top-down, removing children before their parent.
//
// The skip rules match the read side: an unjoinable name or a child that
// vanished under the walk is skipped rather than fatal. The Rmdir at the end
// is the backstop, since a directory still holding something the walk could
// not remove fails there with ErrNotEmpty instead of being left half-gone
// and reported deleted.
func (c *Core) deleteRecursive(ctx context.Context, r Resolved) error {
	entries, err := r.root.ReadDir(r.path, vfs.HideReserved)
	if err != nil {
		return mapVFSErr(err)
	}
	for _, e := range entries {
		child, jerr := r.path.JoinExisting(e.Name)
		if jerr != nil {
			continue
		}
		st, serr := r.root.Stat(child)
		if serr != nil {
			continue
		}
		under, uerr := c.ResolveUnder(r, child, acl.Delete)
		if uerr != nil {
			return uerr
		}
		if st.Kind.IsDir() {
			if rerr := c.deleteRecursive(ctx, under); rerr != nil {
				return rerr
			}
			continue
		}
		if uerr := r.root.Unlink(child); uerr != nil {
			return mapVFSErr(uerr)
		}
	}
	if err := r.root.Rmdir(r.path); err != nil {
		return mapVFSErr(err)
	}
	return nil
}

// Stat is the one single-path read, which is why it sits beside the
// mutations and shares their entry builder. A single named path is the
// bounded case where minting a stable id is worth doing, which is what a
// share-link target needs.
//
// A path that is not there is the missing-file answer, not the skeleton row
// the listing builder falls back to. The skeleton exists so one racing delete
// does not fail a whole page; here the one path is the whole request, and a
// zero-size, no-etag row describing a file that does not exist is a row a
// client renders.
func (c *Core) Stat(ctx context.Context, r Resolved) (Entry, error) {
	if err := r.Require(acl.Read); err != nil {
		return Entry{}, err
	}
	if _, err := r.root.Stat(r.path); err != nil {
		return Entry{}, mapVFSErr(err)
	}
	return c.buildEntry(r, r.path.Name(), r.path), nil
}

// PublishPart is CreateFile's sibling for content already on disk: the
// rename half alone.
//
// The upload engine has been accumulating a part file, possibly for hours,
// already synced and already in the right directory. Staging it again
// through the durable write would be a second full write of a finished file,
// and folding the two would instead grow CreateFile a flag that skips
// durability, which is a footgun with a name.
func (c *Core) PublishPart(
	ctx context.Context, r Resolved, part vfs.SafePath, size uint64, ifMatch ...string,
) (Entry, error) {
	if err := r.Require(acl.Write | acl.Create); err != nil {
		return Entry{}, err
	}
	// Only the upload engine can have minted a reserved name, since
	// JoinControl is the only producer and no parser taking client input
	// reaches it. This check is the proof the source is ours.
	if !vfs.IsReservedName(part.Name()) {
		return Entry{}, errf(ErrDenied, "publish from a name that is not a control file")
	}
	// One parent for both paths is what makes the rename a single atomic
	// step. The caller is refused on it rather than trusted.
	if !part.Parent().Equal(r.path.Parent()) {
		return Entry{}, errf(ErrDenied, "publish a part from another directory")
	}

	// One prior stat, deciding both the clobber flag and whose mode and
	// ownership the published file keeps. Read exactly once because the
	// rename below settles the race either way.
	prior, serr := r.root.Stat(r.path)
	replacing := serr == nil
	if len(ifMatch) > 0 && ifMatch[0] != "" {
		if !replacing {
			return Entry{}, fmt.Errorf("%w: nothing is at the destination", ErrPrecondition)
		}
		cur, _ := FileETag(prior)
		return Entry{}, fmt.Errorf("%w: the destination's current token is %s", ErrPrecondition, cur)
	}
	switch {
	case replacing:
		// No creation-table check: something else already wrote this name,
		// and refusing now would make it unwritable rather than unmakeable.
	case mapVFSErr(serr) == ErrNotFound:
		if cerr := requireCreatableLeaf(r.path); cerr != nil {
			return Entry{}, mapVFSErr(cerr)
		}
	default:
		return Entry{}, mapVFSErr(serr)
	}

	done, err := r.root.PublishPart(part, r.path, replacing)
	if err != nil {
		return Entry{}, mapVFSErr(err)
	}
	if done.OwnerRestore != nil {
		// EPERM is the ordinary answer for an unprivileged process, so this
		// is a warning. A mode that could not be restored already failed
		// inside the VFS helper, because the mode is what the neighbours'
		// access depends on.
		c.warn("the replaced file's ownership could not be restored",
			"path", r.path.String(), "error", done.OwnerRestore)
	}

	// Invalidation first, so no reader caches the stale aggregate while the
	// bookkeeping runs. The journal row precedes the ledger because the row
	// describes the file and the ledger only counts bytes.
	c.markDirty(ctx, r.share, r.path)
	c.record(ctx, r, journal.OpUpload)
	if replacing {
		c.chargeQuota(ctx, r.user, deltaOf(size, prior.Size))
	} else {
		c.chargeQuota(ctx, r.user, deltaOf(size, 0))
	}
	return c.buildEntry(r, r.path.Name(), r.path), nil
}

// deltaOf is the signed change the ledger sees, not the gross size.
//
// A size that does not fit the signed width is a number no filesystem
// produced, and it charges nothing rather than wrapping the ledger by
// petabytes. Saturating to zero, not to an extreme: garbage earns no charge
// in either direction.
func deltaOf(now, before uint64) int64 {
	a, err := num.Narrow[int64](now)
	if err != nil {
		return 0
	}
	b, err := num.Narrow[int64](before)
	if err != nil {
		return 0
	}
	return a - b
}

// int64Minus negates a freed size for the ledger, saturating at the most
// negative value that fits: a corrupt or absurd size is clamped rather than
// wrapped, so it can never turn a credit into a huge debit.
func int64Minus(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MinInt64
	}
	return -int64(v)
}
