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

// The mutation primitives, every one of which is: resolve, check permissions,
// check the precondition, act through the named VFS operation, invalidate,
// then record one journal row. Nothing here parses a path, and nothing here
// touches the raw rename syscall, which internal/vfs owns.

// Mkdir creates one directory.
func (c *Core) Mkdir(ctx context.Context, r Resolved) (Entry, error) {
	if err := r.Require(acl.Create); err != nil {
		return Entry{}, err
	}
	if err := requireCreatableLeaf(r.path); err != nil {
		return Entry{}, err
	}
	if err := r.root.Mkdir(r.path); err != nil {
		return Entry{}, mapVFSErr(err)
	}
	c.markDirty(ctx, r.share, r.path)
	c.record(ctx, r, journal.OpUpload)
	return c.buildEntry(r, r.path.Name(), r.path), nil
}

// CreateFile publishes new content under p. It is the upload finalization
// path and the content-replacement path, both of which go through
// WriteDurable: a truncate-and-write replace is neither atomic nor
// mode-preserving, which is principle 3 broken.
//
// ifMatch is the caller's validator. A weak precondition is refused here
// rather than presented as proof; an explicit unconditional retry is the one
// way past it.
func (c *Core) CreateFile(
	ctx context.Context, r Resolved, mode vfs.DurableOpts, ifMatch *Token, write func(*vfs.File) error,
) (Entry, error) {
	if err := r.Require(acl.Write | acl.Create); err != nil {
		return Entry{}, err
	}

	st, statErr := r.root.Stat(r.path)
	switch {
	case statErr == nil:
		if err := precondition(ifMatch, st); err != nil {
			return Entry{}, err
		}
	case errors.Is(statErr, vfs.ErrNotFound):
		if ifMatch != nil {
			return Entry{}, &PreconditionError{Current: ""}
		}
		if err := requireCreatableLeaf(r.path); err != nil {
			return Entry{}, err
		}
	default:
		return Entry{}, mapVFSErr(statErr)
	}

	if _, err := r.root.WriteDurable(r.path, mode, write); err != nil {
		return Entry{}, mapVFSErr(err)
	}
	c.markDirty(ctx, r.share, r.path)
	c.record(ctx, r, journal.OpUpload)
	return c.buildEntry(r, r.path.Name(), r.path), nil
}

// Rename changes the name within the same directory. It goes through
// ShareRoot.Rename, never a raw syscall.
func (c *Core) Rename(ctx context.Context, r Resolved, newName string, ifMatch *Token) (Entry, error) {
	if err := r.Require(acl.Rename); err != nil {
		return Entry{}, err
	}
	st, err := r.root.Stat(r.path)
	if err != nil {
		return Entry{}, mapVFSErr(err)
	}
	if err := precondition(ifMatch, st); err != nil {
		return Entry{}, err
	}

	dest, jerr := r.path.Parent().Join(newName)
	if jerr != nil {
		return Entry{}, jerr
	}
	if err := r.root.Rename(r.path, dest, true); err != nil {
		return Entry{}, mapVFSErr(err)
	}

	c.markDirty(ctx, r.share, r.path)
	c.markDirty(ctx, r.share, dest)
	c.record(ctx, r, journal.OpMove)
	destResolved := Resolved{user: r.user, share: r.share, root: r.root, path: dest, perms: r.perms}
	return c.buildEntry(destResolved, newName, dest), nil
}

// MoveOpts carries what a move needs beyond the two ends.
type MoveOpts struct {
	// Overwrite replaces the destination. Off means a conflict is returned.
	Overwrite bool
	// IfMatch validates the destination when it exists. Weak is refused.
	IfMatch *Token
}

// MoveResult says what a move did, which is not always exactly a move.
type MoveResult struct {
	// WillCopy reports that a same-name destination across a device or a
	// share boundary had to be copied and deleted rather than renamed, which
	// the UI warns about before the user commits.
	WillCopy bool
	// Created is the path the entry landed at.
	Created vfs.SafePath
	// Moved reports a plain rename happened.
	Moved bool
}

// Move moves from to to within a share, or copies and deletes across shares.
// Across shares it is not atomic, and it says so in the return rather than
// pretending otherwise.
func (c *Core) Move(ctx context.Context, from, to Resolved, opt MoveOpts) (MoveResult, error) {
	if err := from.Require(acl.Move); err != nil {
		return MoveResult{}, err
	}
	if err := to.Require(acl.Create); err != nil {
		return MoveResult{}, err
	}
	if from.path.IsRoot() {
		return MoveResult{}, errf(ErrDenied, "move a share root")
	}
	if to.path.IsRoot() {
		return MoveResult{}, errf(ErrDenied, "move onto a share root")
	}
	if from.share == to.share && from.path.Equal(to.path) {
		return MoveResult{Created: to.path}, nil
	}

	srcSt, err := from.root.Stat(from.path)
	if err != nil {
		return MoveResult{}, mapVFSErr(err)
	}

	destExists, derr := pathExists(to.root, to.path)
	if derr != nil {
		return MoveResult{}, derr
	}
	if destExists {
		if !opt.Overwrite {
			return MoveResult{}, ErrConflict
		}
		dstSt, serr := to.root.Stat(to.path)
		if serr != nil {
			return MoveResult{}, mapVFSErr(serr)
		}
		if err := precondition(opt.IfMatch, dstSt); err != nil {
			return MoveResult{}, err
		}
	}

	willCopy := from.share != to.share || to.root.Dev() != srcSt.Dev
	res := MoveResult{WillCopy: willCopy}

	if !willCopy {
		if err := to.root.Rename(from.path, to.path, !opt.Overwrite); err != nil {
			return MoveResult{}, mapVFSErr(err)
		}
		res.Moved = true
	} else {
		if err := c.copyRecursive(ctx, from, to, srcSt); err != nil {
			return MoveResult{}, err
		}
		if err := c.deleteResolved(ctx, from, srcSt, false); err != nil {
			// The destination is complete and the copy has committed; a
			// failure to remove the source leaves a duplicate, which is
			// reported rather than silently dropped.
			return MoveResult{}, errf(ErrCrossShare, "the copy completed but removing the source failed")
		}
	}

	c.markDirty(ctx, from.share, from.path)
	c.markDirty(ctx, to.share, to.path)
	c.record(ctx, from, journal.OpMove)
	res.Created = to.path
	return res, nil
}

// copyRecursive duplicates a subtree into a destination path. Files go
// through the VFS copy-range helper: a reflink on btrfs and XFS when aligned,
// an in-kernel copy otherwise.
func (c *Core) copyRecursive(ctx context.Context, from, to Resolved, srcSt vfs.Stat) error {
	if srcSt.Kind.IsDir() {
		if _, err := c.Mkdir(ctx, to); err != nil {
			if !errors.Is(err, ErrExists) {
				return err
			}
		}
		entries, err := from.root.ReadDir(from.path, vfs.HideReserved)
		if err != nil {
			return mapVFSErr(err)
		}
		for _, e := range entries {
			childFromPath, jerr := from.path.JoinExisting(e.Name)
			if jerr != nil {
				continue
			}
			childToPath, jerr := to.path.JoinExisting(e.Name)
			if jerr != nil {
				continue
			}
			cst, serr := from.root.Stat(childFromPath)
			if serr != nil {
				continue
			}
			childFrom := Resolved{user: from.user, share: from.share, root: from.root, path: childFromPath, perms: from.perms}
			childTo := Resolved{user: to.user, share: to.share, root: to.root, path: childToPath, perms: to.perms}
			if err := c.copyRecursive(ctx, childFrom, childTo, cst); err != nil {
				return err
			}
		}
		return nil
	}
	return c.copyFile(ctx, from, to)
}

// copyFile copies one file onto the destination. The destination may
// pre-exist, in which case it is replaced atomically through WriteDurable.
func (c *Core) copyFile(ctx context.Context, from, to Resolved) error {
	src, err := from.root.OpenRead(from.path, vfs.IntentRead)
	if err != nil {
		return mapVFSErr(err)
	}
	defer func() {
		if cerr := src.Close(); cerr != nil {
			c.logger.Warn("closing a source handle failed", slog.Any("error", cerr))
		}
	}()
	srcSt, err := src.Stat()
	if err != nil {
		return mapVFSErr(err)
	}

	mode := to.root.Policy().ModeFile
	if _, werr := to.root.WriteDurable(to.path, vfs.DurableOpts{Mode: mode}, func(dst *vfs.File) error {
		_, cerr := vfs.CopyRange(src, 0, dst, 0, srcSt.Size)
		return cerr
	}); werr != nil {
		return mapVFSErr(werr)
	}
	c.markDirty(ctx, to.share, to.path)
	c.record(ctx, to, journal.OpCopy)
	return nil
}

// Delete removes one entry. When trash is enabled on the share it moves into
// the trash instead; permanent is the caller spelling it out.
func (c *Core) Delete(ctx context.Context, r Resolved, permanent bool) error {
	if err := r.Require(acl.Delete); err != nil {
		return err
	}
	st, err := r.root.Stat(r.path)
	if err != nil {
		return mapVFSErr(err)
	}

	if e, ok := c.Share(r.share); ok && e.TrashEnabled && !permanent {
		return c.trashMove(ctx, r, st)
	}
	return c.deleteResolved(ctx, r, st, true)
}

// deleteResolved performs a permanent delete, crediting the quota ledger for
// the freed bytes. A directory's size comes from the recursive aggregate, so
// the ledger gets what was actually on disk, read before the delete.
func (c *Core) deleteResolved(ctx context.Context, r Resolved, st vfs.Stat, charge bool) error {
	freed := uint64(0)
	if st.Kind.IsDir() {
		agg, aerr := c.Aggregate(ctx, r.share, r.path)
		if aerr != nil {
			return aerr
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

// deleteRecursive walks from the root down, deleting children before parents.
func (c *Core) deleteRecursive(ctx context.Context, r Resolved) error {
	entries, err := r.root.ReadDir(r.path, vfs.HideReserved)
	if err != nil {
		return mapVFSErr(err)
	}
	for _, e := range entries {
		childPath, jerr := r.path.JoinExisting(e.Name)
		if jerr != nil {
			continue
		}
		child := Resolved{user: r.user, share: r.share, root: r.root, path: childPath, perms: r.perms}
		st, serr := r.root.Stat(childPath)
		if serr != nil {
			continue
		}
		if st.Kind.IsDir() {
			if err := c.deleteRecursive(ctx, child); err != nil {
				return err
			}
		} else {
			if err := r.root.Unlink(childPath); err != nil {
				return mapVFSErr(err)
			}
		}
	}
	if err := r.root.Rmdir(r.path); err != nil {
		return mapVFSErr(err)
	}
	return nil
}

// Stat returns the entry for one path. A single named path is the bounded
// case where a stable id is worth minting, which is what a share-link target
// needs.
func (c *Core) Stat(ctx context.Context, r Resolved) (Entry, error) {
	if err := r.Require(acl.Read); err != nil {
		return Entry{}, err
	}
	return c.buildEntry(r, r.path.Name(), r.path), nil
}

// requireCreatableLeaf applies the creation table to a leaf about to be
// brought into existence. Anything already on the share stays fully usable;
// nothing typed through this server adds a name a Windows or SMB client could
// never open.
func requireCreatableLeaf(p vfs.SafePath) error {
	if p.IsRoot() {
		return nil
	}
	_, err := p.Parent().Join(p.Name())
	return err
}

// pathExists stats a path and reports whether it is there, folding the
// missing answer to false. It is the one way a mutation asks "is the
// destination occupied" without converting a refusal.
func pathExists(root *vfs.ShareRoot, p vfs.SafePath) (bool, error) {
	_, err := root.Stat(p)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, vfs.ErrNotFound):
		return false, nil
	default:
		return false, mapVFSErr(err)
	}
}

// Token is a caller-supplied validator: the current ETag the client last saw,
// sent to prove nothing changed in between.
type Token string

// precondition is the F11 gate. RFC 9110 requires strong comparison for an
// If-Match validator, and a file ETag is always weak, so a supplied validator
// can never match and is always refused, carrying the current token so a
// conflict screen can show it. Requiring an explicit unconditional retry is
// the only way past.
func precondition(ifMatch *Token, st vfs.Stat) error {
	if ifMatch == nil {
		return nil
	}
	cur, _ := FileETag(st)
	return &PreconditionError{Current: cur}
}

// record writes one journal row after a successful mutation. It happens after
// the write already succeeded, so a failure here is logged and dropped and
// nothing may treat a missing row as evidence.
func (c *Core) record(ctx context.Context, r Resolved, op journal.Op) {
	if c.journal == nil {
		return
	}
	acc, nerr := num.Narrow[uint32](int64(r.user))
	if nerr != nil {
		c.warn("a user id does not fit the journal's account column; skipping the row",
			"user", int64(r.user))
		return
	}
	ev := journal.Event{
		Account: acc,
		Share:   r.share,
		Path:    r.path.Share(),
		Op:      op,
	}
	if err := c.journal.Record(ctx, ev); err != nil {
		c.warn("recording a write to the journal failed; the write itself has committed",
			"error", err)
	}
}
